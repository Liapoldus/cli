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

func (r Runner) service(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "service artifact|inspect|manifest|schema|status|admin-surface|resource is required")
	}
	if args[0] == "admin-surface" {
		return r.serviceAdminSurface(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "resource" {
		return r.serviceResource(ctx, args[1:], stdout, stderr)
	}
	if args[0] != "artifact" {
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "service id is required")
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
			return fail(stderr, application.ExitArguments, "unsupported service command")
		}
		body, err := client.ServiceMetadata(ctx, args[1], view)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, json.RawMessage(body))
	}
	if len(args) < 2 {
		return fail(stderr, application.ExitArguments, "service artifact inspect|register|trust|list|remove is required")
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
		summaries := make([]artifactSummary, 0, len(items))
		for _, item := range items {
			summaries = append(summaries, summarizeArtifact(item))
		}
		return encode(stdout, summaries)
	case "inspect":
		artifact, err := r.findArtifact(options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, summarizeArtifact(artifact))
	case "register":
		if options.id == "" || options.version == "" || options.platform == "" || options.path == "" {
			return fail(stderr, application.ExitArguments, "service artifact register requires --id, --version, --platform and --path")
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
		return encode(stdout, summarizeArtifact(artifact))
	case "trust":
		if options.id == "" || options.version == "" || options.platform == "" || options.digest == "" {
			return fail(stderr, application.ExitArguments, "service artifact trust requires --id, --version, --platform and --digest")
		}
		if err := r.Artifacts.Trust(options.id, options.version, options.platform, options.digest); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		artifact, err := r.Artifacts.Find(options.id, options.version, options.platform)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, summarizeArtifact(artifact))
	case "remove":
		if options.id == "" || options.version == "" || options.platform == "" {
			return fail(stderr, application.ExitArguments, "service artifact remove requires --id, --version and --platform")
		}
		if err := r.Artifacts.Remove(options.id, options.version, options.platform); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"removed": true, "id": options.id, "version": options.version, "platform": options.platform})
	default:
		return fail(stderr, application.ExitArguments, "unsupported service artifact subcommand")
	}
}

