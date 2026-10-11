package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestRegistryRegistersDigestAndTrustsArtifact(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "server")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(binary)
	if err != nil {
		t.Fatal(err)
	}
	registry := Registry{Path: filepath.Join(root, "artifacts.json")}
	artifact := domain.Artifact{ID: "server", Version: "1.0.0", Platform: "linux/arm64", Digest: digest, Path: binary}
	if err := registry.Register(artifact); err != nil {
		t.Fatal(err)
	}
	if err := registry.Trust("server", "1.0.0", "linux/arm64", digest); err != nil {
		t.Fatal(err)
	}
	found, err := registry.Find("server", "1.0.0", "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	if !found.Trusted || found.Digest != digest {
		t.Fatalf("unexpected artifact: %+v", found)
	}
}

func TestRegistryRejectsDigestMismatchOnTrust(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "echo")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, _ := Digest(binary)
	registry := Registry{Path: filepath.Join(root, "artifacts.json")}
	if err := registry.Register(domain.Artifact{ID: "echo", Version: "1.0.0", Platform: "linux/amd64", Digest: digest, Path: binary}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Trust("echo", "1.0.0", "linux/amd64", "sha256:"+string(make([]byte, 64))); err != domain.ErrArtifactDigestMismatch {
		t.Fatalf("error=%v", err)
	}
}
