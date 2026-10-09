package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestRepositoryReadsFilesFromRequestedRevision(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "project.yaml", "id: demo\n")
	writeFile(t, project, "services/orders/settings.json", `{"version":1}`)
	writeFile(t, project, "services/orders/links/payments.json", `{"target":"payments"}`)
	writeFile(t, project, ".env", "TOKEN=secret\n")
	gitCommand(t, project, "init")
	gitCommand(t, project, "config", "user.email", "test@example.test")
	gitCommand(t, project, "config", "user.name", "Test")
	gitCommand(t, project, "add", ".")
	gitCommand(t, project, "commit", "-m", "first")
	first := gitCommand(t, project, "rev-parse", "HEAD")

	writeFile(t, project, "services/orders/settings.json", `{"version":2}`)
	gitCommand(t, project, "add", ".")
	gitCommand(t, project, "commit", "-m", "second")

	repository := Repository{}
	services, err := repository.ServiceSettings(context.Background(), project, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || string(services[0].Settings) != `{"version":1}` {
		t.Fatalf("services from old revision = %+v", services)
	}
	files, err := repository.Files(context.Background(), project, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].Path != "project.yaml" || files[1].Path != "services/orders/links/payments.json" || files[2].Path != "services/orders/settings.json" {
		t.Fatalf("files from old revision = %+v", files)
	}
	links, err := repository.Links(context.Background(), project, first)
	if err != nil || len(links) != 1 || string(links[0]) != `{"target":"payments"}` {
		t.Fatalf("links from old revision = %s err=%v", links, err)
	}
	if _, ok := findFile(files, ".studio/state"); ok {
		t.Fatal("ignored paths must not be included")
	}
}

func findFile(files []domain.File, path string) (domain.File, bool) {
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return domain.File{}, false
}

func writeFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
