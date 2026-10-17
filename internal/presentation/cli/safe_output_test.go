package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/artifacts"
	"github.com/Liapoldus/cli/internal/infrastructure/git"
)

func TestTargetCommandsReturnRendererSafeSummary(t *testing.T) {
	target := domain.Target{
		Name: "local", Environment: "development", Transport: domain.TransportLocalProcess,
		Endpoint: "https://core.example.test", CredentialRef: "marker-credential-reference",
		CAFile: "/private/tls/ca.pem", ClientCertFile: "/private/tls/cli.crt",
		ClientKeyFile: "/private/tls/cli.key", CoreBinary: "/private/bin/core",
		RuntimeImage: "registry.example.test/private/runtime:1", StatePath: "/private/state/core.sqlite",
		Bootstrap: domain.CoreBootstrap{ManagementKey: "/private/tls/management.key", SecretRoot: "/private/secrets"},
		Runtime:   domain.RuntimeConfig{TLSDirectory: "/private/tls", CoreURL: "https://core:18181"},
	}
	runner := Runner{Registry: targetRegistryStub{target: target}}
	for _, args := range [][]string{{"target", "list", studioEventsFlag}, {"target", "inspect", "local", studioEventsFlag}} {
		var stdout, stderr bytes.Buffer
		if code := runner.Run(args, &stdout, &stderr); code != application.ExitOK {
			t.Fatalf("%v failed: %s", args, stderr.String())
		}
		body := stdout.String()
		for _, forbidden := range []string{
			"credentialRef", "marker-credential-reference", "/private/", "management.key",
			"secretRoot", "clientKeyFile", "TLSDirectory", "core.sqlite",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%v exposed %q: %s", args, forbidden, body)
			}
		}
		if !strings.Contains(body, `"name":"local"`) || !strings.Contains(body, `"transport":"local-process"`) {
			t.Errorf("%v omitted safe target metadata: %s", args, body)
		}
	}
}

func TestDevelopmentApplySummaryPreservesPartialReplicaOutcome(t *testing.T) {
	result := summarizeDevelopmentApply(domain.Bundle{Revision: "working-tree:abc", Digest: "sha256:abc"}, application.DevelopmentApplyResult{
		CoreResponse: json.RawMessage(`{"operations":["op-1"],"requestId":"req-1"}`),
		ReplicaOutcomes: []domain.OperationReplicaOutcome{{
			ReplicaID: "server-1", DesiredGeneration: 12, State: "failed", FailureCode: "replica_unavailable",
		}},
	})
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Accepted        bool                             `json:"accepted"`
		ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Accepted || len(decoded.ReplicaOutcomes) != 1 || decoded.ReplicaOutcomes[0].State != "failed" || decoded.ReplicaOutcomes[0].DesiredGeneration != 12 {
		t.Fatalf("partial Core result was not preserved: %s", body)
	}
}

func TestTargetTestDoesNotReturnConfiguredEndpoint(t *testing.T) {
	const endpoint = "https://user:marker-password@core.example.test/api?token=marker-token"
	runner := Runner{
		Registry: targetRegistryStub{target: domain.Target{
			Name: "production", Transport: domain.TransportRemoteAPI, Endpoint: endpoint,
		}},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil },
	}
	var stdout, stderr bytes.Buffer
	if code := runner.Run([]string{"target", "test", "production", studioEventsFlag}, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("target test failed: %s", stderr.String())
	}
	for _, forbidden := range []string{endpoint, "marker-password", "marker-token", `"endpoint"`} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("target test exposed %q: %s", forbidden, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), `"reachable":true`) {
		t.Fatalf("target test omitted safe health status: %s", stdout.String())
	}
}

func TestServiceArtifactCommandsNeverReturnLocalBinaryPath(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "private", "echo-binary")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("prebuilt"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := artifacts.Digest(binary)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Artifacts: artifacts.Registry{Path: filepath.Join(root, "artifacts.json")}}
	commands := [][]string{
		{"service", "artifact", "register", "--id", "echo", "--version", "1.0.0", "--platform", "linux/arm64", "--path", binary, studioEventsFlag},
		{"service", "artifact", "inspect", "--id", "echo", "--version", "1.0.0", "--platform", "linux/arm64", studioEventsFlag},
		{"service", "artifact", "list", studioEventsFlag},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		if code := runner.Run(args, &stdout, &stderr); code != application.ExitOK {
			t.Fatalf("%v failed: %s", args, stderr.String())
		}
		if strings.Contains(stdout.String(), binary) || strings.Contains(stdout.String(), filepath.Dir(binary)) {
			t.Errorf("%v exposed local artifact path: %s", args, stdout.String())
		}
		if !strings.Contains(stdout.String(), digest) {
			t.Errorf("%v omitted artifact digest: %s", args, stdout.String())
		}
	}
}

