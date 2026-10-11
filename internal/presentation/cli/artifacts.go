package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

func (r Runner) plugin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "plugin artifact|inspect|manifest|schema|status is required")
	}
	if args[0] != "artifact" {
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "plugin id is required")
		}
		options, err := parseOptions(args[2:])
		if err != nil {
			return fail(stderr, application.ExitArguments, err.Error())
		}
		if options.target == "" {
			return fail(stderr, application.ExitArguments, "--target is required")
		}
		connection, err := r.credentials(ctx, options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if r.CoreFactory == nil {
			return fail(stderr, application.ExitTarget, "Core client is not configured")
		}
		client, err := r.CoreFactory(connection)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		view := args[0]
		if view == "inspect" {
			view = "status"
		}
		if view != "manifest" && view != "schema" && view != "status" {
			return fail(stderr, application.ExitArguments, "unsupported plugin command")
		}
		body, err := client.PluginMetadata(ctx, args[1], view)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, json.RawMessage(body))
	}
	if len(args) < 2 {
		return fail(stderr, application.ExitArguments, "plugin artifact inspect|register|trust|list|remove is required")
	}
	if r.Artifacts == nil {
		return fail(stderr, application.ExitTarget, "artifact registry is not configured")
	}
	options, err := parseOptions(args[2:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	switch args[1] {
	case "list":
		items, err := r.Artifacts.List()
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, items)
	case "inspect":
		artifact, err := r.findArtifact(options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, artifact)
	case "register":
		if options.id == "" || options.version == "" || options.platform == "" || options.path == "" {
			return fail(stderr, application.ExitArguments, "plugin artifact register requires --id, --version, --platform and --path")
		}
		path, err := filepath.Abs(options.path)
		if err != nil {
			return fail(stderr, application.ExitArguments, err.Error())
		}
		info, err := os.Stat(path)
		if err != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("artifact binary: %v", err))
		}
		if info.IsDir() {
			return fail(stderr, application.ExitTarget, "artifact path must be a file")
		}
		digest, err := digestFile(path)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if options.digest != "" && options.digest != digest {
			return fail(stderr, application.ExitValidation, domain.ErrArtifactDigestMismatch.Error())
		}
		artifact := domain.Artifact{ID: options.id, Version: options.version, Platform: options.platform, Digest: digest, Path: path, Command: options.command, ControlEndpoint: options.controlEndpoint, PeerEndpoint: options.peerEndpoint}
		if err := r.Artifacts.Register(artifact); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, artifact)
	case "trust":
		if options.id == "" || options.version == "" || options.platform == "" || options.digest == "" {
			return fail(stderr, application.ExitArguments, "plugin artifact trust requires --id, --version, --platform and --digest")
		}
		if err := r.Artifacts.Trust(options.id, options.version, options.platform, options.digest); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		artifact, err := r.Artifacts.Find(options.id, options.version, options.platform)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, artifact)
	case "remove":
		if options.id == "" || options.version == "" || options.platform == "" {
			return fail(stderr, application.ExitArguments, "plugin artifact remove requires --id, --version and --platform")
		}
		if err := r.Artifacts.Remove(options.id, options.version, options.platform); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"removed": true, "id": options.id, "version": options.version, "platform": options.platform})
	default:
		return fail(stderr, application.ExitArguments, "unsupported plugin artifact subcommand")
	}
}

func (r Runner) findArtifact(options commandOptions) (domain.Artifact, error) {
	if options.id == "" || options.version == "" || options.platform == "" {
		return domain.Artifact{}, errors.New("--id, --version and --platform are required")
	}
	return r.Artifacts.Find(options.id, options.version, options.platform)
}

func (r Runner) runtimePlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "runtime plan|start|stop|restart|status|logs|inspect is required")
	}
	if args[0] != "plan" {
		return r.runtimeOperation(ctx, args[0], args[1:], stdout, stderr)
	}
	if r.Artifacts == nil || r.Source == nil {
		return fail(stderr, application.ExitTarget, "runtime artifact registry is not configured")
	}
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	plan, err := r.planRuntime(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, plan)
}

