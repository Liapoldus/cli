package localcore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestStateFilesBackupAndRestoreRequireStoppedDockerTarget(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "core.sqlite")
	if err := os.WriteFile(state, []byte("core-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, StatePath: state}
	backup := filepath.Join(root, "backup.sqlite")
	files := StateFiles{}
	if err := files.Backup(target, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("new-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.Restore(target, backup); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(state)
	if err != nil || string(body) != "core-state" {
		t.Fatalf("restored state=%q err=%v", body, err)
	}
}

func TestStateFilesRejectsRemoteTarget(t *testing.T) {
	files := StateFiles{}
	target := domain.Target{Name: "production", Transport: domain.TransportRemoteAPI, StatePath: filepath.Join(t.TempDir(), "core.sqlite")}
	if err := files.Backup(target, filepath.Join(t.TempDir(), "backup.sqlite")); err == nil {
		t.Fatal("legacy host-process target was accepted")
	}
}
