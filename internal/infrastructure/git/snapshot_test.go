package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotCapturesDraftWithoutChangingCheckout(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "project.yaml", "id: demo\n")
	writeFile(t, project, "services/echo/settings.json", `{"message":"committed"}`)
	writeFile(t, project, "services/old/settings.json", `{}`)
	gitCommand(t, project, "init")
	gitCommand(t, project, "add", ".")
	before := gitCommand(t, project, "status", "--porcelain")
	writeFile(t, project, "services/echo/settings.json", `{"message":"draft"}`)
	if err := os.Remove(filepath.Join(project, "services/old/settings.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, project, "services/new/settings.json", `{"message":"untracked"}`)
	writeFile(t, project, ".env", "TOKEN=must-not-copy\n")
	writeFile(t, project, ".studio/workspace.json", `{"private":true}`)
	statusBeforeCapture := gitCommand(t, project, "status", "--porcelain")

	snapshot, err := (Snapshotter{}).Capture(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if snapshot.RootPath() == project {
		t.Fatal("snapshot unexpectedly aliases the user's project")
	}
	repository := Repository{}
	services, err := repository.ServiceSettings(context.Background(), snapshot.RootPath(), WorkingTreeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 || services[0].ID != "echo" || string(services[0].Settings) != `{"message":"draft"}` || services[1].ID != "new" {
		t.Fatalf("snapshot does not reflect current draft: %+v", services)
	}
	files, err := repository.Files(context.Background(), snapshot.RootPath(), WorkingTreeRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Path == ".env" || strings.HasPrefix(file.Path, ".studio/") || file.Path == "services/old/settings.json" {
			t.Fatalf("private, workspace, or deleted path entered snapshot: %s", file.Path)
		}
	}
	if _, err := repository.ResolveRevision(context.Background(), snapshot.RootPath(), WorkingTreeRevision); err != nil {
		t.Fatalf("snapshot revision sentinel rejected: %v", err)
	}
	mode, err := os.Stat(filepath.Join(snapshot.RootPath(), "project.yaml"))
	if err != nil || mode.Mode().Perm()&0o222 != 0 {
		t.Fatalf("snapshot file is not sealed read-only: mode=%v err=%v", mode, err)
	}
	if got := gitCommand(t, project, "status", "--porcelain"); got != statusBeforeCapture || got == before {
		t.Fatalf("capture modified original checkout: before=%q immediately-before=%q after=%q", before, statusBeforeCapture, got)
	}
}

func TestSnapshotRejectsSymlinkEscape(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "project.yaml", "id: demo\n")
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "linked.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	gitCommand(t, project, "init")
	gitCommand(t, project, "add", "project.yaml", "linked.json")
	if _, err := (Snapshotter{}).Capture(context.Background(), project); err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("symlink was not rejected: %v", err)
	}
}

func TestSnapshotCancellationCreatesNoSnapshot(t *testing.T) {
	project := t.TempDir()
	writeFile(t, project, "project.yaml", "id: demo\n")
	gitCommand(t, project, "init")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Snapshotter{}).Capture(ctx, project); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capture returned %v", err)
	}
}