func (r Runner) runtimeOperation(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if r.Docker == nil || r.Artifacts == nil {
		return fail(stderr, application.ExitTarget, "Docker runtime provider is not configured")
	}
	options, err := parseOptions(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target == "" {
		return fail(stderr, application.ExitArguments, "--target is required")
	}
	target, err := r.registry(options.targets).Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if target.Transport != domain.TransportLocalProcess {
		return fail(stderr, application.ExitTarget, "Docker runtime requires a local-process target")
	}
	label := runtimeLabel("target", target.Name)
	status := r.Docker.Probe(ctx)
	if !status.Available {
		code := status.ErrorCode
		if code == "" {
			code = "provider_missing"
		}
		return fail(stderr, application.ExitTarget, code)
	}
	switch action {
	case "start":
		if strings.TrimSpace(target.RuntimeImage) == "" {
			return fail(stderr, application.ExitTarget, "runtime_image_missing")
		}
		plan, err := r.planRuntime(ctx, options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if err := validateRuntimePlanTrusted(plan); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if targetPlatform := strings.TrimSpace(target.RuntimePlatform); targetPlatform != "" {
			for _, service := range plan.Services {
				if service.Artifact.Platform != targetPlatform {
					return fail(stderr, application.ExitTarget, fmt.Sprintf("%s: %v", service.Artifact.Key(), domain.ErrArtifactPlatformMismatch))
				}
			}
		}
		started := make([]string, 0, len(plan.Services))
		keepRuntime := false
		defer func() {
			if keepRuntime {
				return
			}
			for _, name := range started {
				_ = r.Docker.StopContainer(context.Background(), name)
				_ = r.Docker.RemoveContainer(context.Background(), name)
			}
			_ = r.Docker.NetworkRemove(context.Background(), runtimeNetwork(label))
		}()
		coreCreated, startCoreErr := r.startDockerCore(ctx, target, label)
		if startCoreErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("Core start: %v", startCoreErr))
		}
		if coreCreated {
			started = append(started, coreContainer)
		}
		for _, service := range plan.Services {
			artifact := service.Artifact
			staged, stageErr := stageRuntimeArtifact(target, artifact.Path, artifact.ID)
			if stageErr != nil {
				return fail(stderr, application.ExitTarget, stageErr.Error())
			}
			request, requestErr := runtimeContainerRequest(target, service, staged, runtimeNetwork(label), label)
			if requestErr != nil {
				for _, name := range started {
					_ = r.Docker.StopContainer(ctx, name)
					_ = r.Docker.RemoveContainer(ctx, name)
				}
				return fail(stderr, application.ExitTarget, requestErr.Error())
			}
			if err := r.Docker.RunContainer(ctx, request); err != nil {
				for _, name := range started {
					_ = r.Docker.StopContainer(ctx, name)
					_ = r.Docker.RemoveContainer(ctx, name)
				}
				return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s: %v", artifact.ID, err))
			}
			started = append(started, request.Name)
		}
		if r.CoreFactory == nil {
			return fail(stderr, application.ExitTarget, "Core client is not configured")
		}
		connection, err := r.credentials(ctx, options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		client, err := r.CoreFactory(connection)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		registrations := make([]json.RawMessage, 0, len(plan.Services))
		for _, service := range plan.Services {
			artifact := service.Artifact
			// A plugin owns its SDK lifecycle registration. The CLI must never
			// probe the plugin REST surface or manufacture a registration from
			// stale local metadata. Wait until Core has observed the replica and
			// can read its live SDK metadata through the Core control plane.
			if err := waitForCorePlugin(ctx, client, artifact.ID, 30*time.Second); err != nil {
				for _, name := range started {
					_ = r.Docker.StopContainer(ctx, name)
					_ = r.Docker.RemoveContainer(ctx, name)
				}
				return fail(stderr, application.ExitTarget, fmt.Sprintf("Core metadata for %s: %v", artifact.ID, err))
			}
			metadata := domain.PluginRegistrationMetadata{
				ArtifactID: artifact.ID, ArtifactVersion: artifact.Version, Digest: artifact.Digest,
				InstanceID: artifact.ID, ReplicaID: artifact.ID,
				ControlEndpoint: service.ControlEndpoint,
				PeerEndpoint:    service.PeerEndpoint,
				RuntimeStatus:   "started",
			}
			body, registerErr := client.RegisterPlugin(ctx, metadata)
			if registerErr != nil {
				for _, name := range started {
					_ = r.Docker.StopContainer(ctx, name)
					_ = r.Docker.RemoveContainer(ctx, name)
				}
				_ = r.Docker.NetworkRemove(ctx, runtimeNetwork(label))
				return fail(stderr, application.ExitTarget, fmt.Sprintf("Core registration for %s: %v", artifact.ID, registerErr))
			}
			registrations = append(registrations, body)
		}
		// Runtime start owns the complete local debug lifecycle. Once Core has
		// observed and registered every SDK replica, apply the canonical project
		// settings and link policy through Core Management API. This intentionally
		// does not enforce the deployment command's clean-working-tree policy:
		// Studio runtime is the local developer loop and must be able to run the
		// current working tree. The CLI still sends only the typed bundle to Core.
		bundle, bundleErr := (application.BundleService{Source: r.Source}).Build(ctx, options.project, options.revision, options.target)
		if bundleErr != nil {
			return fail(stderr, application.ExitValidation, fmt.Sprintf("build runtime configuration: %v", bundleErr))
		}
		projectID, projectErr := r.Source.ProjectID(ctx, options.project, bundle.Revision)
		if projectErr != nil {
			return fail(stderr, application.ExitValidation, fmt.Sprintf("read project identity: %v", projectErr))
		}
		repository, repositoryErr := r.Source.RemoteOrigin(ctx, options.project)
		if repositoryErr != nil {
			return fail(stderr, application.ExitValidation, fmt.Sprintf("read project repository: %v", repositoryErr))
		}
		applyRequest := domain.ConfigBundleRequest{
			Project: domain.ProjectRef{ID: projectID, Repository: repository, Revision: bundle.Revision},
			Bundle:  domain.BundleRef{SchemaVersion: bundle.SchemaVersion, Digest: bundle.Digest, Services: bundle.Services, Links: bundle.Links},
			Target:  domain.TargetRef{Environment: options.target},
		}
		idempotencyKey, keyErr := runtimeIdempotencyKey(label, bundle.Digest)
		if keyErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("create runtime operation key: %v", keyErr))
		}
		applyResponse, applyErr := client.Apply(ctx, applyRequest, idempotencyKey)
		if applyErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("apply runtime configuration through Core: %v", applyErr))
		}
		for _, operationID := range applyResponse.OperationIDs {
			if _, waitErr := (application.OperationService{Core: client}).Watch(ctx, operationID, 250*time.Millisecond, 60*time.Second); waitErr != nil {
				return fail(stderr, application.ExitOperation, fmt.Sprintf("Core configuration operation: %v", waitErr))
			}
		}
		policies, policyErr := application.LinkPolicies(bundle.Links)
		if policyErr != nil {
			return fail(stderr, application.ExitValidation, fmt.Sprintf("build Core link policy: %v", policyErr))
		}
		for _, policy := range policies {
			policyKey, keyErr := runtimeIdempotencyKey("link", "")
			if keyErr != nil {
				return fail(stderr, application.ExitTarget, fmt.Sprintf("create link operation key: %v", keyErr))
			}
			if _, linkErr := client.EnsurePluginLink(ctx, policy, policyKey); linkErr != nil {
				return fail(stderr, application.ExitTarget, fmt.Sprintf("apply Core link policy %s->%s: %v", policy.CallerInstanceID, policy.TargetInstanceID, linkErr))
			}
		}
		keepRuntime = true
		return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label, "network": runtimeNetwork(label), "services": started, "core": registrations})
	case "stop", "restart":
		names, err := r.stopDockerRuntime(ctx, target, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		if action == "restart" {
			return r.runtimeOperation(ctx, "start", args, stdout, stderr)
		}
		return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label, "stopped": names})
	case "status":
		names, err := r.Docker.ListContainers(ctx, label)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.status", "operationId": label, "containers": names})
	case "logs", "inspect":
		if options.service == "" {
			return fail(stderr, application.ExitArguments, "--service is required")
		}
		name := "liapoldus-" + options.service
		if options.service == "core" {
			name = coreContainer
		}
		var body []byte
		if action == "logs" {
			body, err = r.Docker.Logs(ctx, name)
		} else {
			body, err = r.Docker.Inspect(ctx, name)
		}
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		_, _ = stdout.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			_, _ = stdout.Write([]byte{'\n'})
		}
		return application.ExitOK
	default:
		return fail(stderr, application.ExitArguments, "unsupported runtime subcommand")
	}
}