func (r Runner) serviceAdminSurface(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "inspect" || strings.TrimSpace(args[1]) == "" {
		return fail(stderr, application.ExitArguments, "service admin-surface inspect <service-id> --target local is required")
	}
	options, err := parseOptions(args[2:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target != "local" {
		return fail(stderr, application.ExitArguments, "local --target is required for service admin-surface inspection")
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
	reader, ok := client.(domain.ServiceAdminSurfaceReader)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support service admin-surface inspection")
	}
	surface, err := reader.ReadServiceAdminSurface(ctx, args[1])
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if !validServiceAdminSurface(surface.Descriptor) || !validServiceAdminSurfaceDigest(surface.Digest, surface.Descriptor) {
		return fail(stderr, application.ExitTarget, "Core returned an invalid service admin-surface descriptor")
	}
	return encode(stdout, map[string]any{"serviceId": args[1], "digest": surface.Digest, "descriptor": surface.Descriptor})
}

func (r Runner) serviceResource(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" || (args[0] != "list" && args[0] != "publish") {
		return fail(stderr, application.ExitArguments, "service resource list|publish <service-id> is required")
	}
	options, err := parseOptions(args[2:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target != "local" {
		return fail(stderr, application.ExitArguments, "local --target is required for service resource actions")
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
	if args[0] == "publish" {
		return r.publishServiceResource(ctx, client, args[1], options, stdout, stderr)
	}
	if options.resource == "" {
		return fail(stderr, application.ExitArguments, "--resource is required")
	}
	manifest, err := client.ServiceMetadata(ctx, args[1], "manifest")
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	query, err := serviceResourceQuery(manifest, options.resource)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	response, err := client.ServiceAdminQuery(ctx, args[1], query.pageID, query.resourceID)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	items, err := sanitizeServiceResourceItems(response, query.valuePointer, query.labelPointer)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, map[string]any{"resourceId": options.resource, "items": items})
}

func (r Runner) publishServiceResource(ctx context.Context, client domain.CoreClient, serviceID string, options commandOptions, stdout, stderr io.Writer) int {
	if options.page == "" || options.action == "" || options.artifactFile == "" || options.contentType == "" || options.metadataFile == "" || options.idempotencyKey == "" || !options.projectExplicit {
		return fail(stderr, application.ExitArguments, "service resource publish requires --page, --action, --artifact-file, --content-type, --metadata-file, --idempotency-key and --project")
	}
	publisher, ok := client.(domain.ServiceArtifactPublisher)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support service artifact actions")
	}
	artifactPath, artifactInfo, err := resolveProjectFile(options.project, options.artifactFile)
	if err != nil {
		return fail(stderr, application.ExitArguments, "artifact file: "+err.Error())
	}
	metadataPath, metadataInfo, err := resolveProjectFile(options.project, options.metadataFile)
	if err != nil {
		return fail(stderr, application.ExitArguments, "metadata file: "+err.Error())
	}
	if !artifactInfo.Mode().IsRegular() || !metadataInfo.Mode().IsRegular() {
		return fail(stderr, application.ExitArguments, "artifact and metadata must be regular project files")
	}
	if metadataInfo.Size() > 1<<20 {
		return fail(stderr, application.ExitArguments, "metadata file exceeds the 1 MiB safety limit")
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		return fail(stderr, application.ExitTarget, "metadata file: "+err.Error())
	}
	artifact, err := os.Open(artifactPath)
	if err != nil {
		return fail(stderr, application.ExitTarget, "artifact file: "+err.Error())
	}
	defer artifact.Close()
	result, err := publisher.PublishServiceArtifact(ctx, domain.ServiceArtifactPublication{
		ServiceID: serviceID, PageID: options.page, ActionID: options.action,
		Metadata: json.RawMessage(metadata), ContentType: options.contentType, Artifact: artifact,
		IdempotencyKey: options.idempotencyKey, IfMatch: options.ifMatch,
	})
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, json.RawMessage(result))
}

func resolveProjectFile(projectRoot, filePath string) (string, os.FileInfo, error) {
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return "", nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(root, filePath)
	}
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("file must remain inside the project directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	return resolved, info, nil
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
	return encode(stdout, summarizeRuntimePlan(plan))
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
	if action == "start" || action == "restart" {
		status, err = ensureDockerProvider(ctx, r.Docker)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
	}
	if !status.Available {
		code := status.ErrorCode
		if code == "" {
			code = "provider_missing"
		}
		return fail(stderr, application.ExitTarget, code)
	}
	switch action {
	case "start":
		if options.service != "" {
			return r.runtimeServiceOperation(ctx, action, options.service, label, options, stdout, stderr)
		}
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
		existing, existingErr := r.Docker.ListContainers(ctx, label)
		if existingErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("inspect existing runtime: %v", existingErr))
		}
		cleaned, reconcileErr := reconcileStoppedRuntimeServices(ctx, r.Docker, existing, plan)
		if reconcileErr != nil {
			return fail(stderr, application.ExitTarget, reconcileErr.Error())
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
		coreStart, startCoreErr := r.startDockerCore(ctx, target, label)
		if startCoreErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("Core start: %v", startCoreErr))
		}
		if coreStart.Created && !coreStart.Updated {
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
			metadata := domain.ServiceRegistrationMetadata{
				ArtifactID: artifact.ID, ArtifactVersion: artifact.Version, Digest: artifact.Digest,
				InstanceID: artifact.ID, ReplicaID: artifact.ID,
				ControlEndpoint: service.ControlEndpoint,
				PeerEndpoint:    service.PeerEndpoint,
				RuntimeStatus:   "started",
			}
			// The control-plane sequence is intentional: after Docker starts the
			// binary, CLI submits only its verified artifact/runtime claim. Core
			// validates the SDK lease and fetches live SDK metadata itself. CLI
			// never probes a Service endpoint or asks Core for metadata before
			// submitting the claim.
			body, registerErr := registerRuntimeService(ctx, client, metadata, 60*time.Second)
			if registerErr != nil {
				emitRuntimeStartupLogs(ctx, stderr, r.Docker, started)
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
			keepRuntime = true
			return fail(stderr, application.ExitTarget, fmt.Sprintf("apply runtime configuration through Core: %v; containers remain running so Studio can inspect Core-backed metadata and retry after correcting settings", applyErr))
		}
		for _, operationID := range applyResponse.OperationIDs {
			observer := studioOperationObserver(ctx, stdout, target.Name)
			if _, waitErr := (application.OperationService{Core: client}).WatchObserved(ctx, operationID, 250*time.Millisecond, 60*time.Second, observer); waitErr != nil {
				keepRuntime = true
				return fail(stderr, application.ExitOperation, fmt.Sprintf("Core configuration operation: %v; containers remain running so Studio can inspect metadata and retry", waitErr))
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
			if _, linkErr := client.EnsureServiceLink(ctx, policy, policyKey); linkErr != nil {
				keepRuntime = true
				return fail(stderr, application.ExitTarget, fmt.Sprintf("apply Core link policy %s->%s: %v; containers remain running so Studio can inspect metadata and retry", policy.CallerInstanceID, policy.TargetInstanceID, linkErr))
			}
		}
		keepRuntime = true
		return encode(stdout, map[string]any{
			"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label,
			"network": runtimeNetwork(label), "services": started, "core": registrations,
			"coreCreated": coreStart.Created, "coreUpdated": coreStart.Updated,
			"cleanedStaleContainers": cleaned,
		})
	case "stop", "restart":
		if options.service != "" {
			return r.runtimeServiceOperation(ctx, action, options.service, label, options, stdout, stderr)
		}
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
		containers := make([]dockerInspectSummary, 0, len(names))
		for _, name := range names {
			body, inspectErr := r.Docker.Inspect(ctx, name)
			if inspectErr != nil {
				return fail(stderr, application.ExitTarget, "Docker container state could not be verified")
			}
			summary, summaryErr := safeDockerInspectSummary(body, name)
			if summaryErr != nil {
				return fail(stderr, application.ExitTarget, summaryErr.Error())
			}
			containers = append(containers, summary)
		}
		return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.status", "operationId": label, "containers": containers})
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
		if action == "inspect" {
			summary, summaryErr := safeDockerInspectSummary(body, name)
			if summaryErr != nil {
				return fail(stderr, application.ExitTarget, summaryErr.Error())
			}
			return encode(stdout, summary)
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

// reconcileStoppedRuntimeServices removes only known, target-labeled service
// containers that are conclusively stopped. A live service, Core container,
// unknown container name, or unrecognized state blocks Start without deleting
// anything. Core lifecycle remains owned by startDockerCore.
func reconcileStoppedRuntimeServices(ctx context.Context, docker domain.DockerProvider, existing []string, plan domain.RuntimePlan) ([]string, error) {
	if len(existing) == 0 {
		return nil, nil
	}
	expected := make(map[string]struct{}, len(plan.Services))
	for _, service := range plan.Services {
		expected["liapoldus-"+service.Artifact.ID] = struct{}{}
	}
	type containerState struct {
		name  string
		state string
	}
	states := make([]containerState, 0, len(existing))
	for _, name := range existing {
		if name == coreContainer {
			continue
		}
		if _, ok := expected[name]; !ok {
			return nil, runtimeStartConflict()
		}
		body, err := docker.Inspect(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("runtime_exists: could not safely inspect target container %s: %w", name, err)
		}
		state, err := parseRuntimeContainerState(body, name)
		if err != nil {
			return nil, err
		}
		if state.Running || state.Status == "running" || state.Status == "paused" || state.Status == "restarting" {
			return nil, runtimeStartConflict()
		}
		if state.Status != "created" && state.Status != "exited" && state.Status != "dead" {
			return nil, fmt.Errorf("runtime_exists: container %s has state %q; inspect this target before retrying", name, state.Status)
		}
		states = append(states, containerState{name: name, state: state.Status})
	}
	cleaned := make([]string, 0, len(states))
	for _, item := range states {
		if err := docker.RemoveContainer(ctx, item.name); err != nil {
			return cleaned, fmt.Errorf("remove stale target container %s (%s): %w", item.name, item.state, err)
		}
		cleaned = append(cleaned, item.name)
	}
	return cleaned, nil
}

func parseRuntimeContainerState(body []byte, expectedName string) (dockerCoreState, error) {
	var records []struct {
		Name  string          `json:"Name"`
		State dockerCoreState `json:"State"`
	}
	if err := json.Unmarshal(body, &records); err != nil || len(records) != 1 || strings.TrimPrefix(records[0].Name, "/") != expectedName {
		return dockerCoreState{}, errors.New("Docker runtime returned an invalid container state")
	}
	state := records[0].State
	if strings.TrimSpace(state.Status) == "" || state.Running && state.Status != "running" {
		return dockerCoreState{}, errors.New("Docker runtime returned an invalid container state")
	}
	return state, nil
}

func runtimeStartConflict() error {
	return errors.New("runtime_exists: a target service container is still active or cannot be safely reconciled; inspect with runtime status and stop this target before starting a fresh stack")
}

func (r Runner) runtimeServiceOperation(ctx context.Context, action, serviceID, label string, options commandOptions, stdout, stderr io.Writer) int {
	if !validRuntimeServiceID(serviceID) {
		return fail(stderr, application.ExitArguments, "--service must be a valid service id")
	}
	names, err := r.Docker.ListContainers(ctx, label)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	container := "liapoldus-" + serviceID
	if !containsRuntimeContainer(names, container) {
		return fail(stderr, application.ExitTarget, "runtime service container is not present for this target")
	}
	if action == "start" {
		return r.startRuntimeService(ctx, serviceID, container, label, options, stdout, stderr)
	}
	switch action {
	case "stop":
		err = r.Docker.StopContainer(ctx, container)
	case "restart":
		err = r.Docker.RestartContainer(ctx, container)
	default:
		return fail(stderr, application.ExitArguments, "unsupported service runtime action")
	}
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	containerState := "stopped"
	if action != "stop" {
		inspect, inspectErr := r.Docker.Inspect(ctx, container)
		if inspectErr != nil {
			emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
			return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s state could not be verified after %s; inspect the runtime output", serviceID, action))
		}
		summary, summaryErr := safeDockerInspectSummary(inspect, container)
		if summaryErr != nil || summary.State != "running" {
			emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
			return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s did not remain running after %s; inspect the runtime output", serviceID, action))
		}
		containerState = summary.State
		if err := r.waitForCoreServiceReady(ctx, options, serviceID); err != nil {
			emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
			return fail(stderr, application.ExitTarget, fmt.Sprintf("Core did not confirm service %s readiness after %s: %v", serviceID, action, err))
		}
		inspect, inspectErr = r.Docker.Inspect(ctx, container)
		if inspectErr != nil {
			emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
			return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s state could not be verified after Core readiness", serviceID))
		}
		summary, summaryErr = safeDockerInspectSummary(inspect, container)
		if summaryErr != nil || summary.State != "running" {
			emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
			return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s exited before Core readiness was confirmed", serviceID))
		}
	}
	readiness := "ready"
	if action == "stop" {
		readiness = "stopped"
	}
	return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label, "serviceId": serviceID, "action": action, "container": container, "containerState": containerState, "readiness": readiness})
}

// startRuntimeService reconciles a stopped target-owned service container to
// the exact artifact pin in the current Project. A running container is never
// replaced implicitly; Studio must stop that one service first. The previous
// trusted artifact is staged before removal so a failed replacement can
// recreate the old service without touching Core or sibling services.
func (r Runner) startRuntimeService(ctx context.Context, serviceID, container, label string, options commandOptions, stdout, stderr io.Writer) int {
	inspect, err := r.Docker.Inspect(ctx, container)
	if err != nil {
		return fail(stderr, application.ExitTarget, "service container state could not be verified")
	}
	state, err := parseRuntimeContainerState(inspect, container)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if state.Running || state.Status == "running" {
		return r.startExistingRuntimeService(ctx, serviceID, container, label, options, stdout, stderr)
	}
	if state.Status == "paused" || state.Status == "restarting" {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("runtime_exists: service container has state %q and cannot be safely updated", state.Status))
	}
	if state.Status != "created" && state.Status != "exited" && state.Status != "dead" {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("runtime_exists: service container has state %q and cannot be safely updated", state.Status))
	}

	plan, err := r.planRuntime(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	var desired *domain.RuntimeServiceArtifact
	for index := range plan.Services {
		if plan.Services[index].Artifact.ID == serviceID {
			desired = &plan.Services[index]
			break
		}
	}
	if desired == nil {
		return fail(stderr, application.ExitTarget, "service is not included in the current runtime plan")
	}
	if err := validateRuntimePlanTrusted(domain.RuntimePlan{Services: []domain.RuntimeServiceArtifact{*desired}}); err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	target, err := r.registry(options.targets).Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if target.RuntimePlatform != "" && desired.Artifact.Platform != target.RuntimePlatform {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("%s: %v", desired.Artifact.Key(), domain.ErrArtifactPlatformMismatch))
	}
	client, err := r.runtimeCoreClient(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	status, err := client.ServiceMetadata(ctx, serviceID, "status")
	var currentVersion, currentDigest string
	if err == nil {
		currentVersion, currentDigest, err = serviceArtifactFromStatus(status, serviceID)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
	} else {
		var responseError domain.CoreHTTPError
		if !errors.As(err, &responseError) || responseError.Status != 503 || responseError.Code != "service_unavailable" {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("read Core service artifact status: %v", err))
		}
		// Core has no live replica metadata while this target-owned container is
		// stopped. Recover the previous pin from the exact read-only binary mount
		// in Docker inspect, then require that digest to exist as a trusted CLI
		// registry artifact before allowing replacement/rollback.
		artifacts, listErr := r.Artifacts.List()
		if listErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("read previous service artifact registry: %v", listErr))
		}
		previous, inspectErr := previousServiceArtifactFromStoppedContainer(inspect, container, label, target, serviceID, desired.Artifact.Platform, artifacts)
		if inspectErr != nil {
			return fail(stderr, application.ExitTarget, fmt.Sprintf("Core has no live status for stopped service and its previous trusted artifact could not be proven: %v", inspectErr))
		}
		currentVersion, currentDigest = previous.Version, previous.Digest
	}
	if currentVersion == desired.Artifact.Version && currentDigest == desired.Artifact.Digest {
		return r.startExistingRuntimeService(ctx, serviceID, container, label, options, stdout, stderr)
	}
	if state.Running || state.Status == "running" {
		return fail(stderr, application.ExitTarget, "service_artifact_update_requires_stop: stop this target service before applying its changed artifact pin")
	}
	return r.replaceStoppedRuntimeService(ctx, serviceID, container, label, options, target, *desired, currentVersion, currentDigest, client, stdout, stderr)
}

