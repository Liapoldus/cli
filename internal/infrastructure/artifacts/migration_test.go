package artifacts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestNomenclatureMigrationPreservesPersistedRegistry(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "artifacts.json")}
	// Existing registrations are neutral records. Even historical paths and
	// optional launch arguments are operator data, not nomenclature to rewrite.
	want := domain.Artifact{ID: "echo", Version: "1.0.0", Platform: "linux/arm64", Digest: "sha256:" + strings.Repeat("a", 64), Path: "/operator/plugins/echo", Trusted: true, Command: "plugin", ControlEndpoint: "https://echo:9443", PeerEndpoint: "https://echo:9444"}
	untrusted := domain.Artifact{ID: "server", Version: "2.0.0", Platform: "linux/amd64", Digest: "sha256:" + strings.Repeat("b", 64), Path: "/operator/plugins/server", Trusted: false}
	body, err := json.MarshalIndent(file{Artifacts: []domain.Artifact{untrusted, want}}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := registry.Find(want.ID, want.Version, want.Platform)
		if err != nil || got != want {
			t.Fatalf("registration changed: %+v, %v", got, err)
		}
	}
	after, err := os.ReadFile(registry.Path)
	if err != nil || !bytes.Equal(body, after) {
		t.Fatalf("registry read rewrote persisted bytes: %v", err)
	}
	// A later trust write must retain every registered field.
	if err := registry.Trust(want.ID, want.Version, want.Platform, want.Digest); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Find(want.ID, want.Version, want.Platform)
	if err != nil || got != want {
		t.Fatalf("trust write changed registration: %+v, %v", got, err)
	}
	other, err := registry.Find(untrusted.ID, untrusted.Version, untrusted.Platform)
	if err != nil || other != untrusted {
		t.Fatalf("migration lost or trusted another registration: %+v, %v", other, err)
	}
}
