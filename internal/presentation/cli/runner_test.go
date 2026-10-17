package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/applyqueue"
	"github.com/Liapoldus/cli/internal/infrastructure/coreapi"
	"github.com/Liapoldus/cli/internal/infrastructure/git"
	"github.com/Liapoldus/cli/internal/infrastructure/targets"
)

func TestApplyUsesRequestedGitRevision(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/orders/settings.json", `{"version":1}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "config", "user.email", "test@example.test")
	runRunnerGit(t, project, "config", "user.name", "Test")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "commit", "-m", "first")
	first := runRunnerGit(t, project, "rev-parse", "HEAD")
	writeRunnerFile(t, project, "services/orders/settings.json", `{"version":2}`)
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "commit", "-m", "second")

	var request domain.ConfigBundleRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, requestHTTP *http.Request) {
		if err := json.NewDecoder(requestHTTP.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-1"}`))
	}))
	defer server.Close()

	var output bytes.Buffer
	runner := Runner{Source: git.Repository{}, TokenReader: strings.NewReader("secret"), CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
		return coreapi.New(connection)
	}}
	code := runner.Run([]string{
		"apply", "--project", project, "--revision", first, "--target", "production",
		"--expected-generation", "41",
		"--endpoint", server.URL, "--token-stdin", "--confirm",
	}, &output, &output)
	if code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if request.Project.Revision != first || request.Target.ExpectedGeneration == nil || *request.Target.ExpectedGeneration != 41 || len(request.Bundle.Services) != 1 || string(request.Bundle.Services[0].Settings) != `{"version":1}` {
		t.Fatalf("request does not match requested revision: %+v", request)
	}
}

