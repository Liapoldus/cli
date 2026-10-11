package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestRunContainerDoesNotBuildAndUsesReadOnlyStaging(t *testing.T) {
	var got []string
	provider := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte("ok"), nil
	}}
	err := provider.RunContainer(context.Background(), domain.DockerContainerRequest{Name: "runtime-server", Image: "liapoldus/runtime:1", Network: "runtime-net", StagedBinary: "/tmp/server", Command: "server", Label: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "build") || !strings.Contains(joined, "readonly") || !strings.Contains(joined, "--entrypoint") {
		t.Fatalf("unsafe docker argv: %v", got)
	}
}

func TestRunContainerSupportsDockerOnlyCoreAndLoopbackPublishing(t *testing.T) {
	var got []string
	provider := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return nil, nil
	}}
	request := domain.DockerContainerRequest{
		Name: "liapoldus-core", Image: "alpine:latest", Network: "runtime-net",
		NetworkAliases: []string{"liapoldus-core"}, Platform: "linux/arm64",
		StagedBinary: "/tmp/core", StagedBinaryTarget: "/liapoldus/core", Entrypoint: "/liapoldus/core",
		Environment:    map[string]string{"CORE_INIT_MANAGEMENT_TOKEN_FILE": "/run/secrets/management-token"},
		PublishedPorts: []domain.DockerPort{{HostIP: "127.0.0.1", Host: 18180, Container: 18180}},
		Label:          "studio-local",
	}
	if err := provider.RunContainer(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, expected := range []string{"--platform linux/arm64", "--network-alias liapoldus-core", "127.0.0.1:18180:18180", "dst=/liapoldus/core,readonly", "--entrypoint /liapoldus/core"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in Docker argv: %v", expected, got)
		}
	}
	if strings.Contains(joined, "CORE_INIT_MANAGEMENT_TOKEN=") {
		t.Fatalf("token value must never be passed as Docker environment: %v", got)
	}
}

func TestRunContainerUsesValidDockerMountSyntaxForWritableState(t *testing.T) {
	var got []string
	provider := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return nil, nil
	}}
	request := domain.DockerContainerRequest{
		Name: "liapoldus-core", Image: "alpine:latest", Network: "runtime-net",
		StagedBinary: "/tmp/core", Label: "studio-local",
		Mounts: []domain.DockerMount{{Source: "/tmp/liapoldus-state", Target: "/var/lib/liapoldus"}},
	}
	if err := provider.RunContainer(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < len(got)-1; i++ {
		if got[i] == "--mount" && strings.Contains(got[i+1], "src=/tmp/liapoldus-state") {
			if got[i+1] != "type=bind,src=/tmp/liapoldus-state,dst=/var/lib/liapoldus" {
				t.Fatalf("writable Docker mount must omit invalid rw field; got %q", got[i+1])
			}
			return
		}
	}
	t.Fatalf("expected writable state mount in Docker argv: %v", got)
}

func TestRunContainerRejectsNonLoopbackPublishAddress(t *testing.T) {
	provider := Provider{Execute: func(context.Context, []string) ([]byte, error) { t.Fatal("Docker must not run"); return nil, nil }}
	err := provider.RunContainer(context.Background(), domain.DockerContainerRequest{
		Name: "liapoldus-core", Image: "alpine:latest", Network: "runtime-net", StagedBinary: "/tmp/core", Label: "studio-local",
		PublishedPorts: []domain.DockerPort{{HostIP: "0.0.0.0", Host: 18180, Container: 18180}},
	})
	if err == nil {
		t.Fatal("expected non-loopback host publish to be rejected")
	}
}

func TestExecRejectsPathInsteadOfBinaryName(t *testing.T) {
	provider := Provider{Execute: func(_ context.Context, args []string) ([]byte, error) { t.Fatal(args); return nil, nil }}
	if _, err := provider.Exec(context.Background(), "container", "/bin/sh", nil); err == nil {
		t.Fatal("expected allowlist rejection")
	}
}
