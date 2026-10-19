package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

const maxOutputBytes = 1 << 20

// Provider is a deliberately small Docker CLI adapter. It has no build
// methods: binaries are operator-owned artifacts and Docker is runtime-only.
type Provider struct {
	BinaryPath  string
	Execute     func(context.Context, []string) ([]byte, error)
	StartEngine func(context.Context) error
}

func Default() Provider {
	path, err := exec.LookPath("docker")
	if err != nil {
		path = "docker"
	}
	return Provider{BinaryPath: path}
}

func (provider Provider) run(ctx context.Context, args ...string) ([]byte, error) {
	if provider.Execute != nil {
		return provider.Execute(ctx, args)
	}
	command := exec.CommandContext(ctx, provider.BinaryPath, args...) //nolint:gosec // argv is assembled by typed methods below.
	output, err := command.CombinedOutput()
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes]
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 2048 {
			detail = detail[len(detail)-2048:]
		}
		if detail != "" {
			return output, fmt.Errorf("docker %s: %w: %s", args[0], err, detail)
		}
		return output, fmt.Errorf("docker %s: %w", args[0], err)
	}
	return output, nil
}

func (provider Provider) Probe(ctx context.Context) domain.DockerProviderStatus {
	status := domain.DockerProviderStatus{ID: "docker", BinaryPath: provider.BinaryPath, Platform: "linux/" + runtime.GOARCH}
	if _, err := provider.run(ctx, "--version"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			status.ErrorCode = "provider_missing"
		} else {
			status.ErrorCode = "provider_unavailable"
		}
		return status
	}
	version, err := provider.run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		status.ErrorCode = "provider_unavailable"
		return status
	}
	status.Version = strings.TrimSpace(string(version))
	status.Available = status.Version != ""
	if !status.Available {
		status.ErrorCode = "provider_unavailable"
	}
	return status
}

// EnsureAvailable starts the locally installed Docker Engine when its CLI is
// present but the daemon is stopped. It never installs Docker or changes the
// selected context; it waits for the normal Docker API probe to succeed.
func (provider Provider) EnsureAvailable(ctx context.Context) (domain.DockerProviderStatus, error) {
	status := provider.Probe(ctx)
	if status.Available || status.ErrorCode == "provider_missing" {
		return status, nil
	}
	start := provider.StartEngine
	if start == nil {
		start = func(ctx context.Context) error {
			return startLocalEngine(ctx, provider.BinaryPath)
		}
	}
	if err := start(ctx); err != nil {
		return status, fmt.Errorf("docker engine start failed: %w", err)
	}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		status = provider.Probe(ctx)
		if status.Available {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-deadline.C:
			return status, errors.New("docker engine did not become available within 45 seconds")
		case <-ticker.C:
		}
	}
}

func startLocalEngine(ctx context.Context, dockerBinary string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		application, err := selectedDarwinDockerApplication(ctx, dockerBinary)
		if err != nil {
			return err
		}
		command = exec.CommandContext(ctx, "open", "-a", application)
	case "linux":
		command = exec.CommandContext(ctx, "systemctl", "start", "docker")
	case "windows":
		command = exec.CommandContext(ctx, "sc", "start", "com.docker.service")
	default:
		return fmt.Errorf("automatic Docker Engine start is unsupported on %s", runtime.GOOS)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func selectedDarwinDockerApplication(ctx context.Context, dockerBinary string) (string, error) {
	if strings.TrimSpace(dockerBinary) == "" {
		dockerBinary = "docker"
	}
	contextOutput, err := exec.CommandContext(ctx, dockerBinary, "context", "show").Output()
	if err != nil {
		return "", fmt.Errorf("inspect selected Docker context: %w", err)
	}
	contextName := strings.TrimSpace(string(contextOutput))
	if contextName == "" {
		return "", errors.New("selected Docker context is empty")
	}
	if strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")) == "" {
		if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
			return selectDarwinDockerApplication(contextName, "", host, "")
		}
	}

	endpointOutput, err := exec.CommandContext(ctx, dockerBinary, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}", contextName).Output()
	if err != nil {
		return "", fmt.Errorf("inspect selected Docker context endpoint: %w", err)
	}
	return selectDarwinDockerApplication(contextName, strings.TrimSpace(string(endpointOutput)), os.Getenv("DOCKER_HOST"), os.Getenv("DOCKER_CONTEXT"))
}

