package git

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestServiceSourcePreservesExactSettingsAndDigest(t *testing.T) {
	project := t.TempDir()
	settings := "{\n  \"plugin\": \"application-owned\", \"method\": \"echo.invoke\",\n  \"number\": 1.00, \"text\": \"<>&\"\n}\n"
	writeFile(t, project, "project.yaml", "id: demo\n")
	writeFile(t, project, "services/echo/settings.json", settings)
	gitCommand(t, project, "init")
	repository := Repository{}
	services, err := repository.ServiceSettings(context.Background(), project, WorkingTreeRevision)
	if err != nil || len(services) != 1 || string(services[0].Settings) != settings {
		t.Fatalf("raw settings changed: %+v, %v", services, err)
	}
	files, err := repository.Files(context.Background(), project, WorkingTreeRevision)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := findFile(files, "services/echo/settings.json")
	if !ok || file.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(settings))) || file.Bytes != len(settings) {
		t.Fatalf("raw file digest changed: %+v", file)
	}
}

func TestProjectRejectsRetiredRuntimePaths(t *testing.T) {
	for _, path := range []string{"plugins/echo/plugin.yaml", "plugins/echo/settings.json", "services/echo/plugin.yaml"} {
		t.Run(path, func(t *testing.T) {
			project := t.TempDir()
			writeFile(t, project, "project.yaml", "id: demo\n")
			writeFile(t, project, path, "{}\n")
			gitCommand(t, project, "init")
			for _, read := range []func(context.Context, string, string) error{
				func(ctx context.Context, p, r string) error { _, err := (Repository{}).Files(ctx, p, r); return err },
				func(ctx context.Context, p, r string) error {
					_, err := (Repository{}).RuntimeArtifacts(ctx, p, r)
					return err
				},
				func(ctx context.Context, p, r string) error {
					_, err := (Repository{}).ServiceSettings(ctx, p, r)
					return err
				},
				func(ctx context.Context, p, r string) error { _, err := (Repository{}).Links(ctx, p, r); return err },
			} {
				if err := read(context.Background(), project, WorkingTreeRevision); err == nil || !strings.Contains(err.Error(), "retired runtime path") {
					t.Fatalf("retired path silently accepted: %v", err)
				}
			}
		})
	}
}

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

func TestRuntimeArtifactsReadExactServiceReferenceAtRevision(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "project.yaml", "id: demo\n")
	writeFile(t, project, "services/server/service.yaml", validServiceManifest("server", "1.0.0", "sha256:"+strings.Repeat("a", 64)))
	gitCommand(t, project, "init")
	gitCommand(t, project, "config", "user.email", "test@example.test")
	gitCommand(t, project, "config", "user.name", "Test")
	gitCommand(t, project, "add", ".")
	gitCommand(t, project, "commit", "-m", "first")
	first := gitCommand(t, project, "rev-parse", "HEAD")
	writeFile(t, project, "services/server/service.yaml", validServiceManifest("server", "2.0.0", "sha256:"+strings.Repeat("b", 64)))
	gitCommand(t, project, "add", ".")
	gitCommand(t, project, "commit", "-m", "second")

	artifacts, err := (Repository{}).RuntimeArtifacts(context.Background(), project, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Version != "1.0.0" || artifacts[0].Digest != "sha256:"+strings.Repeat("a", 64) || artifacts[0].Command != "server" || artifacts[0].PeerEndpoint != "https://server:9643" {
		t.Fatalf("runtime artifacts from pinned revision = %+v", artifacts)
	}
}

func TestRuntimeArtifactsRejectLegacyAndUnsafeManifests(t *testing.T) {
	for _, test := range []struct {
		name     string
		manifest string
	}{
		{name: "legacy", manifest: "id: server\nname: Server\nversion: 1.0.0\nmanifestVersion: v1\nsettingsSchema: ''\n"},
		{name: "external-control-endpoint", manifest: strings.Replace(validServiceManifest("server", "1.0.0", "sha256:"+strings.Repeat("a", 64)), "https://server:9443", "https://attacker.invalid:9443", 1)},
		{name: "multiple-documents", manifest: validServiceManifest("server", "1.0.0", "sha256:"+strings.Repeat("a", 64)) + "---\nid: second\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			writeFile(t, project, "project.yaml", "id: demo\n")
			writeFile(t, project, "services/server/service.yaml", test.manifest)
			gitCommand(t, project, "init")
			gitCommand(t, project, "add", ".")
			if _, err := (Repository{}).RuntimeArtifacts(context.Background(), project, WorkingTreeRevision); err == nil {
				t.Fatal("unsafe or legacy runtime manifest was accepted")
			}
		})
	}
}

func validServiceManifest(id, version, digest string) string {
	return "id: " + id + "\nversion: " + version + "\nartifact:\n  id: " + id + "\n  version: " + version + "\n  platform: linux/arm64\n  digest: " + digest + "\nruntime:\n  command: " + id + "\n  controlEndpoint: https://" + id + ":9443\n  peerEndpoint: https://" + id + ":9643\n"
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
