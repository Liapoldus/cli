package docker

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestProbeReportsMissingExecutableSeparatelyFromUnavailableDaemon(t *testing.T) {
	missing := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"--version"}) {
			t.Fatalf("missing executable probe args=%v", args)
		}
		return nil, errors.New("exec: executable file not found")
	}}
	status := missing.Probe(context.Background())
	if status.Available || status.ErrorCode != "provider_unavailable" {
		t.Fatalf("arbitrary execution error must not claim executable is missing: %+v", status)
	}

	missingExecutable := Provider{BinaryPath: "docker", Execute: func(_ context.Context, _ []string) ([]byte, error) {
		return nil, &exec.Error{Name: "docker", Err: exec.ErrNotFound}
	}}
	status = missingExecutable.Probe(context.Background())
	if status.Available || status.ErrorCode != "provider_missing" {
		t.Fatalf("missing executable status=%+v", status)
	}
}

func TestProbeReportsDaemonUnavailableWhenDockerClientExists(t *testing.T) {
	var calls [][]string
	provider := Provider{BinaryPath: "/opt/homebrew/bin/docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) == 1 && args[0] == "--version" {
			return []byte("Docker version 27.3.1, build test"), nil
		}
		return nil, errors.New("Cannot connect to the Docker daemon")
	}}
	status := provider.Probe(context.Background())
	if status.Available || status.ErrorCode != "provider_unavailable" {
		t.Fatalf("unavailable daemon status=%+v", status)
	}
	want := [][]string{{"--version"}, {"version", "--format", "{{.Server.Version}}"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("probe calls=%v want=%v", calls, want)
	}
}

func TestProbeReturnsDockerServerVersionWhenAvailable(t *testing.T) {
	provider := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
		if len(args) == 1 && args[0] == "--version" {
			return []byte("Docker version 27.3.1, build test"), nil
		}
		return []byte("27.3.1\n"), nil
	}}
	status := provider.Probe(context.Background())
	if !status.Available || status.ErrorCode != "" || status.Version != "27.3.1" {
		t.Fatalf("available Docker status=%+v", status)
	}
}

func TestEnsureAvailableStartsLocalEngineAndWaitsForDaemon(t *testing.T) {
	engineStarted := false
	provider := Provider{
		BinaryPath:  "docker",
		StartEngine: func(context.Context) error { engineStarted = true; return nil },
		Execute: func(_ context.Context, args []string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("Docker client"), nil
			}
			if engineStarted {
				return []byte("27.3.1"), nil
			}
			return nil, errors.New("daemon is stopped")
		},
	}
	status, err := provider.EnsureAvailable(context.Background())
	if err != nil || !engineStarted || !status.Available || status.Version != "27.3.1" {
		t.Fatalf("status=%+v started=%t err=%v", status, engineStarted, err)
	}
}

func TestEnsureAvailableDoesNotStartMissingDockerClient(t *testing.T) {
	started := false
	provider := Provider{
		BinaryPath:  "docker",
		StartEngine: func(context.Context) error { started = true; return nil },
		Execute: func(context.Context, []string) ([]byte, error) {
			return nil, &exec.Error{Name: "docker", Err: exec.ErrNotFound}
		},
	}
	status, err := provider.EnsureAvailable(context.Background())
	if err != nil || started || status.Available || status.ErrorCode != "provider_missing" {
		t.Fatalf("status=%+v started=%t err=%v", status, started, err)
	}
}

func TestDarwinDockerApplicationFollowsSelectedContext(t *testing.T) {
	tests := []struct {
		name     string
		context  string
		endpoint string
		want     string
		wantErr  bool
	}{
		{name: "OrbStack context", context: "orbstack", endpoint: "unix:///Users/test/.orbstack/run/docker.sock", want: "OrbStack"},
		{name: "OrbStack endpoint", context: "default", endpoint: "unix:///Users/test/.orbstack/run/docker.sock", want: "OrbStack"},
		{name: "Docker Desktop context", context: "desktop-linux", endpoint: "unix:///Users/test/.docker/run/docker.sock", want: "Docker"},
		{name: "Docker Desktop endpoint", context: "default", endpoint: "unix:///Users/test/.docker/run/docker.sock", want: "Docker"},
		{name: "local Docker socket", context: "default", endpoint: "unix:///var/run/docker.sock", want: "Docker"},
		{name: "unknown context", context: "remote", endpoint: "tcp://docker.example.test:2376", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := darwinDockerApplication(test.context, test.endpoint)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("application=%q want=%q", got, test.want)
			}
		})
	}
}

