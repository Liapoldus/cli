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
	runner := Runner{Source: git.Repository{}, CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
		return coreapi.New(connection)
	}}
	code := runner.Run([]string{
		"apply", "--project", project, "--revision", first, "--target", "production",
		"--expected-generation", "41",
		"--endpoint", server.URL, "--token", "secret", "--confirm",
	}, &output, &output)
	if code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if request.Project.Revision != first || request.Target.ExpectedGeneration == nil || *request.Target.ExpectedGeneration != 41 || len(request.Bundle.Services) != 1 || string(request.Bundle.Services[0].Settings) != `{"version":1}` {
		t.Fatalf("request does not match requested revision: %+v", request)
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
	runner := Runner{Source: git.Repository{}, CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
		return coreapi.New(connection)
	}}
	code := runner.Run([]string{
		"apply", "--project", project, "--revision", "HEAD", "--target", "production",
		"--endpoint", "http://127.0.0.1:1", "--token", "secret",
	}, &output, &output)
	if code != application.ExitArguments || !strings.Contains(output.String(), "--confirm") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	writeRunnerFile(t, project, "draft.txt", "draft")
	output.Reset()
	code = runner.Run([]string{
		"apply", "--project", project, "--revision", "HEAD", "--target", "production",
		"--endpoint", "http://127.0.0.1:1", "--token", "secret", "--confirm",
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
		Source:   git.Repository{},
		Registry: targets.Registry{Path: registryPath},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) {
			return coreapi.New(connection)
		},
	}
	args := []string{"apply", "--project", project, "--revision", revision, "--target", "production", "--token", "secret", "--confirm"}
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
	if err := os.WriteFile(registryPath, []byte(`{"targets":[{"name":"production","transport":"remote-api","endpoint":"http://core.example","tokenEnv":"CORE_TOKEN"}]}`), 0o600); err != nil {
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

type credentialProviderStub struct {
	value string
}

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
