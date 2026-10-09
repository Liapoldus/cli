package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyRequiresCommitConfirmationAndCleanTree(t *testing.T) {
	project := t.TempDir()
	writeProject(t, project)
	if code := runGit(project, "init"); code != 0 {
		t.Fatal("git init failed")
	}
	if code := runGit(project, "config", "user.email", "test@example.test"); code != 0 {
		t.Fatal("git config failed")
	}
	if code := runGit(project, "config", "user.name", "Test"); code != 0 {
		t.Fatal("git config failed")
	}
	if code := runGit(project, "add", "."); code != 0 || runGit(project, "commit", "-m", "initial") != 0 {
		t.Fatal("git commit failed")
	}
	var out bytes.Buffer
	if code := Run([]string{"apply", "--project", project, "--revision", "HEAD", "--target", "local"}, &out, &out); code != ExitArguments || !strings.Contains(out.String(), "--confirm") {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	if err := os.WriteFile(filepath.Join(project, "services.txt"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Run([]string{"apply", "--project", project, "--revision", "HEAD", "--target", "local", "--confirm"}, &out, &out); code != ExitRevision || !strings.Contains(out.String(), "clean working tree") {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
}

func TestApplySendsImmutableBundleToCoreAPI(t *testing.T) {
	project := t.TempDir()
	writeProject(t, project)
	if runGit(project, "init") != 0 || runGit(project, "config", "user.email", "test@example.test") != 0 || runGit(project, "config", "user.name", "Test") != 0 || runGit(project, "add", ".") != 0 || runGit(project, "commit", "-m", "initial") != 0 {
		t.Fatal("git setup failed")
	}
	var gotAuth, gotRevision, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		gotKey = request.Header.Get("Idempotency-Key")
		var payload struct {
			Project struct {
				Revision string `json:"revision"`
			} `json:"project"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotRevision = payload.Project.Revision
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"operations":["op-1"],"requestId":"req-1"}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	code := Run([]string{"apply", "--project", project, "--revision", "HEAD", "--target", "local", "--endpoint", server.URL, "--token", "secret", "--confirm"}, &out, &out)
	if code != ExitOK {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	if gotAuth != "Bearer secret" || gotRevision == "" || gotKey == "" {
		t.Fatalf("headers/revision not sent: auth=%q revision=%q key=%q", gotAuth, gotRevision, gotKey)
	}
}

func writeProject(t *testing.T, project string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, "project.yaml"), []byte("id: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
func runGit(project string, args ...string) int {
	command := exec.Command("git", args...)
	command.Dir = project
	if err := command.Run(); err != nil {
		return 1
	}
	return 0
}
