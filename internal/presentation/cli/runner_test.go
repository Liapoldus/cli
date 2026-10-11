package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
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
	if strings.Contains(message, "top-secret") || len(message) > 512+len("…") {
		t.Fatalf("unsafe diagnostic: %q", message)
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

func TestCoreRestartUsesDockerLifecycle(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, false)
	registry := targetRegistryStub{target: target}
	docker := &dockerRuntimeStub{}
	vault := &memoryCredentialVault{values: make(map[string]string)}
	var output bytes.Buffer
	runner := Runner{Registry: registry, Docker: docker, Credentials: vault, CredentialVault: vault,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil }}
	if code := runner.Run([]string{"core", "restart", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if docker.runCount != 1 || !strings.Contains(output.String(), `"restarted":true`) {
		t.Fatalf("Docker runs=%d output=%s", docker.runCount, output.String())
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