func TestDockerEnvironmentHonorsContextOverHostOverride(t *testing.T) {
	tests := []struct {
		name          string
		contextName   string
		contextHost   string
		dockerHost    string
		dockerContext string
		want          string
	}{
		{name: "DOCKER_HOST overrides default context", contextName: "default", contextHost: "unix:///var/run/docker.sock", dockerHost: "unix:///Users/test/.orbstack/run/docker.sock", want: "OrbStack"},
		{name: "DOCKER_CONTEXT overrides DOCKER_HOST", contextName: "orbstack", contextHost: "unix:///Users/test/.orbstack/run/docker.sock", dockerHost: "unix:///Users/test/.docker/run/docker.sock", dockerContext: "orbstack", want: "OrbStack"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectDarwinDockerApplication(test.contextName, test.contextHost, test.dockerHost, test.dockerContext)
			if err != nil || got != test.want {
				t.Fatalf("application=%q error=%v want=%q", got, err, test.want)
			}
		})
	}
}

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
		CoreBinaryDigest: strings.Repeat("a", 64),
		NetworkAliases:   []string{"liapoldus-core"}, Platform: "linux/arm64",
		StagedBinary: "/tmp/core", StagedBinaryTarget: "/liapoldus/core", Entrypoint: "/liapoldus/core",
		Environment:    map[string]string{"CORE_INIT_MANAGEMENT_TOKEN_FILE": "/run/secrets/management-token"},
		PublishedPorts: []domain.DockerPort{{HostIP: "127.0.0.1", Host: 18180, Container: 18180}},
		Label:          "studio-local",
	}
	if err := provider.RunContainer(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, expected := range []string{"--platform linux/arm64", "--network-alias liapoldus-core", "127.0.0.1:18180:18180", "dst=/liapoldus/core,readonly", "--entrypoint /liapoldus/core", "com.liapoldus.core.binary.sha256=" + strings.Repeat("a", 64)} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected %q in Docker argv: %v", expected, got)
		}
	}
	if strings.Contains(joined, "CORE_INIT_MANAGEMENT_TOKEN=") {
		t.Fatalf("token value must never be passed as Docker environment: %v", got)
	}
}

func TestRunContainerRejectsInvalidCoreBinaryDigestBeforeDocker(t *testing.T) {
	provider := Provider{Execute: func(context.Context, []string) ([]byte, error) { t.Fatal("Docker must not run"); return nil, nil }}
	err := provider.RunContainer(context.Background(), domain.DockerContainerRequest{
		Name: "liapoldus-core", Image: "alpine:latest", Network: "runtime-net", StagedBinary: "/tmp/core", Label: "studio-local", CoreBinaryDigest: "not-a-digest",
	})
	if err == nil || err.Error() != "invalid Core binary digest" {
		t.Fatalf("expected Core digest validation, got %v", err)
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

func TestContainerLifecycleCommandsUseValidatedExactNames(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(Provider) error
		want []string
	}{
		{name: "start", run: func(provider Provider) error { return provider.StartContainer(context.Background(), "liapoldus-echo") }, want: []string{"start", "liapoldus-echo"}},
		{name: "stop", run: func(provider Provider) error { return provider.StopContainer(context.Background(), "liapoldus-echo") }, want: []string{"stop", "--time", "10", "liapoldus-echo"}},
		{name: "restart", run: func(provider Provider) error {
			return provider.RestartContainer(context.Background(), "liapoldus-echo")
		}, want: []string{"restart", "--time", "10", "liapoldus-echo"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			provider := Provider{BinaryPath: "docker", Execute: func(_ context.Context, args []string) ([]byte, error) {
				got = append([]string(nil), args...)
				return nil, nil
			}}
			if err := test.run(provider); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Docker argv=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestInspectUsesTypedContainerNameAndReturnsDockerRecordForProjection(t *testing.T) {
	var got []string
	provider := Provider{Execute: func(_ context.Context, args []string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`[]`), nil
	}}
	if _, err := provider.Inspect(context.Background(), "liapoldus-server"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "inspect liapoldus-server" {
		t.Fatalf("unexpected Docker inspect argv: %v", got)
	}
}