func previousServiceArtifactFromStoppedContainer(inspect []byte, container, targetLabel string, target domain.Target, serviceID, platform string, artifacts []domain.Artifact) (domain.Artifact, error) {
	var records []struct {
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(inspect, &records); err != nil || len(records) != 1 {
		return domain.Artifact{}, errors.New("Docker inspect response is invalid")
	}
	record := records[0]
	if strings.TrimPrefix(record.Name, "/") != container || record.Config.Labels["com.liapoldus.runtime"] != targetLabel || record.State.Running || (record.State.Status != "exited" && record.State.Status != "created" && record.State.Status != "dead") {
		return domain.Artifact{}, errors.New("container identity or stopped state does not match the selected target service")
	}
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		return domain.Artifact{}, err
	}
	wantedDestination := "/liapoldus/" + serviceID
	var source string
	for _, mount := range record.Mounts {
		if mount.Destination != wantedDestination {
			continue
		}
		if source != "" || mount.RW || !filepath.IsAbs(mount.Source) {
			return domain.Artifact{}, errors.New("binary mount is ambiguous or writable")
		}
		source = filepath.Clean(mount.Source)
	}
	if source == "" {
		return domain.Artifact{}, errors.New("staged service binary mount is missing")
	}
	relative, err := filepath.Rel(staging, source)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || strings.Contains(relative, string(filepath.Separator)) {
		return domain.Artifact{}, errors.New("staged service binary is outside target-owned runtime storage")
	}
	digest, err := digestFile(source)
	if err != nil || filepath.Base(source) != serviceID+"-"+strings.TrimPrefix(digest, "sha256:") {
		return domain.Artifact{}, errors.New("staged service binary digest cannot be proven")
	}
	for _, artifact := range artifacts {
		if artifact.ID != serviceID || artifact.Platform != platform || artifact.Digest != digest || !artifact.Trusted || artifact.Version == "" {
			continue
		}
		return artifact, nil
	}
	return domain.Artifact{}, errors.New("staged service binary does not match a trusted artifact in the CLI registry")
}