func selectDarwinDockerApplication(contextName, contextEndpoint, dockerHost, dockerContext string) (string, error) {
	if strings.TrimSpace(dockerContext) == "" && strings.TrimSpace(dockerHost) != "" {
		contextEndpoint = dockerHost
	}
	return darwinDockerApplication(contextName, contextEndpoint)
}

func darwinDockerApplication(contextName, endpoint string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(contextName))
	host := strings.ToLower(strings.TrimSpace(endpoint))
	if name == "orbstack" || strings.Contains(host, ".orbstack/run/docker.sock") {
		return "OrbStack", nil
	}
	if name == "desktop-linux" || strings.Contains(host, ".docker/run/docker.sock") || host == "unix:///var/run/docker.sock" {
		return "Docker", nil
	}
	return "", fmt.Errorf("automatic Docker Engine start cannot identify the app for context %q", contextName)
}

func (provider Provider) NetworkCreate(ctx context.Context, name, label string) error {
	if !validName(name) || !validName(label) {
		return errors.New("invalid Docker runtime network identity")
	}
	_, err := provider.run(ctx, "network", "create", "--label", "com.liapoldus.runtime="+label, name)
	if err == nil {
		return nil
	}
	// A cancelled or failed previous operation may leave its empty network
	// behind. Reuse it only when the immutable runtime label matches exactly.
	output, inspectErr := provider.run(ctx, "network", "inspect", "--format", "{{index .Labels \"com.liapoldus.runtime\"}}", name)
	if inspectErr == nil && strings.TrimSpace(string(output)) == label {
		return nil
	}
	return err
}

func (provider Provider) NetworkRemove(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid Docker runtime network name")
	}
	_, err := provider.run(ctx, "network", "rm", name)
	return err
}

func (provider Provider) RunContainer(ctx context.Context, request domain.DockerContainerRequest) error {
	if !validName(request.Name) || !validName(request.Network) || !validName(request.Label) || strings.TrimSpace(request.Image) == "" {
		return errors.New("invalid Docker runtime container request")
	}
	if !filepath.IsAbs(request.StagedBinary) || strings.ContainsAny(request.StagedBinary, ",\x00\r\n") {
		return errors.New("staged binary must be absolute")
	}
	if request.Platform != "" && !validPlatform(request.Platform) {
		return errors.New("invalid Docker runtime platform")
	}
	stagedTarget := request.StagedBinaryTarget
	if stagedTarget == "" {
		stagedTarget = "/liapoldus/service"
	}
	if !validContainerPath(stagedTarget) {
		return errors.New("invalid staged binary container path")
	}
	// Keep failed containers until the typed runtime operation collects bounded
	// logs and removes them. Successful stop also removes them explicitly.
	command := []string{"run", "--detach", "--read-only", "--name", request.Name, "--network", request.Network, "--label", "com.liapoldus.runtime=" + request.Label}
	if request.CoreBinaryDigest != "" {
		if len(request.CoreBinaryDigest) != 64 || strings.Trim(request.CoreBinaryDigest, "0123456789abcdef") != "" {
			return errors.New("invalid Core binary digest")
		}
		command = append(command, "--label", "com.liapoldus.core.binary.sha256="+request.CoreBinaryDigest)
	}
	if request.Platform != "" {
		command = append(command, "--platform", request.Platform)
	}
	command = append(command, "--mount", "type=bind,src="+request.StagedBinary+",dst="+stagedTarget+",readonly")
	command = append(command, "--tmpfs", "/tmp")
	for _, alias := range request.NetworkAliases {
		if !validName(alias) {
			return errors.New("invalid Docker network alias")
		}
		command = append(command, "--network-alias", alias)
	}
	for _, host := range request.ExtraHosts {
		if !validExtraHost(host) {
			return errors.New("invalid Docker runtime extra host")
		}
		command = append(command, "--add-host", host)
	}
	for _, mount := range request.Mounts {
		if !filepath.IsAbs(mount.Source) || strings.ContainsAny(mount.Source, ",\x00\r\n") || !validContainerPath(mount.Target) {
			return errors.New("invalid Docker runtime mount")
		}
		mountSpec := "type=bind,src=" + mount.Source + ",dst=" + mount.Target
		// Docker's --mount syntax accepts `readonly` as a flag, but `rw` is
		// not a valid field. Read-write is the default, so omit the option.
		if mount.ReadOnly {
			mountSpec += ",readonly"
		}
		command = append(command, "--mount", mountSpec)
	}
	for _, port := range request.PublishedPorts {
		if port.Host <= 0 || port.Host > 65535 || port.Container <= 0 || port.Container > 65535 {
			return errors.New("invalid Docker runtime published port")
		}
		address := net.ParseIP(port.HostIP)
		if address == nil || !address.IsLoopback() {
			return errors.New("docker runtime ports must be published to an explicit loopback address")
		}
		hostIP := port.HostIP
		if strings.Contains(hostIP, ":") {
			hostIP = "[" + hostIP + "]"
		}
		command = append(command, "--publish", fmt.Sprintf("%s:%d:%d", hostIP, port.Host, port.Container))
	}
	keys := make([]string, 0, len(request.Environment))
	for key := range request.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !validEnvKey(key) || strings.ContainsAny(request.Environment[key], "\x00\r\n") {
			return errors.New("invalid Docker runtime environment")
		}
		command = append(command, "--env", key+"="+request.Environment[key])
	}
	entrypoint := request.Entrypoint
	if entrypoint == "" {
		entrypoint = stagedTarget
	}
	if !validContainerPath(entrypoint) {
		return errors.New("invalid Docker runtime entrypoint")
	}
	command = append(command, "--entrypoint", entrypoint, request.Image)
	if strings.TrimSpace(request.Command) != "" {
		// Command is an optional, service-owned argument list entry. Runtime
		// binaries normally bootstrap from environment/configuration and must
		// not receive a synthetic positional service id.
		command = append(command, request.Command)
	}
	command = append(command, request.Arguments...)
	_, err := provider.run(ctx, command...)
	return err
}