func TestDevApplyUsesImmutableWorkingTreeSnapshotOnLocalTarget(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"committed"}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	writeRunnerFile(t, project, "services/echo/links/server.json", `{"caller":"echo","target":"server"}`)
	workingTreeBefore := runRunnerGit(t, project, "status", "--porcelain")

	var received domain.ConfigBundleRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, requestHTTP *http.Request) {
		switch requestHTTP.URL.Path {
		case "/api/config-bundles/plan":
			var planned domain.ConfigBundleRequest
			if err := json.NewDecoder(requestHTTP.Body).Decode(&planned); err != nil {
				t.Errorf("decode plan request: %v", err)
			}
			_, _ = fmt.Fprintf(response, `{"plan":{"valid":true,"digest":%q,"services":[{"id":"echo","currentRevision":7,"currentDigest":"sha256:current","schemaVersion":1}]},"requestId":"req-plan"}`, planned.Bundle.Digest)
		case "/api/config-bundles/apply":
			if err := json.NewDecoder(requestHTTP.Body).Decode(&received); err != nil {
				t.Errorf("decode Core request: %v", err)
			}
			if key := requestHTTP.Header.Get("Idempotency-Key"); !strings.HasPrefix(key, "dev-apply-") {
				t.Errorf("missing generated development idempotency key: %q", key)
			}
			_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-dev"}`))
		default:
			t.Errorf("unexpected Core request: %s %s", requestHTTP.Method, requestHTTP.URL.Path)
			http.NotFound(response, requestHTTP)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	runner := Runner{
		Source: git.Repository{}, Snapshots: git.Snapshotter{}, TokenReader: strings.NewReader("local-secret"),
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	code := runner.Run([]string{
		"dev", "apply", "--project", project, "--target", "local", "--confirm",
		"--endpoint", server.URL, "--token-stdin",
	}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("dev apply failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if received.Project.ID != "demo" || received.Project.Revision != "working-tree:"+strings.TrimPrefix(received.Bundle.Digest, "sha256:") || received.Target.Environment != "local" {
		t.Fatalf("Core request has invalid working-tree identity: %+v", received)
	}
	if len(received.Bundle.Services) != 1 || string(received.Bundle.Services[0].Settings) != `{"message":"draft"}` || len(received.Bundle.Links) != 1 || string(received.Bundle.Links[0]) != `{"caller":"echo","target":"server"}` {
		t.Fatalf("Core did not receive the current draft configuration: %+v", received.Bundle)
	}
	if after := runRunnerGit(t, project, "status", "--porcelain"); after != workingTreeBefore {
		t.Fatalf("dev apply changed the source Git checkout: before=%q after=%q", workingTreeBefore, after)
	}
	if strings.Contains(stdout.String()+stderr.String(), "local-secret") {
		t.Fatal("Core credential leaked into command output")
	}
}

func TestDevApplyReusesKeyForSameRevisionAndChangesAfterAcceptedGeneration(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	var planReads int
	var idempotencyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/config-bundles/plan":
			var input domain.ConfigBundleRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode plan request: %v", err)
			}
			revisions := []int{7, 7, 8}
			revision := revisions[planReads]
			planReads++
			_, _ = fmt.Fprintf(response, `{"plan":{"valid":true,"digest":%q,"services":[{"id":"echo","currentRevision":%d,"currentDigest":"sha256:current","schemaVersion":1}]},"requestId":"req-plan"}`, input.Bundle.Digest, revision)
		case "/api/config-bundles/apply":
			idempotencyKeys = append(idempotencyKeys, request.Header.Get("Idempotency-Key"))
			_, _ = fmt.Fprintf(response, `{"operations":[],"requestId":"req-%d"}`, len(idempotencyKeys))
		default:
			t.Errorf("unexpected Core request: %s", request.URL.Path)
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	for index := 0; index < 3; index++ {
		var stdout, stderr bytes.Buffer
		runner := Runner{
			Source: git.Repository{}, Snapshots: git.Snapshotter{}, TokenReader: strings.NewReader("local-secret"),
			CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
		}
		code := runner.Run([]string{
			"dev", "apply", "--project", project, "--target", "local", "--confirm",
			"--endpoint", server.URL, "--token-stdin",
		}, &stdout, &stderr)
		if code != application.ExitOK {
			t.Fatalf("dev apply #%d failed: code=%d stdout=%s stderr=%s", index+1, code, stdout.String(), stderr.String())
		}
	}
	if len(idempotencyKeys) != 3 || idempotencyKeys[0] == "" || idempotencyKeys[0] != idempotencyKeys[1] || idempotencyKeys[1] == idempotencyKeys[2] {
		t.Fatalf("unexpected retry/new-generation keys: %q", idempotencyKeys)
	}
}

func TestDevApplyRecoversAcceptedOperationAcrossRunnerProcesses(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	journal, err := applyqueue.Default()
	if err != nil {
		t.Fatal(err)
	}
	journal.Directory = t.TempDir()
	var firstTargetKey string
	firstKey := ""
	var resolves, operationReads, applies, planReads int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/config-bundles/plan":
			var input domain.ConfigBundleRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode plan: %v", err)
			}
			revision := 7
			if planReads > 0 {
				revision = 8
			}
			planReads++
			_, _ = fmt.Fprintf(response, `{"plan":{"valid":true,"digest":%q,"services":[{"id":"echo","currentRevision":%d,"currentDigest":"sha256:current","schemaVersion":1}]},"requestId":"req-plan"}`, input.Bundle.Digest, revision)
		case "/api/config-bundles/apply":
			applies++
			key := request.Header.Get("Idempotency-Key")
			if applies == 1 {
				firstKey = key
			} else if key == firstKey {
				t.Errorf("later generation reused old key %q", key)
			}
			if applies == 1 {
				_, _ = response.Write([]byte(`{"operations":["op-prev"],"requestId":"req-1"}`))
				return
			}
			_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-2"}`))
		case "/api/operations/resolve":
			resolves++
			var input struct {
				Scope string   `json:"scope"`
				Keys  []string `json:"keys"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input.Scope == "" || len(input.Keys) != 1 || input.Keys[0] != firstKey+":echo" {
				t.Errorf("unexpected recovery lookup: %+v err=%v", input, err)
			}
			_, _ = response.Write([]byte(`{"items":[{"id":"op-prev","state":"running","resourceId":"echo"}],"requestId":"req-resolve"}`))
		case "/api/operations/op-prev":
			states := []string{"running", "succeeded"}
			state := states[operationReads]
			operationReads++
			outcomeState := "pending"
			if state == "succeeded" {
				outcomeState = "applied"
			}
			_, _ = fmt.Fprintf(response, `{"id":"op-prev","kind":"service-settings-apply","state":%q,"createdAt":"2026-10-16T10:00:00Z","requestId":"req-prev","replicaOutcomes":[{"replicaId":"echo-1","desiredGeneration":3,"state":%q}]}`, state, outcomeState)
		default:
			t.Errorf("unexpected Core request: %s", request.URL.Path)
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	firstTargetKey = applyqueue.TargetKey("local", server.URL)
	firstRunner := Runner{Source: git.Repository{}, Snapshots: git.Snapshotter{}, ApplyRecovery: journal, TokenReader: strings.NewReader("secret"), CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
	args := []string{"dev", "apply", "--project", project, "--target", "local", "--confirm", "--endpoint", server.URL, "--token-stdin"}
	var output bytes.Buffer
	if code := firstRunner.Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("first apply code=%d output=%s", code, output.String())
	}
	if _, found, err := journal.Load(firstTargetKey); err != nil || !found {
		t.Fatalf("accepted operation journal found=%v err=%v", found, err)
	}
	secondRunner := Runner{Source: git.Repository{}, Snapshots: git.Snapshotter{}, ApplyRecovery: journal, TokenReader: strings.NewReader("secret"), CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
	output.Reset()
	if code := secondRunner.Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("second apply code=%d output=%s", code, output.String())
	}
	if resolves != 1 || operationReads != 2 || applies != 2 {
		t.Fatalf("resolve=%d operationReads=%d applies=%d", resolves, operationReads, applies)
	}
	if _, found, err := journal.Load(firstTargetKey); err != nil || found {
		t.Fatalf("terminal recovery journal found=%v err=%v", found, err)
	}
}

func TestDevApplyFailedRecoveryRequiresExplicitRetryAndUsesFreshKey(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	journal := applyqueue.Journal{Directory: t.TempDir()}
	var targetKey string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/operations/resolve":
			_, _ = response.Write([]byte(`{"items":[{"id":"op-failed","state":"failed","resourceId":"echo","errorCode":"apply_failed"}],"requestId":"req-resolve"}`))
		case "/api/config-bundles/plan":
			var input domain.ConfigBundleRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode plan: %v", err)
			}
			_, _ = fmt.Fprintf(response, `{"plan":{"valid":true,"digest":%q,"services":[{"id":"echo","currentRevision":4,"currentDigest":"sha256:current","schemaVersion":1}]},"requestId":"req-plan"}`, input.Bundle.Digest)
		case "/api/config-bundles/apply":
			key := request.Header.Get("Idempotency-Key")
			if !strings.HasPrefix(key, "dev-retry-") || key == "previous-key:echo" {
				t.Errorf("explicit retry did not use a fresh idempotency key: %q", key)
			}
			_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-apply"}`))
		default:
			t.Errorf("unexpected Core request: %s", request.URL.Path)
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	targetKey = applyqueue.TargetKey("local", server.URL)
	if err := journal.Save(targetKey, domain.ApplyIntent{Scope: "config-bundle/previous-revision", Keys: []string{"previous-key:echo"}}); err != nil {
		t.Fatal(err)
	}
	newRunner := func() Runner {
		return Runner{Source: git.Repository{}, Snapshots: git.Snapshotter{}, ApplyRecovery: journal,
			TokenReader: strings.NewReader("local-secret"),
			CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
	}
	args := []string{"dev", "apply", "--project", project, "--target", "local", "--confirm", "--endpoint", server.URL, "--token-stdin"}
	var output bytes.Buffer
	if code := newRunner().Run(args, &output, &output); code == application.ExitOK || !strings.Contains(output.String(), "--retry-failed") {
		t.Fatalf("failed operation was not paused for explicit retry: code=%d output=%s", code, output.String())
	}
	if _, found, err := journal.Load(targetKey); err != nil || !found {
		t.Fatalf("failed operation journal was cleared before confirmation: found=%v err=%v", found, err)
	}
	output.Reset()
	args = append(args, "--retry-failed")
	if code := newRunner().Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("explicit failed-operation retry failed: code=%d output=%s", code, output.String())
	}
	if _, found, err := journal.Load(targetKey); err != nil || found {
		t.Fatalf("recovered retry journal remains: found=%v err=%v", found, err)
	}
}

