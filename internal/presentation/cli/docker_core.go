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

type dockerCoreConfig struct {
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

type dockerInspectContainer struct {
	State  dockerCoreState  `json:"State"`
	Config dockerCoreConfig `json:"Config"`
}

type dockerCoreStartResult struct {
	Created bool
	Updated bool
}

func parseDockerCoreState(body []byte) (dockerCoreState, error) {
	var containers []dockerInspectContainer
	if err := json.Unmarshal(body, &containers); err != nil {
		return dockerCoreState{}, errors.New("Core container returned invalid status")
	}
	if len(containers) != 1 {
		return dockerCoreState{}, errors.New("Core container returned invalid status")
	}
	state := containers[0].State
	if strings.TrimSpace(state.Status) == "" || (state.Running && state.Status != "running") {
		return dockerCoreState{}, errors.New("Core container returned invalid status")
	}
	return state, nil
}

func (r Runner) startDockerCore(ctx context.Context, target domain.Target, label string, services []domain.Service) (dockerCoreStartResult, error) {
	result := dockerCoreStartResult{}
	if target.Transport != domain.TransportLocalProcess {
		return result, errors.New("Docker Core lifecycle requires a local-process target")
	}
	if r.Docker == nil || r.CoreFactory == nil {
		return result, errors.New("Docker Core runtime is not configured")
	}
	provider, engineErr := ensureDockerProvider(ctx, r.Docker)
	if engineErr != nil {
		return result, engineErr
	}
	if !provider.Available {
		if provider.ErrorCode != "" {
			return result, errors.New(provider.ErrorCode)
		}
		return result, errors.New("provider_missing")
	}
	if strings.TrimSpace(target.RuntimeImage) == "" {
		return result, errors.New("runtime_image_missing")
	}
	if strings.TrimSpace(target.CoreBinary) == "" {
		return result, errors.New("Core binary is not configured")
	}
	if !filepath.IsAbs(target.StatePath) {
		return result, errors.New("Core state path must be absolute")
	}
	if target.RuntimePlatform != "linux/amd64" && target.RuntimePlatform != "linux/arm64" {
		return result, domain.ErrArtifactPlatformMismatch
	}
	if target.Endpoint == "" {
		return result, errors.New("Core Management endpoint is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(target.StatePath), 0o700); err != nil {
		return result, errors.New("Core state directory is unavailable")
	}
	stateInfo, stateErr := os.Lstat(target.StatePath)
	newState := errors.Is(stateErr, os.ErrNotExist)
	if stateErr != nil && !newState {
		return result, errors.New("Core state path cannot be inspected")
	}
	if !newState && !stateInfo.Mode().IsRegular() {
		return result, errors.New("Core state path must be a regular file")
	}
	info, err := os.Stat(target.CoreBinary)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("configured Linux Core binary is unavailable")
	}
	binaryDigest, err := digestFile(target.CoreBinary)
	if err != nil || !strings.HasPrefix(binaryDigest, "sha256:") {
		return result, errors.New("configured Core binary digest is unavailable")
	}
	if target.CoreBinaryDigest != "" && target.CoreBinaryDigest != binaryDigest {
		return result, errors.New("configured Core binary digest changed; review and trust the replacement before starting Core")
	}
	binaryDigest = strings.TrimPrefix(binaryDigest, "sha256:")
	credential, err := r.bootstrapManagementCredential(ctx, target, newState)
	if err != nil {
		return result, err
	}
	stateDirectory := filepath.Dir(target.StatePath)
	tokenDirectory, err := writeBootstrapToken(credential, stateDirectory)
	if err != nil {
		return result, errors.New("could not prepare one-time Core bootstrap credential")
	}
	defer os.RemoveAll(tokenDirectory)
	stagedCore, err := stageRuntimeArtifact(target, target.CoreBinary, "core")
	if err != nil {
		return result, errors.New("could not stage the configured Core binary")
	}

	network := runtimeNetwork(label)
	request, err := coreContainerRequest(target, label, network, stagedCore, tokenDirectory)
	if err != nil {
		return result, err
	}
	request.CoreBinaryDigest = binaryDigest
	if err := r.materializeServiceSecretMount(ctx, target, false, services); err != nil {
		return result, err
	}
	if err := r.Docker.NetworkCreate(ctx, network, label); err != nil {
		return result, fmt.Errorf("create Docker runtime network: %w", err)
	}
	containers, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return result, fmt.Errorf("inspect Docker runtime containers: %w", err)
	}
	for _, name := range containers {
		if name != coreContainer {
			continue
		}
		body, inspectErr := r.Docker.Inspect(ctx, coreContainer)
		if inspectErr != nil {
			return result, fmt.Errorf("inspect Core container: %w", inspectErr)
		}
		state, statusErr := parseDockerCoreState(body)
		if statusErr != nil {
			return result, statusErr
		}
		inspected, inspectErr := parseDockerCoreInspect(body)
		if inspectErr != nil {
			return result, inspectErr
		}
		if inspected.Config.Labels["com.liapoldus.runtime"] != label {
			return result, errors.New("Core container is not owned by the selected target")
		}
		if state.Running || state.Status == "running" {
			if inspected.Config.Image == target.RuntimeImage && inspected.Config.Labels["com.liapoldus.core.binary.sha256"] == binaryDigest {
				if err := r.waitDockerCore(ctx, target, 3*time.Second); err != nil {
					return result, fmt.Errorf("Core container is running but Management API is unavailable: %w", err)
				}
				return result, nil
			}
			result.Updated = true
			if err := r.Docker.StopContainer(ctx, coreContainer); err != nil {
				return dockerCoreStartResult{}, fmt.Errorf("stop outdated Core container: %w", err)
			}
			if err := r.Docker.RemoveContainer(ctx, coreContainer); err != nil {
				return dockerCoreStartResult{}, fmt.Errorf("remove outdated Core container: %w", err)
			}
		} else {
			if err := r.Docker.RemoveContainer(ctx, coreContainer); err != nil {
				return dockerCoreStartResult{}, fmt.Errorf("remove stopped Core container: %w", err)
			}
		}
	}
	if err := r.Docker.RunContainer(ctx, request); err != nil {
		return result, fmt.Errorf("start Core Docker container: %w", describeCoreMountFailure(err, request, target.Name))
	}
	if err := r.waitDockerCore(ctx, target, 35*time.Second); err != nil {
		logs, _ := r.Docker.Logs(ctx, coreContainer)
		_ = r.Docker.StopContainer(context.Background(), coreContainer)
		_ = r.Docker.RemoveContainer(context.Background(), coreContainer)
		if len(logs) > 4000 {
			logs = logs[len(logs)-4000:]
		}
		if len(logs) > 0 {
			return result, fmt.Errorf("Core Management API did not become ready: %w; bounded Core output: %s", err, strings.TrimSpace(string(logs)))
		}
		return result, fmt.Errorf("Core Management API did not become ready: %w", err)
	}
	result.Created = true
	return result, nil
}