func (r Runner) runtimeCoreClient(ctx context.Context, options commandOptions) (domain.CoreClient, error) {
	if r.CoreFactory == nil {
		return nil, errors.New("Core client is not configured")
	}
	connection, err := r.credentials(ctx, options)
	if err != nil {
		return nil, err
	}
	client, err := r.CoreFactory(connection)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func serviceArtifactFromStatus(body json.RawMessage, expectedServiceID string) (string, string, error) {
	var status struct {
		InstanceID      string `json:"instanceId"`
		ArtifactID      string `json:"artifactId"`
		ArtifactVersion string `json:"artifactVersion"`
		ArtifactDigest  string `json:"artifactDigest"`
	}
	if !json.Valid(body) || json.Unmarshal(body, &status) != nil || status.InstanceID != expectedServiceID || status.ArtifactID != expectedServiceID || strings.TrimSpace(status.ArtifactVersion) == "" {
		return "", "", errors.New("Core returned an invalid service artifact status contract")
	}
	if !validArtifactDigest(status.ArtifactDigest) {
		return "", "", errors.New("Core returned an invalid service artifact digest")
	}
	return status.ArtifactVersion, status.ArtifactDigest, nil
}

func validArtifactDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	return len(digest) == 64 && strings.Trim(digest, "0123456789abcdef") == ""
}