func TestStudioDevApplyWaitStreamsCoreOperationStateOnly(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	writeRunnerFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")

	var operationReads int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/config-bundles/plan":
			var planned domain.ConfigBundleRequest
			if err := json.NewDecoder(request.Body).Decode(&planned); err != nil {
				t.Errorf("decode plan request: %v", err)
			}
			_, _ = fmt.Fprintf(response, `{"plan":{"valid":true,"digest":%q,"services":[{"id":"echo","currentRevision":3,"currentDigest":"sha256:current","schemaVersion":1}]},"requestId":"req-plan"}`, planned.Bundle.Digest)
		case "/api/config-bundles/apply":
			_, _ = response.Write([]byte(`{"operations":["op-1"],"requestId":"req-dev"}`))
		case "/api/operations/op-1":
			states := []string{"pending", "running", "succeeded"}
			index := operationReads
			if index >= len(states) {
				index = len(states) - 1
			}
			operationReads++
			outcomeState := "pending"
			if states[index] == "succeeded" {
				outcomeState = "applied"
			}
			_, _ = fmt.Fprintf(response, `{"id":"op-1","kind":"service-settings-apply","state":%q,"createdAt":"2026-10-16T10:00:00Z","requestId":"req-dev","replicaOutcomes":[{"replicaId":"server-1","desiredGeneration":5,"state":%q}]}`, states[index], outcomeState)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	runner := Runner{
		Source: git.Repository{}, Snapshots: git.Snapshotter{}, TokenReader: strings.NewReader("local-secret"),
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	code := runner.Run([]string{
		"dev", "apply", "--project", project, "--target", "local", "--confirm", "--wait",
		"--endpoint", server.URL, "--token-stdin", "--studio-events",
	}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("Studio dev apply failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var events []studioEvent
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var event studioEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	var states []string
	var gotReplicaOutcomes bool
	for _, event := range events {
		if event.Type == "operation.updated" {
			states = append(states, event.State)
			var safe struct {
				OperationID     string                           `json:"operationId"`
				Target          string                           `json:"target"`
				State           string                           `json:"state"`
				ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes"`
			}
			if err := json.Unmarshal(event.Payload, &safe); err != nil || safe.OperationID != "op-1" || safe.Target != "local" || safe.State != event.State {
				t.Fatalf("unexpected safe operation payload %s: %v", event.Payload, err)
			}
			if len(safe.ReplicaOutcomes) == 1 {
				outcome := safe.ReplicaOutcomes[0]
				gotReplicaOutcomes = outcome.ReplicaID == "server-1" && outcome.DesiredGeneration == 5 && outcome.State == "applied"
			}
		}
		if event.Type == "run.output" {
			var result struct {
				ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes"`
			}
			if err := json.Unmarshal(event.Payload, &result); err == nil && len(result.ReplicaOutcomes) == 1 {
				outcome := result.ReplicaOutcomes[0]
				gotReplicaOutcomes = outcome.ReplicaID == "server-1" && outcome.DesiredGeneration == 5 && outcome.State == "applied"
			}
		}
	}
	if !reflect.DeepEqual(states, []string{"pending", "running", "succeeded"}) {
		t.Fatalf("Core-backed state events=%v", states)
	}
	if !gotReplicaOutcomes {
		t.Fatalf("typed final replica outcomes missing from Studio output: %s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "local-secret") {
		t.Fatal("Core credential leaked into Studio JSONL events")
	}
}

func TestDevApplyRejectsNonLocalTargetAndUnconfirmedMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "remote", args: []string{"dev", "apply", "--project", "/tmp/project", "--target", "production", "--confirm"}, want: "require --target local"},
		{name: "unconfirmed", args: []string{"dev", "apply", "--project", "/tmp/project", "--target", "local"}, want: "requires --confirm"},
		{name: "retry on plan", args: []string{"dev", "plan", "--project", "/tmp/project", "--target", "local", "--retry-failed"}, want: "only valid with dev apply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			code := (Runner{}).Run(test.args, &output, &output)
			if code != application.ExitArguments || !strings.Contains(output.String(), test.want) {
				t.Fatalf("code=%d output=%s", code, output.String())
			}
		})
	}
}