type dockerEngineEnsurer interface {
	EnsureAvailable(context.Context) (domain.DockerProviderStatus, error)
}

func ensureDockerProvider(ctx context.Context, provider domain.DockerProvider) (domain.DockerProviderStatus, error) {
	status := provider.Probe(ctx)
	if status.Available {
		return status, nil
	}
	if ensurer, ok := provider.(dockerEngineEnsurer); ok {
		return ensurer.EnsureAvailable(ctx)
	}
	return status, nil
}

// describeCoreMountFailure replaces Docker's host-path-bearing bind error with
// a stable, actionable diagnostic. Paths from target profiles and the local
// credential store must never be copied into CLI JSONL events rendered by
// Studio.
func describeCoreMountFailure(err error, request domain.DockerContainerRequest, target string) error {
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "bind source path does not exist") {
		return err
	}
	role := "a required Core runtime mount"
	if dockerErrorReferencesMountSource(err.Error(), request.StagedBinary) {
		role = "the staged Core binary"
	} else {
		for _, mount := range request.Mounts {
			if dockerErrorReferencesMountSource(err.Error(), mount.Source) {
				switch {
				case mount.Target == "/var/lib/liapoldus/secrets":
					role = "the private read-only service-secret runtime mount"
				case mount.Target == "/var/lib/liapoldus":
					role = "the Core state directory"
				case mount.Target == "/run/secrets":
					role = "the temporary Core bootstrap-token directory"
				case strings.HasPrefix(mount.Target, "/liapoldus/tls/"):
					role = "a Core TLS asset"
				}
				break
			}
		}
	}
	return fmt.Errorf("docker_mount_source_unavailable: Docker cannot access %s for target %s; verify the selected Docker context can bind-mount this host location", role, target)
}

func dockerErrorReferencesMountSource(message, source string) bool {
	if source == "" {
		return false
	}
	if strings.Contains(message, source) {
		return true
	}
	// macOS aliases such as /tmp → /private/tmp can be canonicalized by Docker
	// Desktop/OrbStack before they are echoed in a bind-mount error. Resolve the
	// host-side spelling only for matching; never include either path in output.
	canonical, err := filepath.EvalSymlinks(source)
	return err == nil && canonical != source && strings.Contains(message, canonical)
}

