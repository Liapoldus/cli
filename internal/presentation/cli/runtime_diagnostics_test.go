package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestSafeRuntimeLogSummaryBoundsAndRedactsStartupOutput(t *testing.T) {
	got := safeRuntimeLogSummary([]byte(strings.Repeat("x", 2300)))

	if len(got) > 512+len("…") {
		t.Fatalf("summary length = %d, want at most 513", len(got))
	}

	got = safeRuntimeLogSummary([]byte("Bearer runtime-secret /Users/alice/private/service.key"))
	if strings.Contains(got, "runtime-secret") || strings.Contains(got, "/Users/alice") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("summary did not redact sensitive values: %q", got)
	}
}

func TestSafeRuntimeLogSummaryKeepsActionableStartupMessage(t *testing.T) {
	got := safeRuntimeLogSummary([]byte("stage=control-client: failed to connect to Core\n"))
	if !strings.Contains(got, "failed to connect to Core") {
		t.Fatalf("summary = %q, want actionable startup message", got)
	}
}

type startupLogDockerStub struct{ logs map[string][]byte }

func (startupLogDockerStub) Probe(context.Context) domain.DockerProviderStatus {
	return domain.DockerProviderStatus{}
}
func (startupLogDockerStub) NetworkCreate(context.Context, string, string) error { return nil }
func (startupLogDockerStub) NetworkRemove(context.Context, string) error         { return nil }
func (startupLogDockerStub) RunContainer(context.Context, domain.DockerContainerRequest) error {
	return nil
}
func (startupLogDockerStub) ListContainers(context.Context, string) ([]string, error) {
	return nil, nil
}
func (startupLogDockerStub) StopContainer(context.Context, string) error    { return nil }
func (startupLogDockerStub) StartContainer(context.Context, string) error   { return nil }
func (startupLogDockerStub) RestartContainer(context.Context, string) error { return nil }
func (startupLogDockerStub) RemoveContainer(context.Context, string) error  { return nil }
func (docker startupLogDockerStub) Logs(_ context.Context, name string) ([]byte, error) {
	return docker.logs[name], nil
}
func (startupLogDockerStub) Inspect(_ context.Context, name string) ([]byte, error) {
	return []byte(`[ {"Name":"/` + name + `","Config":{"Image":"alpine:latest"},"State":{"Status":"running"}} ]`), nil
}
func (startupLogDockerStub) Exec(context.Context, string, string, []string) ([]byte, error) {
	return nil, nil
}

func TestRuntimeStartupDiagnosticsIncludeSafeCoreLog(t *testing.T) {
	var output bytes.Buffer
	coreLine := `Core service metadata read failed: instance="echo" phase="manifest" reason="deadline_exceeded"`
	docker := startupLogDockerStub{logs: map[string][]byte{
		"liapoldus-core": []byte(coreLine),
		"liapoldus-echo": []byte("echo started"),
	}}
	emitRuntimeStartupLogs(context.Background(), &output, docker, []string{"liapoldus-core", "liapoldus-echo"})
	if !strings.Contains(output.String(), "Core startup output: "+coreLine) || !strings.Contains(output.String(), "service echo startup output: echo started") {
		t.Fatalf("startup diagnostics omitted Core or service output: %s", output.String())
	}
}