func runtimeIdempotencyKey(label, digest string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "runtime-" + label + "-" + strings.TrimPrefix(digest, "sha256:") + "-" + hex.EncodeToString(nonce[:]), nil
}

func waitForCorePlugin(ctx context.Context, client domain.CoreClient, pluginID string, timeout time.Duration) error {
	if client == nil || strings.TrimSpace(pluginID) == "" {
		return errors.New("Core client and plugin id are required")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		body, err := client.PluginMetadata(waitContext, pluginID, "status")
		if err == nil && json.Valid(body) {
			return nil
		}
		lastErr = err
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

func runtimeContainerRequest(target domain.Target, service domain.RuntimeServiceArtifact, staged, network, label string) (domain.DockerContainerRequest, error) {
	artifact := service.Artifact
	config := target.Runtime
	if strings.TrimSpace(target.RuntimeImage) == "" || !filepath.IsAbs(config.TLSDirectory) {
		return domain.DockerContainerRequest{}, errors.New("runtime launch context is incomplete: runtime image and TLS directory are required")
	}
	if _, err := os.Stat(target.CAFile); err != nil {
		return domain.DockerContainerRequest{}, fmt.Errorf("runtime CA: %w", err)
	}
	pluginCertificate := filepath.Join(config.TLSDirectory, artifact.ID+".crt")
	pluginKey := filepath.Join(config.TLSDirectory, artifact.ID+".key")
	peerCertificate := filepath.Join(config.TLSDirectory, artifact.ID+"-peer.crt")
	peerKey := filepath.Join(config.TLSDirectory, artifact.ID+"-peer.key")
	crlFile := filepath.Join(config.TLSDirectory, "revoked.pem")
	for _, path := range []string{pluginCertificate, pluginKey, peerCertificate, peerKey, crlFile} {
		if _, err := os.Stat(path); err != nil {
			return domain.DockerContainerRequest{}, fmt.Errorf("runtime TLS asset %s: %w", filepath.Base(path), err)
		}
	}
	coreControlPort, err := listenPort(target.Bootstrap.ControlListen, 18181)
	if err != nil {
		return domain.DockerContainerRequest{}, errors.New("runtime Core control port is invalid")
	}
	coreURL := fmt.Sprintf("https://%s:%d", coreContainer, coreControlPort)
	controlEndpoint := service.ControlEndpoint
	peerPort, err := endpointPort(service.PeerEndpoint)
	if err != nil {
		return domain.DockerContainerRequest{}, errors.New("project peer endpoint is invalid")
	}
	peerIdentity, err := runtimePeerIdentity(pluginCertificate, pluginKey, peerCertificate, peerKey, artifact.ID)
	if err != nil {
		return domain.DockerContainerRequest{}, err
	}
	coreServerName := config.CoreServerName
	if coreServerName == "" {
		coreServerName = target.ServerName
	}
	coreName := config.CoreCommonName
	if coreName == "" {
		coreName = coreServerName
	}
	coreClientName := config.CoreClientCommonName
	if coreClientName == "" {
		coreClientName = coreName
	}
	containerCA := "/liapoldus/tls/ca.pem"
	containerCert := "/liapoldus/tls/plugin.crt"
	containerKey := "/liapoldus/tls/plugin.key"
	containerPeerCert := "/liapoldus/tls/peer.crt"
	containerPeerKey := "/liapoldus/tls/peer.key"
	containerCRL := "/liapoldus/tls/revoked.pem"
	var publicPorts runtimePublicPortSet
	if artifact.ID == config.PublicServiceID {
		publicPorts, err = runtimePublicPorts(config)
		if err != nil {
			return domain.DockerContainerRequest{}, err
		}
	}
	containerRest := "0.0.0.0:9443"
	containerPeer := "0.0.0.0:" + strconv.Itoa(peerPort)
	environment := map[string]string{
		"LIAPOLDUS_PLUGIN_ID":               artifact.ID,
		"LIAPOLDUS_RUNTIME_EVENTS":          domain.RuntimeEventsSchema,
		"LIAPOLDUS_INSTANCE_ID":             artifact.ID,
		"LIAPOLDUS_REPLICA_ID":              artifact.ID,
		"LIAPOLDUS_PLACEMENT_ID":            "container-" + artifact.ID,
		"LIAPOLDUS_REST_LISTEN":             containerRest,
		"LIAPOLDUS_REST_ADVERTISE_URL":      controlEndpoint,
		"LIAPOLDUS_CORE_URL":                coreURL,
		"LIAPOLDUS_CORE_SERVER_NAME":        coreServerName,
		"LIAPOLDUS_CORE_COMMON_NAME":        coreName,
		"LIAPOLDUS_CORE_URI":                config.CoreURI,
		"LIAPOLDUS_CORE_CLIENT_COMMON_NAME": coreClientName,
		"LIAPOLDUS_CORE_CLIENT_URI":         config.CoreClientURI,
		"LIAPOLDUS_CA_FILE":                 containerCA,
		"LIAPOLDUS_SERVER_CERT":             containerCert,
		"LIAPOLDUS_SERVER_KEY":              containerKey,
		"LIAPOLDUS_CLIENT_CERT":             containerCert,
		"LIAPOLDUS_CLIENT_KEY":              containerKey,
		"LIAPOLDUS_CRL_FILE":                containerCRL,
		"LIAPOLDUS_PEER_LISTEN":             containerPeer,
		"LIAPOLDUS_PEER_ADVERTISE_ENDPOINT": strings.TrimPrefix(service.PeerEndpoint, "https://"),
		"LIAPOLDUS_PEER_CARRIER":            "tcp",
		"LIAPOLDUS_PEER_IDENTITY":           peerIdentity,
		"LIAPOLDUS_PEER_ALLOWED_CALLER":     config.PeerAllowedCaller,
		"LIAPOLDUS_PEER_CA_FILE":            containerCA,
		"LIAPOLDUS_PEER_CERT":               containerPeerCert,
		"LIAPOLDUS_PEER_KEY":                containerPeerKey,
		"LIAPOLDUS_HOME":                    "/tmp",
		"HOME":                              "/tmp",
		"XDG_CONFIG_HOME":                   "/tmp/config",
	}
	if artifact.ID == config.PublicServiceID {
		// The SDK-backed operator surface and the product's public listener are
		// separate HTTP servers. Keep their container ports distinct so a
		// configured Caddy listener cannot fail activation by colliding with the
		// plugin's health/metrics/peer diagnostic listener.
		environment["LIAPOLDUS_PUBLIC_LISTEN"] = "0.0.0.0:" + strconv.Itoa(publicPorts.apiContainer)
	}
	request := domain.DockerContainerRequest{
		Name:               "liapoldus-" + artifact.ID,
		Image:              target.RuntimeImage,
		Network:            network,
		NetworkAliases:     []string{artifact.ID},
		Platform:           target.RuntimePlatform,
		StagedBinary:       staged,
		StagedBinaryTarget: "/liapoldus/" + service.Command,
		Entrypoint:         "/liapoldus/" + service.Command,
		Command:            artifact.Command,
		Label:              label,
		Environment:        environment,
		Mounts: []domain.DockerMount{
			{Source: target.CAFile, Target: containerCA, ReadOnly: true},
			{Source: pluginCertificate, Target: containerCert, ReadOnly: true},
			{Source: pluginKey, Target: containerKey, ReadOnly: true},
			{Source: peerCertificate, Target: containerPeerCert, ReadOnly: true},
			{Source: peerKey, Target: containerPeerKey, ReadOnly: true},
			{Source: crlFile, Target: containerCRL, ReadOnly: true},
		},
	}
	// Plugin SDK REST and peer transports stay private to the Docker network.
	// The product listener and the SDK-backed public diagnostic surface are
	// independently published only on host loopback.
	if artifact.ID == config.PublicServiceID {
		if config.PublicPort > 0 {
			request.PublishedPorts = append(request.PublishedPorts, domain.DockerPort{HostIP: "127.0.0.1", Host: config.PublicPort, Container: publicPorts.dataContainer})
		}
		if publicPorts.apiHost > 0 {
			request.PublishedPorts = append(request.PublishedPorts, domain.DockerPort{HostIP: "127.0.0.1", Host: publicPorts.apiHost, Container: publicPorts.apiContainer})
		}
	}
	return request, nil
}

// runtimePeerIdentity requires the protocol certificate to authenticate the
// same replica identity that Core registered from the SDK lifecycle
// certificate. Core's peer directory pins callers to that full identity, so a
// plugin-wide URI such as spiffe://liapoldus/plugin/echo is not sufficient.
func runtimePeerIdentity(pluginCertificate, pluginKey, peerCertificate, peerKey, pluginID string) (string, error) {
	coreIdentity, err := runtimeCertificateIdentity(pluginCertificate, pluginKey)
	if err != nil {
		return "", fmt.Errorf("plugin Core certificate identity is invalid: %w", err)
	}
	peerIdentity, err := runtimeCertificateIdentity(peerCertificate, peerKey)
	if err != nil {
		return "", fmt.Errorf("plugin peer certificate identity is invalid: %w", err)
	}
	if coreIdentity != peerIdentity {
		return "", errors.New("plugin peer certificate identity must match the Core replica certificate identity")
	}
	parsed, err := url.Parse(coreIdentity)
	if err != nil || parsed.Scheme != "spiffe" || parsed.Host != "liapoldus" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("plugin certificate must contain one Liapoldus SPIFFE replica identity")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 4 || segments[0] != "plugin" {
		return "", errors.New("plugin certificate must contain one Liapoldus SPIFFE replica identity")
	}
	instanceID, instanceErr := url.PathUnescape(segments[1])
	replicaID, replicaErr := url.PathUnescape(segments[2])
	incarnationID, incarnationErr := url.PathUnescape(segments[3])
	if instanceErr != nil || replicaErr != nil || incarnationErr != nil || instanceID != pluginID || replicaID != pluginID || incarnationID == "" {
		return "", errors.New("plugin certificate SPIFFE identity does not match its runtime replica")
	}
	return coreIdentity, nil
}

func runtimeCertificateIdentity(certificatePath, keyPath string) (string, error) {
	pair, err := tls.LoadX509KeyPair(certificatePath, keyPath)
	if err != nil {
		return "", errors.New("certificate and private key do not form a valid pair")
	}
	leaf := pair.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return "", errors.New("certificate cannot be parsed")
		}
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0] == nil {
		return "", errors.New("certificate must contain exactly one URI SAN")
	}
	return leaf.URIs[0].String(), nil
}

