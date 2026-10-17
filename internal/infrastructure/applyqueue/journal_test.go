package applyqueue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestJournalPersistsOnlyOpaqueRecoveryReferencesAtomically(t *testing.T) {
	journal := Journal{Directory: t.TempDir()}
	key := TargetKey("local", "https://127.0.0.1:18180")
	intent := domain.ApplyIntent{Scope: "config-bundle/working-tree:abc", Keys: []string{"dev-apply-digest:server", "dev-apply-digest:echo"}}
	if err := journal.Save(key, intent); err != nil {
		t.Fatal(err)
	}
	updated := domain.ApplyIntent{Scope: "config-bundle/working-tree:def", Keys: []string{"dev-apply-digest:server-next"}}
	if err := journal.Save(key, updated); err != nil {
		t.Fatalf("replace existing journal: %v", err)
	}
	intent = updated
	got, found, err := journal.Load(key)
	if err != nil || !found || got.Scope != intent.Scope || strings.Join(got.Keys, ",") != strings.Join(intent.Keys, ",") {
		t.Fatalf("intent=%+v found=%v err=%v", got, found, err)
	}
	path := journal.path(key) + ".json"
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode=%v err=%v", info.Mode().Perm(), err)
	}
	directoryInfo, err := os.Stat(journal.Directory)
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("journal directory mode=%v err=%v", directoryInfo.Mode().Perm(), err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"settings", "token", "password", "privateKey"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("journal contains forbidden payload marker %q", forbidden)
		}
	}
	if err := journal.Clear(key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := journal.Load(key); err != nil || found {
		t.Fatalf("after clear found=%v err=%v", found, err)
	}
}

func TestAcquireHonorsAlreadyCancelledContext(t *testing.T) {
	journal := Journal{Directory: filepath.Join(t.TempDir(), "queue")}
	key := TargetKey("local", "http://core")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journal.Acquire(ctx, key); err == nil {
		t.Fatal("acquire succeeded with an already cancelled context")
	}
}

func TestAcquireSerializesTargetAndHonorsCancellation(t *testing.T) {
	journal := Journal{Directory: filepath.Join(t.TempDir(), "queue")}
	key := TargetKey("local", "http://core")
	release, err := journal.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := journal.Acquire(ctx, key); err == nil || !strings.Contains(err.Error(), "waiting for another apply") {
		t.Fatalf("second acquire error=%v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = journal.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsInvalidTargetAndMalformedJournal(t *testing.T) {
	journal := Journal{Directory: t.TempDir()}
	if _, _, err := journal.Load("../escape"); err == nil {
		t.Fatal("path-like target key accepted")
	}
	key := TargetKey("local", "http://core")
	if err := os.WriteFile(journal.path(key)+".json", []byte(`{"scope":"scope","keys":[],"settings":{"token":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := journal.Load(key); err == nil || found {
		t.Fatalf("malformed journal found=%v err=%v", found, err)
	}
}