func TestRetryFailedFlagIsRejectedOutsideDevelopmentApply(t *testing.T) {
	var output bytes.Buffer
	code := (Runner{}).Run([]string{"version", "--retry-failed"}, &output, &output)
	if code != application.ExitArguments || !strings.Contains(output.String(), "only valid with dev apply") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
}

func TestObserveTrafficPublishesProjectScopedReport(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "config", "user.email", "test@example.test")
	runRunnerGit(t, project, "config", "user.name", "Test")
	runRunnerGit(t, project, "add", "project.yaml")
	runRunnerGit(t, project, "commit", "-m", "initial")
	reportPath := filepath.Join(project, "reports", "traffic.json")
	writeRunnerFile(t, project, "reports/traffic.json", `{"schemaVersion":"studio-reports/v1","projectId":"demo","target":"local","traffic":[{"method":"GET"}]}`)
	registryPath := filepath.Join(t.TempDir(), "targets.json")
	registry := targets.Registry{Path: registryPath}
	if err := registry.Add(domain.Target{Name: "local", Environment: "local", Transport: domain.TransportLocalProcess, TrafficReportPath: "reports/traffic.json"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := Runner{Source: git.Repository{}, Registry: registry}
	code := runner.Run([]string{"observe", "traffic", "--project", project, "--target", "local"}, &output, &output)
	if code != application.ExitOK || !strings.Contains(output.String(), `"reportPath":"reports/traffic.json"`) || !strings.Contains(output.String(), `"digest":"sha256:`) {
		t.Fatalf("code=%d output=%s report=%s", code, output.String(), reportPath)
	}
}

func TestHistoricalTrafficReportBytesAndDigestRemainUnchanged(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "config", "user.email", "test@example.test")
	runRunnerGit(t, project, "config", "user.name", "Test")
	runRunnerGit(t, project, "add", "project.yaml")
	runRunnerGit(t, project, "commit", "-m", "initial")
	const report = "{\n \"schemaVersion\":\"studio-reports/v1\",\"projectId\":\"demo\",\"target\":\"local\",\n \"traffic\":[{\"serviceId\":\"echo\",\"method\":\"echo.invoke\",\"path\":\"/api/services/echo\"}]\n}\n"
	writeRunnerFile(t, project, "reports/history.json", report)
	runner := Runner{Source: git.Repository{}, Registry: targetRegistryStub{target: domain.Target{Name: "local", TrafficReportPath: "reports/history.json"}}}
	var stdout, stderr bytes.Buffer
	if code := runner.Run([]string{"observe", "traffic", "--project", project, "--target", "local"}, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("observe failed: code=%d error=%s", code, stderr.String())
	}
	var result struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(report))) {
		t.Fatalf("historical digest changed: %s, %v", stdout.String(), err)
	}
	after, err := os.ReadFile(filepath.Join(project, "reports", "history.json"))
	if err != nil || string(after) != report {
		t.Fatalf("historical report rewritten: %q, %v", after, err)
	}
}

