package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

const coreContainer = "liapoldus-core"

var runtimeUserCacheDir = os.UserCacheDir

type dockerCoreState struct {
	Status  string `json:"Status"`
	Running bool   `json:"Running"`
}

func (r Runner) startDockerCore(ctx context.Context, target domain.Target, label string) (bool, error) {
	if target.Transport != domain.TransportLocalProcess {
		return false, errors.New("Docker Core lifecycle requires a local-process target")
	}
	if r.Docker == nil || r.CoreFactory == nil {
		return false, errors.New("Docker Core runtime is not configured")
	}
	provider := r.Docker.Probe(ctx)
	if !provider.Available {
		if provider.ErrorCode != "" {
			return false, errors.New(provider.ErrorCode)
		}
		return false, errors.New("provider_missing")
	}
	if strings.TrimSpace(target.RuntimeImage) == "" {
		return false, errors.New("runtime_image_missing")
	}
	if strings.TrimSpace(target.CoreBinary) == "" {
		return false, errors.New("Core binary is not configured")
	}
	if !filepath.IsAbs(target.StatePath) {
		return false, errors.New("Core state path must be absolute")
	}
	if target.RuntimePlatform != "linux/amd64" && target.RuntimePlatform != "linux/arm64" {
		return false, domain.ErrArtifactPlatformMismatch
	}
	if target.Endpoint == "" {
		return false, errors.New("Core Management endpoint is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(target.StatePath), 0o700); err != nil {
		return false, errors.New("Core state directory is unavailable")
	}
	stateInfo, stateErr := os.Lstat(target.StatePath)
	newState := errors.Is(stateErr, os.ErrNotExist)
	if stateErr != nil && !newState {
		return false, errors.New("Core state path cannot be inspected")
	}
	if !newState && !stateInfo.Mode().IsRegular() {
		return false, errors.New("Core state path must be a regular file")
	}
	if _, err := os.Stat(target.CoreBinary); err != nil {
		return false, errors.New("configured Linux Core binary is unavailable")
	}

	network := runtimeNetwork(label)
	if err := r.Docker.NetworkCreate(ctx, network, label); err != nil {
		return false, fmt.Errorf("create Docker runtime network: %w", err)
	}
	containers, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return false, fmt.Errorf("inspect Docker runtime containers: %w", err)
	}
	for _, name := range containers {
		if name != coreContainer {
			continue
		}
		body, inspectErr := r.Docker.Inspect(ctx, coreContainer)
		if inspectErr != nil {
			return false, fmt.Errorf("inspect Core container: %w", inspectErr)
		}
		var state dockerCoreState
		if json.Unmarshal(body, &state) != nil {
			return false, errors.New("Core container returned invalid status")
		}
		if state.Running || state.Status == "running" {
			if err := r.waitDockerCore(ctx, target, 3*time.Second); err != nil {
				return false, fmt.Errorf("Core container is running but Management API is unavailable: %w", err)
			}
			return false, nil
		}
		_ = r.Docker.RemoveContainer(ctx, coreContainer)
		break
	}

	credential, err := r.bootstrapManagementCredential(ctx, target, newState)
	if err != nil {
		return false, err
	}
	tokenDirectory, err := writeBootstrapToken(credential)
	if err != nil {
		return false, errors.New("could not prepare one-time Core bootstrap credential")
	}
	defer os.RemoveAll(tokenDirectory)

	stagedCore, err := stageRuntimeArtifact(target, target.CoreBinary, "core")
	if err != nil {
		return false, errors.New("could not stage the configured Core binary")
	}
	request, err := coreContainerRequest(target, label, network, stagedCore, tokenDirectory)
	if err != nil {
		return false, err
	}
	if err := r.Docker.RunContainer(ctx, request); err != nil {
		return false, fmt.Errorf("start Core Docker container: %w", err)
	}
	if err := r.waitDockerCore(ctx, target, 35*time.Second); err != nil {
		logs, _ := r.Docker.Logs(ctx, coreContainer)
		_ = r.Docker.StopContainer(context.Background(), coreContainer)
		_ = r.Docker.RemoveContainer(context.Background(), coreContainer)
		if len(logs) > 4000 {
			logs = logs[len(logs)-4000:]
		}
		if len(logs) > 0 {
			return false, fmt.Errorf("Core Management API did not become ready: %w; bounded Core output: %s", err, strings.TrimSpace(string(logs)))
		}
		return false, fmt.Errorf("Core Management API did not become ready: %w", err)
	}
	return true, nil
}

