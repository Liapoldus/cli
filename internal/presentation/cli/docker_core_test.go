package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestParseDockerCoreStateReadsDockerInspectArray(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		status  string
		running bool
	}{
		{name: "running", payload: `[{"State":{"Status":"running","Running":true}}]`, status: "running", running: true},
		{name: "exited", payload: `[{"State":{"Status":"exited","Running":false}}]`, status: "exited"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := parseDockerCoreState([]byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != test.status || state.Running != test.running {
				t.Fatalf("unexpected state: %#v", state)
			}
		})
	}
}

func TestParseDockerCoreStateRejectsInvalidInspectPayloads(t *testing.T) {
	for _, payload := range []string{
		"not-json",
		`{ "State": { "Status": "running", "Running": true } }`,
		`[]`,
		`[{},{}]`,
		`[{"State":{"Running":true}}]`,
		`[{"State":{"Status":"exited","Running":true}}]`,
	} {
		t.Run(strings.ReplaceAll(payload, " ", ""), func(t *testing.T) {
			if _, err := parseDockerCoreState([]byte(payload)); err == nil || err.Error() != "core container returned invalid status" {
				t.Fatalf("expected safe invalid-status error, got %v", err)
			}
		})
	}
}

func TestCoreContainerUsesSeparateReadOnlySecretMount(t *testing.T) {
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	stateDirectory := filepath.Join(root, "core-state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	certificateFiles := make([]string, 7)
	for index := range certificateFiles {
		certificateFiles[index] = filepath.Join(root, "tls", string(rune('a'+index)))
		if err := os.MkdirAll(filepath.Dir(certificateFiles[index]), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certificateFiles[index], []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := domain.Target{
		Name: "local", Transport: domain.TransportLocalProcess, Endpoint: "https://127.0.0.1:18443",
		RuntimeImage: "liapoldus/core:local", StatePath: filepath.Join(stateDirectory, "core.sqlite"),
		Bootstrap: domain.CoreBootstrap{
			ManagementCertificate: certificateFiles[0], ManagementKey: certificateFiles[1], ManagementClientCA: certificateFiles[2],
			ControlCertificate: certificateFiles[3], ControlKey: certificateFiles[4], ReplicaClientCA: certificateFiles[5],
			ReplicaServerCA: certificateFiles[6], ControlListen: "0.0.0.0:18181", SecretRoot: filepath.Join(stateDirectory, "secrets"),
		},
	}
	request, err := coreContainerRequest(target, "target-local", "liapoldus-runtime-local", "/runtime/core", "")
	if err != nil {
		t.Fatal(err)
	}
	var secretMount *domain.DockerMount
	for index := range request.Mounts {
		if request.Mounts[index].Target == "/var/lib/liapoldus/secrets" {
			secretMount = &request.Mounts[index]
			break
		}
	}
	if secretMount == nil || !secretMount.ReadOnly {
		t.Fatalf("Core secret mount is missing or writable: %#v", request.Mounts)
	}
	if relative, err := filepath.Rel(stateDirectory, secretMount.Source); err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		t.Fatalf("secret mount source is inside Core state: %q error=%v", relative, err)
	}
	if secretMount.Source == filepath.Join(stateDirectory, "secrets") {
		t.Fatalf("Core secret mount still uses the persistent state volume: %#v", secretMount)
	}
}

func TestDescribeCoreMountFailureNamesMountRoleWithoutLeakingPath(t *testing.T) {
	request := domain.DockerContainerRequest{
		StagedBinary: "/Users/operator/Library/Caches/Liapoldus/runtime/core-a1b2",
		Mounts: []domain.DockerMount{
			{Source: "/private/tmp/core-smoke/tls/management.key", Target: "/liapoldus/tls/management.key", ReadOnly: true},
			{Source: "/private/tmp/core-smoke/data", Target: "/var/lib/liapoldus"},
		},
	}
	tests := []struct {
		name, source, wantRole string
	}{
		{"binary", request.StagedBinary, "staged Core binary"},
		{"TLS", request.Mounts[0].Source, "Core TLS asset"},
		{"state", request.Mounts[1].Source, "Core state directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := describeCoreMountFailure(errors.New("docker run failed: bind source path does not exist: "+test.source), request, "local")
			if !strings.Contains(err.Error(), "docker_mount_source_unavailable") || !strings.Contains(err.Error(), test.wantRole) {
				t.Fatalf("expected actionable mount role, got %v", err)
			}
			if strings.Contains(err.Error(), test.source) || strings.Contains(err.Error(), "/Users/") || strings.Contains(err.Error(), "/private/") {
				t.Fatalf("diagnostic leaked a host path: %v", err)
			}
		})
	}
}

func TestDescribeCoreMountFailurePreservesUnrelatedDockerError(t *testing.T) {
	err := errors.New("docker daemon unavailable")
	if got := describeCoreMountFailure(err, domain.DockerContainerRequest{}, "local"); got != err {
		t.Fatalf("unrelated Docker error changed: %v", got)
	}
}

func TestDescribeCoreMountFailureMatchesCanonicalMacOSPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("canonical /private path normalization is specific to macOS Docker hosts")
	}
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	canonicalDockerPath, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.DockerContainerRequest{
		Mounts: []domain.DockerMount{{Source: alias, Target: "/var/lib/liapoldus"}},
	}
	// Docker Desktop/OrbStack may report a canonicalized host path even though
	// the CLI supplied the symlinked spelling (for example /tmp vs /private/tmp).
	err = errors.New("docker run failed: bind source path does not exist: " + canonicalDockerPath)
	got := describeCoreMountFailure(err, request, "local")
	if !strings.Contains(got.Error(), "Core state directory") || strings.Contains(got.Error(), root) {
		t.Fatalf("canonicalized Docker path did not produce a safe mount diagnosis: %v", got)
	}
}