func (r Runner) startExistingRuntimeService(ctx context.Context, serviceID, container, label string, options commandOptions, stdout, stderr io.Writer) int {
	if err := r.Docker.StartContainer(ctx, container); err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	inspect, err := r.Docker.Inspect(ctx, container)
	if err != nil {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s state could not be verified after start", serviceID))
	}
	summary, err := safeDockerInspectSummary(inspect, container)
	if err != nil || summary.State != "running" {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s did not remain running after start", serviceID))
	}
	if err := r.waitForCoreServiceReady(ctx, options, serviceID); err != nil {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return fail(stderr, application.ExitTarget, fmt.Sprintf("Core did not confirm service %s readiness after start: %v", serviceID, err))
	}
	inspect, err = r.Docker.Inspect(ctx, container)
	if err != nil {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s state could not be verified after Core readiness", serviceID))
	}
	summary, err = safeDockerInspectSummary(inspect, container)
	if err != nil || summary.State != "running" {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service %s exited before Core readiness was confirmed", serviceID))
	}
	return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label, "serviceId": serviceID, "action": "start", "container": container, "containerState": "running", "readiness": "ready", "artifactReplaced": false})
}

func (r Runner) replaceStoppedRuntimeService(ctx context.Context, serviceID, container, label string, options commandOptions, target domain.Target, desired domain.RuntimeServiceArtifact, previousVersion, previousDigest string, client domain.CoreClient, stdout, stderr io.Writer) int {
	var previous domain.Artifact
	artifacts, err := r.Artifacts.List()
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("read previous service artifact registry: %v", err))
	}
	for _, artifact := range artifacts {
		if artifact.ID == serviceID && artifact.Version == previousVersion && artifact.Digest == previousDigest && artifact.Platform == desired.Artifact.Platform && artifact.Trusted {
			previous = artifact
			break
		}
	}
	if previous.Path == "" {
		return fail(stderr, application.ExitTarget, "artifact_rollback_unavailable: previous trusted service binary is not available in the CLI registry")
	}
	oldStaged, err := stageRuntimeArtifact(target, previous.Path, serviceID+"-rollback")
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("stage previous service artifact for rollback: %v", err))
	}
	oldStagedDigest, err := digestFile(oldStaged)
	if err != nil || oldStagedDigest != previous.Digest {
		return fail(stderr, application.ExitTarget, "artifact_rollback_unavailable: previous binary no longer matches its registered digest")
	}
	newStaged, err := stageRuntimeArtifact(target, desired.Artifact.Path, serviceID)
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("stage desired service artifact: %v", err))
	}
	newStagedDigest, err := digestFile(newStaged)
	if err != nil || newStagedDigest != desired.Artifact.Digest {
		return fail(stderr, application.ExitTarget, "artifact_digest_mismatch: desired staged binary changed after Runtime Plan")
	}
	network := runtimeNetwork(label)
	previousService := desired
	previousService.Artifact = previous
	oldRequest, err := runtimeContainerRequest(target, previousService, oldStaged, network, label)
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("prepare rollback runtime: %v", err))
	}
	newRequest, err := runtimeContainerRequest(target, desired, newStaged, network, label)
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("prepare desired runtime: %v", err))
	}
	if err := r.Docker.RemoveContainer(ctx, container); err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("remove stopped service container for artifact update: %v", err))
	}
	if err := r.Docker.RunContainer(ctx, newRequest); err != nil {
		return r.rollbackRuntimeService(ctx, serviceID, container, label, options, oldRequest, client, serviceRegistrationMetadata(previousService), fmt.Errorf("start desired service artifact: %w", err), stderr)
	}
	metadata := domain.ServiceRegistrationMetadata{
		ArtifactID: desired.Artifact.ID, ArtifactVersion: desired.Artifact.Version, Digest: desired.Artifact.Digest,
		InstanceID: desired.Artifact.ID, ReplicaID: desired.Artifact.ID,
		ControlEndpoint: desired.ControlEndpoint, PeerEndpoint: desired.PeerEndpoint, RuntimeStatus: "started",
	}
	if _, err := registerRuntimeService(ctx, client, metadata, 60*time.Second); err != nil {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return r.rollbackRuntimeService(ctx, serviceID, container, label, options, oldRequest, client, serviceRegistrationMetadata(previousService), fmt.Errorf("Core registration for desired artifact: %w", err), stderr)
	}
	if err := r.waitForCoreServiceReady(ctx, options, serviceID); err != nil {
		emitRuntimeStartupLogs(ctx, stderr, r.Docker, []string{container})
		return r.rollbackRuntimeService(ctx, serviceID, container, label, options, oldRequest, client, serviceRegistrationMetadata(previousService), fmt.Errorf("Core readiness for desired artifact: %w", err), stderr)
	}
	confirmed, err := client.ServiceMetadata(ctx, serviceID, "status")
	if err != nil {
		return r.rollbackRuntimeService(ctx, serviceID, container, label, options, oldRequest, client, serviceRegistrationMetadata(previousService), fmt.Errorf("verify Core artifact status: %w", err), stderr)
	}
	version, digest, err := serviceArtifactFromStatus(confirmed, serviceID)
	if err != nil || version != desired.Artifact.Version || digest != desired.Artifact.Digest {
		if err == nil {
			err = errors.New("Core still reports a different live artifact")
		}
		return r.rollbackRuntimeService(ctx, serviceID, container, label, options, oldRequest, client, serviceRegistrationMetadata(previousService), fmt.Errorf("verify desired Core artifact provenance: %w", err), stderr)
	}
	return encode(stdout, map[string]any{"schemaVersion": domain.RuntimeEventsSchema, "type": "runtime.completed", "operationId": label, "serviceId": serviceID, "action": "start", "container": container, "containerState": "running", "readiness": "ready", "artifactReplaced": true, "previousArtifactVersion": previousVersion, "previousArtifactDigest": previousDigest, "artifactVersion": desired.Artifact.Version, "artifactDigest": desired.Artifact.Digest})
}