func TestRuntimePlanDoesNotReturnProjectOrArtifactPaths(t *testing.T) {
	project := t.TempDir()
	writeRunnerFile(t, project, "project.yaml", "id: safe-output-test\n")
	binary := filepath.Join(t.TempDir(), "private-artifact", "server")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("server-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := artifacts.Digest(binary)
	if err != nil {
		t.Fatal(err)
	}
	writeRunnerFile(t, project, "services/server/service.yaml", "id: server\nversion: 1.0.0\nartifact:\n  id: server\n  version: 1.0.0\n  platform: linux/arm64\n  digest: "+digest+"\nruntime:\n  command: server\n  controlEndpoint: https://server:9443\n  peerEndpoint: https://server:9643\n")
	runRunnerGit(t, project, "init")
	runRunnerGit(t, project, "add", ".")
	runRunnerGit(t, project, "-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-m", "initial")
	registry := artifacts.Registry{Path: filepath.Join(t.TempDir(), "artifacts.json")}
	if err := registry.Register(domain.Artifact{ID: "server", Version: "1.0.0", Platform: "linux/arm64", Digest: digest, Path: binary, Trusted: true}); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Source: git.Repository{}, Artifacts: registry}
	var stdout, stderr bytes.Buffer
	if code := runner.Run([]string{"runtime", "plan", "--project", project, "--revision", "HEAD", "--target", "local", studioEventsFlag}, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("runtime plan failed: %s", stderr.String())
	}
	for _, forbidden := range []string{project, binary, "\"path\""} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("runtime plan exposed %q: %s", forbidden, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), digest) || !strings.Contains(stdout.String(), `"id":"server"`) {
		t.Fatalf("runtime plan omitted safe artifact identity: %s", stdout.String())
	}
}

func TestDockerInspectSummaryOmitsEnvironmentAndMountHostPaths(t *testing.T) {
	const input = `[{"Name":"/liapoldus-server","Config":{"Image":"runtime:1","Env":["TOKEN=marker-secret"]},"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}},"NetworkSettings":{"Ports":{"9080/tcp":[{"HostIp":"127.0.0.1","HostPort":"19080"}]}},"Mounts":[{"Source":"/private/staging/server","Destination":"/service/server"}]}]`
	output, err := safeDockerInspectSummary([]byte(input), "liapoldus-server")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"TOKEN", "marker-secret", "/private/staging", "Mounts", "Source"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("safe inspect exposed %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(string(body), "healthy") || !strings.Contains(string(body), "19080") {
		t.Fatalf("safe inspect omitted runtime status/ports: %s", body)
	}
}

func TestRuntimeInspectReturnsOnlyAllowlistedDockerFields(t *testing.T) {
	docker := inspectDockerStub{}
	runner := Runner{
		Registry:  targetRegistryStub{target: domain.Target{Name: "local", Transport: domain.TransportLocalProcess}},
		Artifacts: emptyArtifactRegistry{}, Docker: docker,
	}
	var stdout, stderr bytes.Buffer
	if code := runner.Run([]string{"runtime", "inspect", "--target", "local", "--service", "server", studioEventsFlag}, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("runtime inspect failed: %s", stderr.String())
	}
	for _, forbidden := range []string{"TOKEN", "marker-secret", "/private/staging", "Mounts", "Source"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("runtime inspect forwarded %q to Studio: %s", forbidden, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "healthy") || !strings.Contains(stdout.String(), "19080") {
		t.Fatalf("runtime inspect omitted safe diagnostics: %s", stdout.String())
	}
}

type inspectDockerStub struct{}

func (inspectDockerStub) Probe(context.Context) domain.DockerProviderStatus {
	return domain.DockerProviderStatus{ID: "docker", Available: true}
}
func (inspectDockerStub) NetworkCreate(context.Context, string, string) error { return nil }
func (inspectDockerStub) NetworkRemove(context.Context, string) error         { return nil }
func (inspectDockerStub) RunContainer(context.Context, domain.DockerContainerRequest) error {
	return nil
}
func (inspectDockerStub) ListContainers(context.Context, string) ([]string, error) { return nil, nil }
func (inspectDockerStub) StopContainer(context.Context, string) error              { return nil }
func (inspectDockerStub) StartContainer(context.Context, string) error             { return nil }
func (inspectDockerStub) RestartContainer(context.Context, string) error           { return nil }
func (inspectDockerStub) RemoveContainer(context.Context, string) error            { return nil }
func (inspectDockerStub) Logs(context.Context, string) ([]byte, error)             { return nil, nil }
func (inspectDockerStub) Inspect(context.Context, string) ([]byte, error) {
	return []byte(`[{"Name":"/liapoldus-server","Config":{"Image":"runtime:1","Env":["TOKEN=marker-secret"]},"State":{"Status":"running","Health":{"Status":"healthy"}},"NetworkSettings":{"Ports":{"9080/tcp":[{"HostIp":"127.0.0.1","HostPort":"19080"}]}},"Mounts":[{"Source":"/private/staging/server","Destination":"/service/server"}]}]`), nil
}
func (inspectDockerStub) Exec(context.Context, string, string, []string) ([]byte, error) {
	return nil, nil
}

type emptyArtifactRegistry struct{}

func (emptyArtifactRegistry) List() ([]domain.Artifact, error) { return nil, nil }
func (emptyArtifactRegistry) Find(string, string, string) (domain.Artifact, error) {
	return domain.Artifact{}, domain.ErrArtifactNotFound
}
func (emptyArtifactRegistry) Register(domain.Artifact) error             { return nil }
func (emptyArtifactRegistry) Trust(string, string, string, string) error { return nil }
func (emptyArtifactRegistry) Remove(string, string, string) error        { return nil }