func runtimePublicContainerPort(config domain.RuntimeConfig) int {
	if config.PublicContainerPort > 0 {
		return config.PublicContainerPort
	}
	return 9080
}

type runtimePublicPortSet struct {
	dataContainer int
	apiContainer  int
	apiHost       int
}

func runtimePublicPorts(config domain.RuntimeConfig) (runtimePublicPortSet, error) {
	ports := runtimePublicPortSet{dataContainer: runtimePublicContainerPort(config)}
	if config.PublicContainerPort < 0 || ports.dataContainer < 1 || ports.dataContainer > 65535 || config.PublicPort < 0 || config.PublicPort > 65535 || config.PublicAPIContainerPort < 0 || config.PublicAPIHostPort < 0 {
		return runtimePublicPortSet{}, errors.New("runtime public port is invalid")
	}
	ports.apiContainer = config.PublicAPIContainerPort
	if ports.apiContainer == 0 {
		ports.apiContainer = ports.dataContainer + 1
	}
	ports.apiHost = config.PublicAPIHostPort
	if ports.apiHost == 0 && config.PublicPort > 0 {
		ports.apiHost = config.PublicPort + 1
	}
	if ports.apiContainer < 1 || ports.apiContainer > 65535 || ports.apiContainer == ports.dataContainer || ports.apiHost > 65535 || (config.PublicPort > 0 && ports.apiHost == config.PublicPort) {
		return runtimePublicPortSet{}, errors.New("runtime public API ports conflict or are invalid")
	}
	return ports, nil
}