func serviceRegistrationMetadata(service domain.RuntimeServiceArtifact) domain.ServiceRegistrationMetadata {
	return domain.ServiceRegistrationMetadata{
		ArtifactID: service.Artifact.ID, ArtifactVersion: service.Artifact.Version, Digest: service.Artifact.Digest,
		InstanceID: service.Artifact.ID, ReplicaID: service.Artifact.ID,
		ControlEndpoint: service.ControlEndpoint, PeerEndpoint: service.PeerEndpoint, RuntimeStatus: "started",
	}
}

func (r Runner) rollbackRuntimeService(ctx context.Context, serviceID, container, label string, options commandOptions, previousRequest domain.DockerContainerRequest, client domain.CoreClient, previousMetadata domain.ServiceRegistrationMetadata, cause error, stderr io.Writer) int {
	rollbackContext, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = r.Docker.StopContainer(rollbackContext, container)
	_ = r.Docker.RemoveContainer(rollbackContext, container)
	if err := r.Docker.RunContainer(rollbackContext, previousRequest); err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed (%v); rollback failed: %v", cause, err))
	}
	if _, err := registerRuntimeService(rollbackContext, client, previousMetadata, 60*time.Second); err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed (%v); rollback container started but Core registration failed: %v", cause, err))
	}
	if err := r.waitForCoreServiceReady(rollbackContext, options, serviceID); err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed (%v); previous container restarted but Core readiness failed: %v", cause, err))
	}
	status, err := client.ServiceMetadata(rollbackContext, serviceID, "status")
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed (%v); previous container is ready but Core provenance could not be verified: %v", cause, err))
	}
	version, digest, err := serviceArtifactFromStatus(status, serviceID)
	if err != nil || version != previousMetadata.ArtifactVersion || digest != previousMetadata.Digest {
		if err == nil {
			err = errors.New("Core reports a different artifact after rollback")
		}
		return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed (%v); previous container is ready but Core provenance does not match rollback: %v", cause, err))
	}
	return fail(stderr, application.ExitTarget, fmt.Sprintf("service artifact update failed and previous container was restored: %v", cause))
}