func validPlatform(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validName(parts[0]) && validName(parts[1])
}

func (provider Provider) ListContainers(ctx context.Context, label string) ([]string, error) {
	if !validName(label) {
		return nil, errors.New("invalid Docker runtime label")
	}
	output, err := provider.run(ctx, "ps", "--all", "--filter", "label=com.liapoldus.runtime="+label, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			names = append(names, strings.TrimSpace(line))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (provider Provider) StopContainer(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid Docker container name")
	}
	_, err := provider.run(ctx, "stop", "--time", "10", name)
	return err
}

func (provider Provider) StartContainer(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid Docker container name")
	}
	_, err := provider.run(ctx, "start", name)
	return err
}

func (provider Provider) RestartContainer(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid Docker container name")
	}
	_, err := provider.run(ctx, "restart", "--time", "10", name)
	return err
}

func (provider Provider) RemoveContainer(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid Docker container name")
	}
	_, err := provider.run(ctx, "rm", "--force", name)
	return err
}

func (provider Provider) Logs(ctx context.Context, name string) ([]byte, error) {
	if !validName(name) {
		return nil, errors.New("invalid Docker container name")
	}
	return provider.run(ctx, "logs", "--tail", "500", name)
}

func (provider Provider) Inspect(ctx context.Context, name string) ([]byte, error) {
	if !validName(name) {
		return nil, errors.New("invalid Docker container name")
	}
	// The presentation adapter projects this full record onto a safe summary
	// before returning it to Studio. Keep the Docker-specific data here; never
	// write the raw result to CLI stdout.
	return provider.run(ctx, "inspect", name)
}

func (provider Provider) Exec(ctx context.Context, name, executable string, args []string) ([]byte, error) {
	if !validName(name) || !validName(executable) || strings.ContainsAny(executable, "/\\") {
		return nil, errors.New("runtime exec is not allowlisted")
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return nil, errors.New("runtime exec argument is invalid")
		}
	}
	return provider.run(ctx, append([]string{"exec", name, executable}, args...)...)
}

func validName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func validEnvKey(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_') {
			return false
		}
	}
	return true
}

func validContainerPath(value string) bool {
	return filepath.IsAbs(value) && !strings.ContainsAny(value, "\x00\r\n") && !strings.Contains(value, "..")
}

func validExtraHost(value string) bool {
	parts := strings.Split(value, ":")
	return len(parts) == 2 && validName(parts[0]) && (parts[1] == "host-gateway" || net.ParseIP(parts[1]) != nil)
}
