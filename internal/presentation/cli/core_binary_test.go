package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestInspectCoreBinaryRejectsNonELFExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core")
	if err := os.WriteFile(path, []byte("not an ELF executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := inspectCoreBinary(path); err == nil || !strings.Contains(err.Error(), "Linux ELF") {
		t.Fatalf("inspectCoreBinary error = %v, want Linux ELF diagnostic", err)
	}
}

func TestInspectCoreBinaryRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "core-real")
	if err := os.WriteFile(regular, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "core-link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := inspectCoreBinary(link); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("inspectCoreBinary error = %v, want regular-file diagnostic", err)
	}
}

func TestCoreBinaryInspectDoesNotExposePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-core")
	if err := os.WriteFile(path, []byte("not ELF"), 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var stderr bytes.Buffer
	code := (Runner{}).RunContext(context.Background(), []string{"core", "binary", "inspect", "--path", path}, &output, &stderr)
	if code == 0 {
		t.Fatalf("inspect unexpectedly accepted a non-ELF file: %s", output.String())
	}
	if strings.Contains(output.String()+stderr.String(), path) {
		t.Fatalf("CLI diagnostic leaked selected path: stdout=%q stderr=%q", output.String(), stderr.String())
	}
}

func TestCoreBinaryUpdateRequiresExplicitConfirmationBeforeInspect(t *testing.T) {
	var output bytes.Buffer
	var stderr bytes.Buffer
	code := (Runner{}).RunContext(context.Background(), []string{
		"core", "binary", "update", "--target", "local", "--path", filepath.Join(t.TempDir(), "missing"),
		"--digest", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}, &output, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "--confirm") {
		t.Fatalf("missing confirmation was not rejected: code=%d stdout=%q stderr=%q", code, output.String(), stderr.String())
	}
}

func TestCoreBinaryUpdateDoesNotCommitTargetWhenCoreStartFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "candidate")
	writeMinimalARM64ELF(t, path)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	previous := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, RuntimePlatform: "linux/arm64"}
	registry := &updateTargetRegistry{target: previous}
	var output bytes.Buffer
	code := (Runner{Registry: registry}).RunContext(context.Background(), []string{
		"core", "binary", "update", "--target", "local", "--path", path, "--digest", digest, "--confirm",
	}, &output, &output)
	if code == 0 || !strings.Contains(output.String(), "previous target configuration was preserved") {
		t.Fatalf("failed Core start was not reported: code=%d output=%s", code, output.String())
	}
	if registry.addCalls != 0 || registry.target.CoreBinary != previous.CoreBinary || registry.target.CoreBinaryDigest != previous.CoreBinaryDigest {
		t.Fatalf("failed update mutated target registry: adds=%d target=%+v", registry.addCalls, registry.target)
	}
}

func TestCoreBinaryUpdateRemovesCandidateIfRegistryCommitFails(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	writeMinimalARM64ELF(t, candidate)
	body, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	registry := &updateTargetRegistry{target: target, addErr: errors.New("registry unavailable")}
	docker := &dockerRuntimeStub{}
	vault := &memoryCredentialVault{values: map[string]string{target.CredentialRef: "existing-management-token-which-is-long-enough"}}
	runner := Runner{
		Registry: registry, Credentials: vault, CredentialVault: vault, Docker: docker,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil },
	}
	var output bytes.Buffer
	code := runner.Run([]string{"core", "binary", "update", "--target", "local", "--path", candidate, "--digest", digest, "--confirm"}, &output, &output)
	if code == 0 || !strings.Contains(output.String(), "candidate Core container was stopped and removed") {
		t.Fatalf("registry failure did not report candidate cleanup: code=%d output=%s", code, output.String())
	}
	if registry.addCalls != 1 || registry.target.CoreBinary != target.CoreBinary || registry.target.CoreBinaryDigest != target.CoreBinaryDigest {
		t.Fatalf("failed registry commit changed prior target: calls=%d target=%+v", registry.addCalls, registry.target)
	}
	if !reflect.DeepEqual(docker.stopped, []string{coreContainer}) || !reflect.DeepEqual(docker.removed, []string{coreContainer}) {
		t.Fatalf("cleanup must affect Core only: stopped=%v removed=%v", docker.stopped, docker.removed)
	}
}

type updateTargetRegistry struct {
	target   domain.Target
	addCalls int
	addErr   error
}

func (r *updateTargetRegistry) List() ([]domain.Target, error) { return []domain.Target{r.target}, nil }
func (r *updateTargetRegistry) Find(name string) (domain.Target, error) {
	if name != r.target.Name {
		return domain.Target{}, domain.ErrTargetNotConfigured
	}
	return r.target, nil
}
func (r *updateTargetRegistry) Add(target domain.Target) error {
	r.addCalls++
	if r.addErr != nil {
		return r.addErr
	}
	r.target = target
	return nil
}

func writeMinimalARM64ELF(t *testing.T, path string) {
	t.Helper()
	body := make([]byte, 64)
	copy(body[:4], []byte{0x7f, 'E', 'L', 'F'})
	body[4], body[5], body[6] = 2, 1, 1             // ELF64, little-endian, current version
	binary.LittleEndian.PutUint16(body[16:18], 2)   // ET_EXEC
	binary.LittleEndian.PutUint16(body[18:20], 183) // EM_AARCH64
	binary.LittleEndian.PutUint32(body[20:24], 1)
	binary.LittleEndian.PutUint16(body[52:54], 64)
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
}