func TestApplyRequiresConfirmationAndCleanTree(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "config", "user.email", "test@example.test")
	runRunnerGit(t, project, "config", "user.name", "Test")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "commit", "-m", "initial")
	var output bytes.Buffer
	runner := Runner{Source: git.Repository{}, TokenReader: strings.NewReader("secret"), CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
		return coreapi.New(connection)
	}}
	code := runner.Run([]string{
		"apply", "--project", project, "--revision", "HEAD", "--target", "production",
		"--endpoint", "http://127.0.0.1:1", "--token-stdin",
	}, &output, &output)
	if code != application.ExitArguments || !strings.Contains(output.String(), "--confirm") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	writeRunnerFile(t, project, "draft.txt", "draft")
	output.Reset()
	code = runner.Run([]string{
		"apply", "--project", project, "--revision", "HEAD", "--target", "production",
		"--endpoint", "http://127.0.0.1:1", "--token-stdin", "--confirm",
	}, &output, &output)
	if code != application.ExitRevision || !strings.Contains(output.String(), "clean") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
}

func TestRemoteRequiredTargetRejectsApplyWithoutApprovalEvidence(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: demo\n")
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "config", "user.email", "test@example.test")
	runRunnerGit(t, project, "config", "user.name", "Test")
	runRunnerGit(t, project, "remote", "add", "origin", "https://github.com/Liapoldus/demo.git")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "commit", "-m", "initial")
	revision := runRunnerGit(t, project, "rev-parse", "HEAD")

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, requestHTTP *http.Request) {
		_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-1"}`))
	}))
	defer server.Close()
	registryPath := filepath.Join(t.TempDir(), "targets.json")
	registryBody := `{"targets":[{"name":"production","environment":"production","transport":"remote-api","approval":"remote-required","endpoint":"` + server.URL + `"}]}`
	if err := os.WriteFile(registryPath, []byte(registryBody), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Source:      git.Repository{},
		TokenReader: strings.NewReader("secret"),
		Registry:    targets.Registry{Path: registryPath},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
			return coreapi.New(connection)
		},
	}
	args := []string{"apply", "--project", project, "--revision", revision, "--target", "production", "--token-stdin", "--confirm"}
	var output bytes.Buffer
	if code := runner.Run(args, &output, &output); code != application.ExitRevision || !strings.Contains(output.String(), "approval-evidence") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}

	evidencePath := filepath.Join(t.TempDir(), "approval.json")
	evidence := domain.ApprovalEvidence{Repository: "https://github.com/Liapoldus/demo.git", Revision: revision, Target: "production", Approved: true, Source: "github", EvidenceID: "run-1"}
	body, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	args = append(args, "--approval-evidence", evidencePath)
	if code := runner.Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("approved code=%d output=%s", code, output.String())
	}
}