func (r Runner) bootstrapManagementCredential(ctx context.Context, target domain.Target, newState bool) (string, error) {
	if target.CredentialRef == "" {
		target.CredentialRef = domain.ManagementCredentialReference(target.Name)
	}
	provider := r.credentialProvider()
	if provider == nil {
		return "", domain.ErrCredentialStore
	}
	token, err := provider.Resolve(ctx, target.CredentialRef)
	if err == nil {
		return token, nil
	}
	if !newState || !errors.Is(err, domain.ErrCredentialNotFound) {
		return "", fmt.Errorf("Core management credential is unavailable: %w", err)
	}
	vault := r.credentialVault()
	if vault == nil {
		return "", domain.ErrCredentialStore
	}
	token, err = generateManagementToken()
	if err != nil {
		return "", errors.New("could not generate a fresh Core management credential")
	}
	if err := vault.Store(ctx, target.CredentialRef, token); err != nil {
		return "", domain.ErrCredentialStore
	}
	return token, nil
}

func writeBootstrapToken(token string) (string, error) {
	if len(token) < 32 || len(token) > 2048 || strings.ContainsAny(token, "\x00\r\n") {
		return "", errors.New("invalid bootstrap credential")
	}
	directory, err := os.MkdirTemp("", "liapoldus-core-secret-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(directory, "management-token"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	if _, err := file.WriteString(token); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(directory)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(directory)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}

func coreContainerRequest(target domain.Target, label, network, stagedCore, tokenDirectory string) (domain.DockerContainerRequest, error) {
	managementPort, err := endpointPort(target.Endpoint)
	if err != nil {
		return domain.DockerContainerRequest{}, errors.New("Core Management endpoint must use a local TCP port")
	}
	if !isLoopbackURL(target.Endpoint) {
		return domain.DockerContainerRequest{}, errors.New("Docker Core Management endpoint must be loopback-only")
	}
	managementListen := listenOnAllInterfaces(target.Bootstrap.ManagementListen, managementPort)
	controlPort, err := listenPort(target.Bootstrap.ControlListen, 18181)
	if err != nil {
		return domain.DockerContainerRequest{}, errors.New("Core plugin-control listen port is invalid")
	}
	if _, err := os.Stat(target.Bootstrap.ManagementCertificate); err != nil {
		return domain.DockerContainerRequest{}, errors.New("Core Management TLS certificate is unavailable")
	}
	mounts := make([]domain.DockerMount, 0, 8)
	addMount := func(source, destination string, readOnly bool) error {
		if source == "" {
			return errors.New("Core TLS reference is missing")
		}
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("Core TLS asset is unavailable")
		}
		mounts = append(mounts, domain.DockerMount{Source: source, Target: destination, ReadOnly: readOnly})
		return nil
	}
	for _, entry := range []struct{ source, target string }{
		{target.Bootstrap.ManagementCertificate, "/liapoldus/tls/management.crt"},
		{target.Bootstrap.ManagementKey, "/liapoldus/tls/management.key"},
		{target.Bootstrap.ManagementClientCA, "/liapoldus/tls/management-client-ca.pem"},
		{target.Bootstrap.ControlCertificate, "/liapoldus/tls/control.crt"},
		{target.Bootstrap.ControlKey, "/liapoldus/tls/control.key"},
		{target.Bootstrap.ReplicaClientCA, "/liapoldus/tls/replica-client-ca.pem"},
		{target.Bootstrap.ReplicaServerCA, "/liapoldus/tls/replica-server-ca.pem"},
	} {
		if err := addMount(entry.source, entry.target, true); err != nil {
			return domain.DockerContainerRequest{}, err
		}
	}
	stateDirectory := filepath.Dir(target.StatePath)
	if err := os.MkdirAll(filepath.Join(stateDirectory, "secrets"), 0o700); err != nil {
		return domain.DockerContainerRequest{}, errors.New("Core state storage is unavailable")
	}
	mounts = append(mounts, domain.DockerMount{Source: stateDirectory, Target: "/var/lib/liapoldus", ReadOnly: false})
	if tokenDirectory != "" {
		mounts = append(mounts, domain.DockerMount{Source: tokenDirectory, Target: "/run/secrets", ReadOnly: false})
	}
	controlPublicURL := fmt.Sprintf("https://%s:%d", coreContainer, controlPort)
	environment := map[string]string{"CORE_SQLITE_PATH": "/var/lib/liapoldus/" + filepath.Base(target.StatePath)}
	if tokenDirectory != "" {
		environment["CORE_INIT_MANAGEMENT_TOKEN_FILE"] = "/run/secrets/management-token"
		environment["CORE_INIT_MANAGEMENT_LISTEN"] = managementListen
		environment["CORE_INIT_MANAGEMENT_CERTIFICATE"] = "/liapoldus/tls/management.crt"
		environment["CORE_INIT_MANAGEMENT_KEY"] = "/liapoldus/tls/management.key"
		environment["CORE_INIT_MANAGEMENT_CLIENT_CA"] = "/liapoldus/tls/management-client-ca.pem"
		environment["CORE_INIT_CONTROL_LISTEN"] = fmt.Sprintf("0.0.0.0:%d", controlPort)
		environment["CORE_INIT_CONTROL_PUBLIC_URL"] = controlPublicURL
		environment["CORE_INIT_CONTROL_CERTIFICATE"] = "/liapoldus/tls/control.crt"
		environment["CORE_INIT_CONTROL_KEY"] = "/liapoldus/tls/control.key"
		environment["CORE_INIT_REPLICA_CLIENT_CA"] = "/liapoldus/tls/replica-client-ca.pem"
		environment["CORE_INIT_REPLICA_SERVER_CA"] = "/liapoldus/tls/replica-server-ca.pem"
		environment["CORE_INIT_SECRET_ROOT"] = "/var/lib/liapoldus/secrets"
	}
	return domain.DockerContainerRequest{
		Name: coreContainer, Image: target.RuntimeImage, Network: network,
		NetworkAliases: []string{coreContainer}, Platform: target.RuntimePlatform,
		StagedBinary: stagedCore, StagedBinaryTarget: "/liapoldus/core", Entrypoint: "/liapoldus/core",
		Label: label, Environment: environment, Mounts: mounts,
		PublishedPorts: []domain.DockerPort{{HostIP: "127.0.0.1", Host: managementPort, Container: managementPort}},
	}, nil
}

func (r Runner) waitDockerCore(ctx context.Context, target domain.Target, timeout time.Duration) error {
	if r.CoreFactory == nil {
		return errors.New("Core Management API client is not configured")
	}
	connection, err := r.credentials(ctx, commandOptions{target: target.Name})
	if err != nil {
		return err
	}
	client, err := r.CoreFactory(connection)
	if err != nil {
		return err
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		status, healthErr := client.Health(waitContext)
		if healthErr == nil && status >= 200 && status < 300 {
			return nil
		}
		lastErr = healthErr
		select {
		case <-waitContext.Done():
			if lastErr != nil {
				return lastErr
			}
			return waitContext.Err()
		case <-ticker.C:
		}
	}
}

func endpointPort(value string) (int, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return 0, errors.New("invalid Management endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid Management endpoint port")
	}
	return port, nil
}

func isLoopbackURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if parsed.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(parsed.Hostname())
	return ip != nil && ip.IsLoopback()
}

func listenPort(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	_, raw, err := net.SplitHostPort(value)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid listen port")
	}
	return port, nil
}

func listenOnAllInterfaces(value string, fallback int) string {
	port, err := listenPort(value, fallback)
	if err != nil {
		port = fallback
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
}

func (r Runner) dockerCoreStatus(ctx context.Context, target domain.Target, label string) (map[string]any, error) {
	if r.Docker == nil {
		return nil, errors.New("Docker runtime is not configured")
	}
	names, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"target": target.Name, "container": coreContainer, "running": false, "reachable": false}
	for _, name := range names {
		if name != coreContainer {
			continue
		}
		body, inspectErr := r.Docker.Inspect(ctx, coreContainer)
		if inspectErr != nil {
			return nil, inspectErr
		}
		var state dockerCoreState
		if json.Unmarshal(body, &state) != nil {
			return nil, errors.New("Core container returned invalid status")
		}
		running := state.Running || state.Status == "running"
		result["running"] = running
		result["containerStatus"] = state.Status
		if running {
			err := r.waitDockerCore(ctx, target, 3*time.Second)
			result["reachable"] = err == nil
			if err != nil {
				result["diagnostic"] = "Core container is running, but its Management API is unreachable"
			}
		}
		break
	}
	return result, nil
}