func (r Runner) planRuntime(ctx context.Context, options commandOptions) (domain.RuntimePlan, error) {
	revision := options.revision
	if revision == "" {
		revision = "HEAD"
	}
	resolved, err := r.Source.ResolveRevision(ctx, options.project, revision)
	if err != nil {
		return domain.RuntimePlan{}, err
	}
	references, err := r.Source.RuntimeArtifacts(ctx, options.project, resolved)
	if err != nil {
		return domain.RuntimePlan{}, err
	}
	services := make([]domain.RuntimeServiceArtifact, 0, len(references))
	for _, reference := range references {
		artifact, err := r.Artifacts.Find(reference.ID, reference.Version, reference.Platform)
		if err != nil {
			return domain.RuntimePlan{}, fmt.Errorf("%s@%s/%s: %w", reference.ID, reference.Version, reference.Platform, domain.ErrArtifactNotFound)
		}
		if artifact.Digest != reference.Digest {
			return domain.RuntimePlan{}, fmt.Errorf("%s: %w", artifact.Key(), domain.ErrArtifactDigestMismatch)
		}
		if digest, err := digestFile(artifact.Path); err != nil || digest != artifact.Digest {
			return domain.RuntimePlan{}, fmt.Errorf("%s: %w", artifact.Key(), domain.ErrArtifactDigestMismatch)
		}
		if !strings.HasPrefix(artifact.Platform, "linux/") {
			return domain.RuntimePlan{}, fmt.Errorf("%s: %w", artifact.Key(), domain.ErrArtifactPlatformMismatch)
		}
		services = append(services, domain.RuntimeServiceArtifact{
			Artifact: artifact, Command: reference.Command,
			ControlEndpoint: reference.ControlEndpoint, PeerEndpoint: reference.PeerEndpoint,
		})
	}
	return domain.RuntimePlan{Project: options.project, Revision: resolved, Target: options.target, Services: services}, nil
}

func validateRuntimePlanTrusted(plan domain.RuntimePlan) error {
	for _, service := range plan.Services {
		if !service.Artifact.Trusted {
			return fmt.Errorf("%s: %w", service.Artifact.Key(), domain.ErrArtifactUntrusted)
		}
	}
	return nil
}

func runtimeLabel(_ string, target string) string {
	value := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(target)
	if len(value) > 90 {
		value = value[len(value)-90:]
	}
	digest := sha256.Sum256([]byte(target))
	return "studio-" + value + "-" + hex.EncodeToString(digest[:])[:12]
}

func runtimeNetwork(label string) string { return "liapoldus-" + label }

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return digestReader(file)
}

func digestReader(reader io.Reader) (string, error) {
	// Keep hashing in the presentation adapter independent from registry storage
	// so CLI tests can use a fake registry without touching the filesystem.
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(reader, 1<<34)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