func TestRunnerResolvesTargetTokenThroughCredentialProvider(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "targets.json")
	profile, err := json.Marshal(map[string]any{"targets": []domain.Target{{Name: "production", Transport: domain.TransportRemoteAPI, Endpoint: "http://core.example", CredentialRef: domain.ManagementCredentialReference("production")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, profile, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Registry:    targets.Registry{Path: registryPath},
		Credentials: credentialProviderStub{value: "secret-value"},
	}
	connection, err := runner.credentials(context.Background(), commandOptions{target: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Endpoint != "http://core.example" || connection.Token != "secret-value" {
		t.Fatalf("connection=%+v", connection)
	}
}

func TestRunnerReadsTokenFromStdinWithoutIncludingItInErrors(t *testing.T) {
	runner := Runner{TokenReader: strings.NewReader("stdin-secret\n")}
	connection, err := runner.credentials(context.Background(), commandOptions{
		endpoint: "http://core.example", target: "local", tokenStdin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Token != "stdin-secret" {
		t.Fatalf("token=%q", connection.Token)
	}

	runner.TokenReader = strings.NewReader("")
	_, err = runner.credentials(context.Background(), commandOptions{
		endpoint: "http://core.example", target: "local", tokenStdin: true,
	})
	if err == nil || strings.Contains(err.Error(), "stdin-secret") {
		t.Fatalf("error=%v", err)
	}
}

func TestStudioEventsModeEmitsVersionedJSONLAndHidesPlainErrors(t *testing.T) {
	var output bytes.Buffer
	runner := Runner{}
	if code := runner.Run([]string{"version", studioEventsFlag}, &output, &output); code != application.ExitOK {
		t.Fatalf("version code=%d output=%s", code, output.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("events=%q", lines)
	}
	var started, result, completed studioEvent
	if err := json.Unmarshal([]byte(lines[0]), &started); err != nil || started.Type != "run.started" || started.SchemaVersion != "cli-events/v1" {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &result); err != nil || result.Type != "run.output" || string(result.Payload) != `{"version":"dev"}` {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &completed); err != nil || completed.Type != "run.completed" || completed.ExitCode == nil || *completed.ExitCode != 0 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}

	output.Reset()
	if code := runner.Run([]string{"unsupported", studioEventsFlag}, &output, &output); code != application.ExitArguments {
		t.Fatalf("unsupported code=%d output=%s", code, output.String())
	}
	if !strings.Contains(output.String(), "unsupported command") || !strings.Contains(output.String(), `"type":"diagnostic"`) || !strings.Contains(output.String(), `"type":"run.failed"`) {
		t.Fatalf("actionable diagnostic or terminal event missing: %s", output.String())
	}
}

func TestStudioDiagnosticRedactsCredentialValuesAndBoundsMessage(t *testing.T) {
	message := safeStudioDiagnostic("Core status: token=top-secret; " + strings.Repeat("x", 600))
	pathMessage := safeStudioDiagnostic("Core status failed; profile=/Users/alice/private/target.json")
	if strings.Contains(message, "top-secret") || len(message) > 512+len("…") || strings.Contains(pathMessage, "/Users/alice") || !strings.Contains(pathMessage, "[redacted path]") {
		t.Fatalf("unsafe diagnostic: %q", message)
	}
}

func TestStudioEventWriterPreservesKnownTypedDockerDiagnosticCode(t *testing.T) {
	var output bytes.Buffer
	writer := &studioEventWriter{output: &output, runID: "run-core", kind: "stderr"}
	line := "Core start: start Core Docker container: docker_mount_source_unavailable: Docker cannot access the Core state directory for target local; path=/private/tmp/core/state"
	if _, err := writer.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	var event studioEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "diagnostic" || event.Severity != "error" || event.Code != "docker_mount_source_unavailable" || !strings.Contains(event.Message, "Core state directory") {
		t.Fatalf("typed mount diagnostic was collapsed: %+v", event)
	}
	if strings.Contains(event.Message, "docker_mount_source_unavailable") || strings.Contains(event.Message, "/private/tmp") {
		t.Fatalf("diagnostic code/path leaked into message: %+v", event)
	}
}

func TestStudioEventWriterExplainsExpiredServiceReplicaIncarnation(t *testing.T) {
	var output bytes.Buffer
	writer := &studioEventWriter{output: &output, runID: "run-expired-replica", kind: "stderr"}
	line := "Core registration for server: service SDK replica lease expired"
	if _, err := writer.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	var event studioEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "diagnostic" || event.Severity != "error" || event.Code != serviceReplicaLeaseExpiredCode {
		t.Fatalf("expired incarnation was not preserved as a typed diagnostic: %+v", event)
	}
	if !strings.Contains(event.Message, "new operator-signed service identity/certificate") || !strings.Contains(event.Message, "same certificate cannot recover") {
		t.Fatalf("diagnostic omitted the safe recovery step: %+v", event)
	}
	if strings.Contains(event.Message, "Core registration for server") {
		t.Fatalf("diagnostic should not echo untrusted raw stderr: %+v", event)
	}
}

func TestStudioEventWriterPreservesReportAndOperationMetadata(t *testing.T) {
	var output bytes.Buffer
	writer := &studioEventWriter{output: &output, runID: "run-1", kind: "stdout"}
	if _, err := writer.Write([]byte(`{"operationId":"op-1","target":"local","state":"running"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(`{"reportPath":".studio/reports/op-1.json","digest":"sha256:abc"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"type":"operation.updated"`) || !strings.Contains(lines[0], `"operationId":"op-1"`) || !strings.Contains(lines[1], `"type":"report.available"`) || !strings.Contains(lines[1], `.studio/reports/op-1.json`) {
		t.Fatalf("unexpected events: %s", output.String())
	}
}

func TestStudioEventWriterSurfacesRedactedPlainLogOutput(t *testing.T) {
	var output bytes.Buffer
	writer := &studioEventWriter{output: &output, runID: "run-logs", kind: "stdout"}
	if _, err := writer.Write([]byte("core ready; token=do-not-leak\n")); err != nil {
		t.Fatal(err)
	}
	var event studioEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "run.output" || bytes.Contains(event.Payload, []byte("do-not-leak")) || !bytes.Contains(event.Payload, []byte("[redacted]")) {
		t.Fatalf("plain log output was not safely surfaced: %+v", event)
	}
}

func TestStudioEventWriterRedactsBearerAndPrivateKeyLogLines(t *testing.T) {
	if got := safeStudioDiagnostic("Authorization: Bearer do-not-leak"); strings.Contains(got, "do-not-leak") {
		t.Fatalf("bearer credential leaked: %q", got)
	}
	if got := safeStudioDiagnostic("-----BEGIN PRIVATE KEY-----"); strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("private key marker leaked: %q", got)
	}
}

func TestCoreRestartOnlyRestartsCoreContainer(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, false)
	registry := targetRegistryStub{target: target}
	label := runtimeLabel("target", target.Name)
	docker := &dockerRuntimeStub{
		containers:  []string{coreContainer, "liapoldus-echo", "liapoldus-server"},
		inspectBody: []byte(fmt.Sprintf(`[{"State":{"Status":"running","Running":true},"Config":{"Labels":{"com.liapoldus.runtime":%q}}}]`, label)),
	}
	vault := &memoryCredentialVault{values: map[string]string{target.CredentialRef: "existing-management-token-which-is-long-enough"}}
	var output bytes.Buffer
	runner := Runner{Registry: registry, Docker: docker, Credentials: vault, CredentialVault: vault,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil }}
	if code := runner.Run([]string{"core", "restart", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if docker.runCount != 0 || len(docker.restarted) != 1 || docker.restarted[0] != coreContainer || len(docker.stopped) != 0 || len(docker.removed) != 0 || len(docker.networkRemoved) != 0 {
		t.Fatalf("Core restart affected other runtime resources: runs=%d restarted=%v stopped=%v removed=%v networks=%v", docker.runCount, docker.restarted, docker.stopped, docker.removed, docker.networkRemoved)
	}
	if !strings.Contains(output.String(), `"restarted":true`) || !strings.Contains(output.String(), `"container":"liapoldus-core"`) {
		t.Fatalf("unexpected Core restart result: %s", output.String())
	}
}

func TestCoreStopOnlyStopsCoreContainer(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, false)
	label := runtimeLabel("target", target.Name)
	docker := &dockerRuntimeStub{
		containers:  []string{coreContainer, "liapoldus-echo", "liapoldus-server"},
		inspectBody: []byte(fmt.Sprintf(`[{"State":{"Status":"running","Running":true},"Config":{"Labels":{"com.liapoldus.runtime":%q}}}]`, label)),
	}
	var output bytes.Buffer
	runner := Runner{Registry: targetRegistryStub{target: target}, Docker: docker}
	if code := runner.Run([]string{"core", "stop", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if len(docker.stopped) != 1 || docker.stopped[0] != coreContainer || len(docker.restarted) != 0 || len(docker.removed) != 0 || len(docker.networkRemoved) != 0 {
		t.Fatalf("Core stop affected other runtime resources: stopped=%v restarted=%v removed=%v networks=%v", docker.stopped, docker.restarted, docker.removed, docker.networkRemoved)
	}
	if !strings.Contains(output.String(), `"stopped":true`) || !strings.Contains(output.String(), `"container":"liapoldus-core"`) {
		t.Fatalf("unexpected Core stop result: %s", output.String())
	}
}

func TestCoreLifecycleDoesNotMutateStackWithoutOwnedCoreContainer(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, false)
	docker := &dockerRuntimeStub{containers: []string{"liapoldus-echo", "liapoldus-server"}}
	for _, action := range []string{"stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			var output bytes.Buffer
			runner := Runner{Registry: targetRegistryStub{target: target}, Docker: docker}
			if code := runner.Run([]string{"core", action, "--target", "local"}, &output, &output); code == application.ExitOK {
				t.Fatalf("Core %s unexpectedly succeeded without an owned Core container: %s", action, output.String())
			}
			if len(docker.stopped) != 0 || len(docker.restarted) != 0 || len(docker.removed) != 0 || len(docker.networkRemoved) != 0 || docker.runCount != 0 {
				t.Fatalf("Core %s mutated service stack without its container", action)
			}
		})
	}
}

type credentialProviderStub struct {
	value string
}

type targetRegistryStub struct {
	target domain.Target
}

func (s targetRegistryStub) List() ([]domain.Target, error) { return []domain.Target{s.target}, nil }
func (s targetRegistryStub) Find(name string) (domain.Target, error) {
	if name != s.target.Name {
		return domain.Target{}, domain.ErrTargetNotConfigured
	}
	return s.target, nil
}
func (targetRegistryStub) Add(domain.Target) error { return nil }

func (s credentialProviderStub) Resolve(context.Context, string) (string, error) {
	return s.value, nil
}

func writeRunnerFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRunnerGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