func parseDockerCoreInspect(body []byte) (dockerInspectContainer, error) {
	var containers []dockerInspectContainer
	if err := json.Unmarshal(body, &containers); err != nil || len(containers) != 1 {
		return dockerInspectContainer{}, errors.New("Core container returned invalid status")
	}
	return containers[0], nil
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

func writeBootstrapToken(token, parentDirectory string) (string, error) {
	if len(token) < 32 || len(token) > 2048 || strings.ContainsAny(token, "\x00\r\n") {
		return "", errors.New("invalid bootstrap credential")
	}
	if !filepath.IsAbs(parentDirectory) {
		return "", errors.New("invalid Core state directory")
	}
	directory, err := os.MkdirTemp(parentDirectory, ".core-bootstrap-")
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
		return domain.DockerContainerRequest{}, errors.New("Core service-control listen port is invalid")
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
	mounts = append(mounts, domain.DockerMount{Source: stateDirectory, Target: "/var/lib/liapoldus", ReadOnly: false})
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		return domain.DockerContainerRequest{}, errors.New("local secret runtime mount is unavailable")
	}
	mounts = append(mounts, domain.DockerMount{Source: secretRoot, Target: "/var/lib/liapoldus/secrets", ReadOnly: true})
	controlPublicURL := fmt.Sprintf("https://%s:%d", coreContainer, controlPort)
	environment := map[string]string{"CORE_SQLITE_PATH": "/var/lib/liapoldus/" + filepath.Base(target.StatePath)}
	if tokenDirectory != "" {
		relativeTokenDirectory, err := filepath.Rel(stateDirectory, tokenDirectory)
		if err != nil || relativeTokenDirectory == "." || relativeTokenDirectory == ".." || strings.HasPrefix(relativeTokenDirectory, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeTokenDirectory) {
			return domain.DockerContainerRequest{}, errors.New("Core bootstrap credential must be inside the mounted state directory")
		}
		environment["CORE_INIT_MANAGEMENT_TOKEN_FILE"] = "/var/lib/liapoldus/" + filepath.ToSlash(filepath.Join(relativeTokenDirectory, "management-token"))
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
		state, statusErr := parseDockerCoreState(body)
		if statusErr != nil {
			return nil, statusErr
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

// inspectTargetCore returns Core only when its container is present in the
// selected target's exact runtime-label scope. Core lifecycle commands must
// never use stopDockerRuntime: that operation intentionally tears down the
// complete service stack and its network.
func (r Runner) inspectTargetCore(ctx context.Context, label string) (dockerInspectContainer, bool, error) {
	names, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return dockerInspectContainer{}, false, err
	}
	for _, name := range names {
		if name != coreContainer {
			continue
		}
		body, err := r.Docker.Inspect(ctx, coreContainer)
		if err != nil {
			return dockerInspectContainer{}, false, err
		}
		container, err := parseDockerCoreInspect(body)
		if err != nil {
			return dockerInspectContainer{}, false, err
		}
		if container.Config.Labels["com.liapoldus.runtime"] != label {
			return dockerInspectContainer{}, false, errors.New("Core container is not owned by the selected target")
		}
		if strings.TrimSpace(container.State.Status) == "" || (container.State.Running && container.State.Status != "running") {
			return dockerInspectContainer{}, false, errors.New("Core container returned invalid status")
		}
		return container, true, nil
	}
	return dockerInspectContainer{}, false, nil
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
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("runtime staging directory contains an unsafe path component")
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
		started, err := r.startDockerCore(ctx, target, label, nil)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "container": coreContainer, "running": true, "created": started.Created, "updated": started.Updated})
	case "stop":
		container, exists, err := r.inspectTargetCore(ctx, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if !exists {
			return fail(stderr, application.ExitTarget, "Core container is not present in the selected target")
		}
		stopped := container.State.Running || container.State.Status == "running"
		if stopped {
			if err := r.Docker.StopContainer(ctx, coreContainer); err != nil {
				return fail(stderr, application.ExitTarget, fmt.Sprintf("stop Core container: %v", err))
			}
		}
		if secretRoot, rootErr := serviceSecretRoot(target); rootErr != nil || clearServiceSecretMount(secretRoot) != nil {
			return fail(stderr, application.ExitTarget, "Core stopped, but local service-secret runtime cleanup failed")
		}
		return encode(stdout, map[string]any{"target": target.Name, "container": coreContainer, "stopped": stopped})
	case "restart":
		container, exists, err := r.inspectTargetCore(ctx, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if !exists {
			return fail(stderr, application.ExitTarget, "Core container is not present in the selected target; start it first")
		}
		if !container.State.Running && container.State.Status != "running" {
			return fail(stderr, application.ExitTarget, "Core is stopped; use `runtime start --project <path> --revision WORKING_TREE --target local` to restore the selected Project's scoped service secrets")
		}
		if err := r.Docker.RestartContainer(ctx, coreContainer); err != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("restart Core container: %v", err))
		}
		if err := r.waitDockerCore(ctx, target, 35*time.Second); err != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("Core container restarted but its Management API is unreachable: %v", err))
		}
		return encode(stdout, map[string]any{"target": target.Name, "container": coreContainer, "running": true, "restarted": true})
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