func (r Runner) stopDockerRuntime(ctx context.Context, target domain.Target, label string) ([]string, error) {
	names, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := r.Docker.StopContainer(ctx, name); err != nil {
			return names, err
		}
		if err := r.Docker.RemoveContainer(ctx, name); err != nil {
			return names, err
		}
	}
	if err := r.Docker.NetworkRemove(ctx, runtimeNetwork(label)); err != nil && len(names) > 0 {
		return names, err
	}
	if staging, err := runtimeStagingDirectory(target); err == nil {
		_ = os.RemoveAll(staging)
	}
	return names, nil
}

func runtimeStagingDirectory(target domain.Target) (string, error) {
	cache, err := runtimeUserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		return "", errors.New("user cache directory is unavailable")
	}
	path := filepath.Join(cache, "Liapoldus", "runtime", runtimeLabel("target", target.Name))
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", errors.New("runtime staging directory is unavailable")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", errors.New("runtime staging directory permissions are unavailable")
	}
	return path, nil
}

func stageRuntimeArtifact(target domain.Target, source, artifactID string) (string, error) {
	if strings.TrimSpace(artifactID) == "" || strings.ContainsAny(artifactID, `/\\\x00`) {
		return "", errors.New("invalid staged runtime artifact id")
	}
	digest, err := digestFile(source)
	if err != nil {
		return "", err
	}
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(staging, artifactID+"-"+strings.TrimPrefix(digest, "sha256:"))
	if existing, err := digestFile(destination); err == nil {
		if existing == digest {
			return destination, nil
		}
		return "", errors.New("runtime staging digest conflict; stop the target runtime before replacing artifacts")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return "", err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(destination)
		return "", err
	}
	return destination, nil
}

func (r Runner) coreDockerCommand(ctx context.Context, action string, target domain.Target, stdout, stderr interface{ Write([]byte) (int, error) }) int {
	if r.Docker == nil {
		return fail(stderr, application.ExitTarget, "Docker runtime is not configured")
	}
	if target.Transport != domain.TransportLocalProcess {
		return fail(stderr, application.ExitTarget, "Core Docker lifecycle requires a local-process target")
	}
	label := runtimeLabel("target", target.Name)
	switch action {
	case "start":
		created, err := r.startDockerCore(ctx, target, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "container": coreContainer, "running": true, "created": created})
	case "stop":
		names, err := r.stopDockerRuntime(ctx, target, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "stopped": names})
	case "restart":
		if _, err := r.stopDockerRuntime(ctx, target, label); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		created, err := r.startDockerCore(ctx, target, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "container": coreContainer, "running": true, "created": created, "restarted": true})
	case "status":
		result, err := r.dockerCoreStatus(ctx, target, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, result)
	case "logs":
		body, err := r.Docker.Logs(ctx, coreContainer)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		_, _ = stdout.Write(body)
		return application.ExitOK
	default:
		return fail(stderr, application.ExitArguments, "unsupported Docker Core action")
	}
}