func (r Runner) waitForCoreServiceReady(ctx context.Context, options commandOptions, serviceID string) error {
	if r.CoreFactory == nil {
		return errors.New("Core client is not configured")
	}
	connection, err := r.credentials(ctx, options)
	if err != nil {
		return err
	}
	client, err := r.CoreFactory(connection)
	if err != nil {
		return err
	}

	const timeout = 30 * time.Second
	const interval = 250 * time.Millisecond
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		body, statusErr := client.ServiceMetadata(waitContext, serviceID, "status")
		if statusErr == nil {
			ready, parseErr := serviceStatusReady(body, serviceID, serviceID)
			if parseErr != nil {
				return parseErr
			}
			if ready {
				return nil
			}
			lastErr = errors.New("Core reports that one or more replicas are not ready")
		} else {
			var responseError domain.CoreHTTPError
			if errors.As(statusErr, &responseError) && responseError.Status < 500 {
				return statusErr
			}
			lastErr = statusErr
		}
		select {
		case <-waitContext.Done():
			if lastErr != nil {
				return fmt.Errorf("readiness confirmation timed out: %w", lastErr)
			}
			return fmt.Errorf("readiness confirmation timed out: %w", waitContext.Err())
		case <-ticker.C:
		}
	}
}

func serviceStatusReady(body json.RawMessage, expectedServiceID, expectedReplicaID string) (bool, error) {
	var status struct {
		InstanceID string `json:"instanceId"`
		Replicas   []struct {
			ReplicaID string `json:"replicaId"`
			Ready     *bool  `json:"ready"`
		} `json:"replicas"`
	}
	if !json.Valid(body) || json.Unmarshal(body, &status) != nil || status.InstanceID != expectedServiceID || len(status.Replicas) == 0 {
		return false, errors.New("Core returned an invalid service status contract")
	}
	foundExpectedReplica := false
	for _, replica := range status.Replicas {
		if replica.ReplicaID == "" || replica.Ready == nil {
			return false, errors.New("Core returned an incomplete replica readiness contract")
		}
		if replica.ReplicaID == expectedReplicaID {
			foundExpectedReplica = true
		}
		if !*replica.Ready {
			return false, nil
		}
	}
	return foundExpectedReplica, nil
}

func validRuntimeServiceID(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || value == "core" || strings.HasPrefix(value, "-") || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func containsRuntimeContainer(names []string, expected string) bool {
	for _, name := range names {
		if name == expected {
			return true
		}
	}
	return false
}

// emitRuntimeStartupLogs preserves bounded, redacted Core and service output
// before the failed-start rollback removes its temporary containers. This keeps
// the normal rollback policy while making SDK registration/startup failures
// diagnosable in Studio, whose typed Runtime Logs action can no longer address
// removed ones.
func emitRuntimeStartupLogs(ctx context.Context, output io.Writer, docker domain.DockerProvider, names []string) {
	if docker == nil || output == nil {
		return
	}
	const maximumStartupLogServices = 8
	count := 0
	for _, name := range names {
		if count >= maximumStartupLogServices {
			continue
		}
		label := "service " + strings.TrimPrefix(name, "liapoldus-")
		if name == coreContainer {
			label = "Core"
		}
		inspect, inspectErr := docker.Inspect(ctx, name)
		if inspectErr == nil {
			if summary, summaryErr := safeDockerInspectSummary(inspect, name); summaryErr == nil {
				if encoded, marshalErr := json.Marshal(summary); marshalErr == nil {
					_, _ = fmt.Fprintf(output, "%s container state: %s\n", label, safeStudioDiagnostic(string(encoded)))
				}
			}
		}
		body, err := docker.Logs(ctx, name)
		if err != nil || len(body) == 0 {
			count++
			continue
		}
		_, _ = fmt.Fprintf(output, "%s startup output: %s\n", label, safeRuntimeLogSummary(body))
		count++
	}
}

func safeRuntimeLogSummary(body []byte) string {
	const maximumRuntimeLogBytes = 2048
	if len(body) > maximumRuntimeLogBytes {
		body = body[len(body)-maximumRuntimeLogBytes:]
	}
	value := strings.ReplaceAll(string(body), "\r\n", " | ")
	value = strings.ReplaceAll(value, "\n", " | ")
	value = strings.ReplaceAll(value, "\r", " | ")
	value = strings.TrimSpace(value)
	return safeStudioDiagnostic(value)
}

func runtimeIdempotencyKey(label, digest string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "runtime-" + label + "-" + strings.TrimPrefix(digest, "sha256:") + "-" + hex.EncodeToString(nonce[:]), nil
}

func registerRuntimeService(ctx context.Context, client domain.CoreClient, metadata domain.ServiceRegistrationMetadata, timeout time.Duration) (json.RawMessage, error) {
	if ctx == nil || client == nil || strings.TrimSpace(metadata.InstanceID) == "" || timeout <= 0 {
		return nil, errors.New("Core client, service identity and timeout are required")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		// Core may need to fetch manifest, schema, identity, and readiness from
		// the Service SDK before it can accept this claim. The SDK read deadline
		// is 15 seconds, so a 5-second CLI attempt cancelled that Core→SDK
		// control-plane work prematurely. Leave headroom below Core client's
		// 30-second HTTP timeout while still bounding one registration attempt.
		attemptTimeout := 25 * time.Second
		if deadline, ok := waitContext.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < attemptTimeout {
				attemptTimeout = remaining
			}
		}
		attemptContext, attemptCancel := context.WithTimeout(waitContext, attemptTimeout)
		body, err := client.RegisterService(attemptContext, metadata)
		attemptCancel()
		if err == nil && json.Valid(body) {
			return body, nil
		}
		if err == nil {
			err = domain.CoreContractError{Operation: "service register", Message: "invalid JSON"}
		}
		lastErr = err
		if !retryableServiceRegistrationError(err) {
			return nil, err
		}
		select {
		case <-waitContext.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, waitContext.Err()
		case <-ticker.C:
		}
	}
}

func retryableServiceRegistrationError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var responseError domain.CoreHTTPError
	return errors.As(err, &responseError) && responseError.Status >= 500
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
	serviceCertificate := filepath.Join(config.TLSDirectory, artifact.ID+".crt")
	serviceKey := filepath.Join(config.TLSDirectory, artifact.ID+".key")
	peerCertificate := filepath.Join(config.TLSDirectory, artifact.ID+"-peer.crt")
	peerKey := filepath.Join(config.TLSDirectory, artifact.ID+"-peer.key")
	crlFile := filepath.Join(config.TLSDirectory, "revoked.pem")
	for _, path := range []string{serviceCertificate, serviceKey, peerCertificate, peerKey, crlFile} {
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
	peerIdentity, err := runtimePeerIdentity(serviceCertificate, serviceKey, peerCertificate, peerKey, artifact.ID)
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
	containerCert := "/liapoldus/tls/service.crt"
	containerKey := "/liapoldus/tls/service.key"
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
		"LIAPOLDUS_SERVICE_ID":              artifact.ID,
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
		// service's health/metrics/peer diagnostic listener.
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
			{Source: serviceCertificate, Target: containerCert, ReadOnly: true},
			{Source: serviceKey, Target: containerKey, ReadOnly: true},
			{Source: peerCertificate, Target: containerPeerCert, ReadOnly: true},
			{Source: peerKey, Target: containerPeerKey, ReadOnly: true},
			{Source: crlFile, Target: containerCRL, ReadOnly: true},
		},
	}
	// Service SDK REST and peer transports stay private to the Docker network.
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
// service-wide URI such as spiffe://liapoldus/service/echo is not sufficient.
func runtimePeerIdentity(serviceCertificate, serviceKey, peerCertificate, peerKey, serviceID string) (string, error) {
	coreIdentity, err := runtimeCertificateIdentity(serviceCertificate, serviceKey)
	if err != nil {
		return "", fmt.Errorf("service Core certificate identity is invalid: %w", err)
	}
	peerIdentity, err := runtimeCertificateIdentity(peerCertificate, peerKey)
	if err != nil {
		return "", fmt.Errorf("service peer certificate identity is invalid: %w", err)
	}
	if coreIdentity != peerIdentity {
		return "", errors.New("service peer certificate identity must match the Core replica certificate identity")
	}
	parsed, err := url.Parse(coreIdentity)
	if err != nil || parsed.Scheme != "spiffe" || parsed.Host != "liapoldus" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("service certificate must contain one Liapoldus SPIFFE replica identity")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 4 || segments[0] != "service" {
		return "", errors.New("service certificate must contain one Liapoldus SPIFFE replica identity")
	}
	instanceID, instanceErr := url.PathUnescape(segments[1])
	replicaID, replicaErr := url.PathUnescape(segments[2])
	incarnationID, incarnationErr := url.PathUnescape(segments[3])
	if instanceErr != nil || replicaErr != nil || incarnationErr != nil || instanceID != serviceID || replicaID != serviceID || incarnationID == "" {
		return "", errors.New("service certificate SPIFFE identity does not match its runtime replica")
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
