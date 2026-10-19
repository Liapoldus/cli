package cli

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/applyqueue"
)

type Runner struct {
	Source          domain.ProjectSource
	Registry        domain.TargetRegistry
	RegistryFactory func(path string) domain.TargetRegistry
	Credentials     domain.CredentialProvider
	CredentialVault domain.CredentialVault
	PromptSecret    func(prompt string) ([]byte, error)
	TokenReader     io.Reader
	CoreState       domain.CoreStateFiles
	CoreFactory     CoreClientFactory
	Snapshots       domain.ProjectSnapshotter
	Artifacts       domain.ArtifactRegistry
	ApplyRecovery   domain.ApplyRecoveryStore
	Docker          domain.DockerProvider
}

type studioEventsContextKey struct{}

var Version = "dev"

type CoreClientFactory func(connection domain.CoreConnection) (domain.CoreClient, error)

func New(source domain.ProjectSource, registry domain.TargetRegistry, registryFactory func(path string) domain.TargetRegistry, credentials domain.CredentialProvider, tokenReader io.Reader, coreState domain.CoreStateFiles, coreFactory CoreClientFactory) Runner {
	vault, _ := credentials.(domain.CredentialVault)
	return Runner{Source: source, Registry: registry, RegistryFactory: registryFactory, Credentials: credentials, CredentialVault: vault, TokenReader: tokenReader, CoreState: coreState, CoreFactory: coreFactory}
}

func (r Runner) Run(args []string, stdout, stderr io.Writer) int {
	return r.RunContext(context.Background(), args, stdout, stderr)
}

func (r Runner) RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	studioMode, cleanArgs := isStudioEventsRequest(args)
	if studioMode {
		ctx = context.WithValue(ctx, studioEventsContextKey{}, true)
		runID := studioRunID()
		studioStdout := &studioEventWriter{output: stdout, runID: runID, kind: "stdout"}
		studioStderr := &studioEventWriter{output: stdout, runID: runID, kind: "stderr"}
		if err := emitStudioEvent(stdout, studioEvent{SchemaVersion: "cli-events/v1", Type: "run.started", RunID: runID}); err != nil {
			return application.ExitOperation
		}
		code := application.ExitOK
		if isCredentialMutation(cleanArgs) {
			code = fail(studioStderr, application.ExitArguments, "credential changes require the CLI interactive terminal and cannot be invoked through Studio")
		} else {
			code = r.runCommand(ctx, cleanArgs, studioStdout, studioStderr)
		}
		if err := studioStdout.Flush(); err != nil {
			return application.ExitOperation
		}
		if err := studioStderr.Flush(); err != nil {
			return application.ExitOperation
		}
		terminalType := "run.completed"
		severity := ""
		if code != application.ExitOK {
			terminalType = "run.failed"
			severity = "error"
		}
		if err := emitStudioEvent(stdout, studioEvent{SchemaVersion: "cli-events/v1", Type: terminalType, RunID: runID, Severity: severity, ExitCode: intPointer(code)}); err != nil {
			return application.ExitOperation
		}
		return code
	}
	return r.runCommand(ctx, args, stdout, stderr)
}

func isCredentialMutation(args []string) bool {
	return len(args) >= 2 && args[0] == "credential" && args[1] != "status"
}

func intPointer(value int) *int { return &value }

func (r Runner) runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "command is required")
	}
	if containsArgument(args, "--retry-failed") && !(len(args) >= 2 && args[0] == "dev" && args[1] == "apply") {
		return fail(stderr, application.ExitArguments, "--retry-failed is only valid with dev apply")
	}
	switch args[0] {
	case "dev":
		return r.development(ctx, args[1:], stdout, stderr)
	case "version":
		return encode(stdout, map[string]string{"version": Version})
	case "target":
		return r.target(ctx, args[1:], stdout, stderr)
	case "credential":
		return r.credential(ctx, args[1:], stdout, stderr)
	case "service":
		return r.service(ctx, args[1:], stdout, stderr)
	case "runtime":
		return r.runtimePlan(ctx, args[1:], stdout, stderr)
	case "core":
		return r.core(ctx, args[1:], stdout, stderr)
	case "operation":
		return r.operation(ctx, args[1:], stdout, stderr)
	case "observe":
		if len(args) >= 2 && args[1] == "traffic" {
			return r.observeTraffic(ctx, args[2:], stdout, stderr)
		}
	case "status":
		return r.status(ctx, args[1:], stdout, stderr)
	case "project":
		if len(args) >= 2 && args[1] == "inspect" {
			return r.inspect(ctx, args[2:], stdout, stderr)
		}
	case "validate":
		return r.build(ctx, args[1:], false, stdout, stderr)
	case "bundle":
		if len(args) >= 2 {
			switch args[1] {
			case "build":
				return r.build(ctx, args[2:], true, stdout, stderr)
			case "inspect":
				return inspectBundle(args[2:], stdout, stderr)
			}
		}
	case "doctor":
		return r.doctor(ctx, args[1:], stdout, stderr)
	case "plan":
		return r.deploy(ctx, args[1:], false, stdout, stderr)
	case "apply":
		return r.deploy(ctx, args[1:], true, stdout, stderr)
	}
	return fail(stderr, application.ExitArguments, "unsupported command")
}

func containsArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

const maxTrafficReportBytes = 8 << 20

func (r Runner) observeTraffic(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	if target.TrafficReportPath == "" {
		return fail(stderr, application.ExitTarget, "target has no trafficReportPath")
	}
	path, relative, err := projectScopedReportPath(options.project, target.TrafficReportPath)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return fail(stderr, application.ExitOperation, err.Error())
	}
	body, err := readTrafficReport(path)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	var report struct {
		SchemaVersion string          `json:"schemaVersion"`
		ProjectID     string          `json:"projectId"`
		Target        string          `json:"target"`
		Traffic       json.RawMessage `json:"traffic"`
	}
	if err := json.Unmarshal(body, &report); err != nil || report.SchemaVersion != "studio-reports/v1" || strings.TrimSpace(report.ProjectID) == "" || strings.TrimSpace(report.Target) == "" || report.Target != target.Name || !json.Valid(report.Traffic) {
		return fail(stderr, application.ExitValidation, "invalid studio traffic report")
	}
	var traffic []json.RawMessage
	if err := json.Unmarshal(report.Traffic, &traffic); err != nil || traffic == nil {
		return fail(stderr, application.ExitValidation, "invalid studio traffic report")
	}
	if r.Source == nil {
		return fail(stderr, application.ExitValidation, "project source is not configured")
	}
	expectedProjectID, err := r.Source.ProjectID(ctx, options.project, "HEAD")
	if err != nil || expectedProjectID != report.ProjectID {
		return fail(stderr, application.ExitValidation, "traffic report project does not match the opened project")
	}
	digest := sha256.Sum256(body)
	return encode(stdout, map[string]any{
		"schemaVersion": "studio-reports/v1",
		"projectId":     report.ProjectID,
		"target":        target.Name,
		"reportPath":    relative,
		"digest":        fmt.Sprintf("sha256:%x", digest),
	})
}

func projectScopedReportPath(root, configured string) (string, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve project root: %w", err)
	}
	clean := filepath.Clean(configured)
	if filepath.IsAbs(configured) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", errors.New("traffic report path must remain inside the project")
	}
	candidate := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve traffic report: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("traffic report path escapes the project")
	}
	return resolved, filepath.ToSlash(relative), nil
}

func readTrafficReport(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read traffic report: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTrafficReportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read traffic report: %w", err)
	}
	if len(body) > maxTrafficReportBytes {
		return nil, errors.New("traffic report exceeds the safety limit")
	}
	return body, nil
}

func inspectBundle(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return fail(stderr, application.ExitArguments, "bundle file is required")
	}
	body, err := os.ReadFile(args[0])
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	var bundle domain.Bundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		return fail(stderr, application.ExitValidation, fmt.Sprintf("invalid bundle: %v", err))
	}
	if err := bundle.Validate(); err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	return encode(stdout, map[string]any{
		"schemaVersion": bundle.SchemaVersion,
		"revision":      bundle.Revision,
		"target":        bundle.Target,
		"digest":        bundle.Digest,
		"files":         len(bundle.Files),
		"services":      len(bundle.Services),
	})
}

func (r Runner) doctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options, err := parseOptions(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.revision == "" {
		return fail(stderr, application.ExitArguments, "--revision is required")
	}
	bundle, err := (application.BundleService{Source: r.Source}).Build(ctx, options.project, options.revision, options.target)
	if err != nil {
		return fail(stderr, revisionCode(err), err.Error())
	}
	result := map[string]any{"project": options.project, "revision": bundle.Revision, "digest": bundle.Digest, "files": len(bundle.Files), "services": len(bundle.Services)}
	if options.target != "" {
		connection, err := r.credentials(ctx, options)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		client, err := r.CoreFactory(connection)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		status, err := client.Health(ctx)
		result["coreStatus"] = status
		result["coreReachable"] = err == nil && status >= 200 && status < 300
		if !result["coreReachable"].(bool) {
			_ = encode(stdout, result)
			return application.ExitTarget
		}
	}
	return encode(stdout, result)
}

func (r Runner) target(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "target subcommand is required")
	}
	registry := r.Registry
	if path, ok := optionValue(args, "--targets"); ok && r.RegistryFactory != nil {
		registry = r.RegistryFactory(path)
	}
	switch args[0] {
	case "list":
		items, err := registry.List()
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		summaries := make([]targetSummary, 0, len(items))
		for _, item := range items {
			summaries = append(summaries, summarizeTarget(item))
		}
		return encode(stdout, summaries)
	case "inspect":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "target name is required")
		}
		item, err := registry.Find(args[1])
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, summarizeTarget(item))
	case "add":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "target name is required")
		}
		profile, ok := optionValue(args[2:], "--profile")
		if !ok || profile == "" {
			return fail(stderr, application.ExitArguments, "--profile is required")
		}
		body, err := os.ReadFile(profile)
		if err != nil {
			return fail(stderr, application.ExitArguments, err.Error())
		}
		var item domain.Target
		if err := json.Unmarshal(body, &item); err != nil {
			return fail(stderr, application.ExitArguments, fmt.Sprintf("invalid target profile: %v", err))
		}
		item = resolveProfilePaths(item, filepath.Dir(profile))
		item.Name = args[1]
		if err := registry.Add(item); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, summarizeTarget(item))
	case "test":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "target name is required")
		}
		item, err := registry.Find(args[1])
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return r.testTarget(ctx, item, stdout, stderr)
	default:
		return fail(stderr, application.ExitArguments, "unsupported target subcommand")
	}
}

// Target profiles are portable documents. Relative file references are
// resolved beside the selected profile before the item enters the global CLI
// registry; otherwise the registry's own directory would silently become the
// base and a valid Studio-selected profile would fail later at target test.
func resolveProfilePaths(item domain.Target, base string) domain.Target {
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Clean(filepath.Join(base, value))
	}
	item.CoreBinary = resolve(item.CoreBinary)
	item.StatePath = resolve(item.StatePath)
	item.PIDPath = resolve(item.PIDPath)
	item.LogPath = resolve(item.LogPath)
	item.CAFile = resolve(item.CAFile)
	item.ClientCertFile = resolve(item.ClientCertFile)
	item.ClientKeyFile = resolve(item.ClientKeyFile)
	item.Bootstrap.ManagementCertificate = resolve(item.Bootstrap.ManagementCertificate)
	item.Bootstrap.ManagementKey = resolve(item.Bootstrap.ManagementKey)
	item.Bootstrap.ManagementClientCA = resolve(item.Bootstrap.ManagementClientCA)
	item.Bootstrap.ControlCertificate = resolve(item.Bootstrap.ControlCertificate)
	item.Bootstrap.ControlKey = resolve(item.Bootstrap.ControlKey)
	item.Bootstrap.ReplicaClientCA = resolve(item.Bootstrap.ReplicaClientCA)
	item.Bootstrap.ReplicaServerCA = resolve(item.Bootstrap.ReplicaServerCA)
	item.Bootstrap.SecretRoot = resolve(item.Bootstrap.SecretRoot)
	return item
}

func (r Runner) testTarget(ctx context.Context, target domain.Target, stdout, stderr io.Writer) int {
	if target.Endpoint == "" {
		return fail(stderr, application.ExitTarget, "target endpoint is required")
	}
	client, err := r.CoreFactory(domain.CoreConnection{
		Endpoint: target.Endpoint, CAFile: target.CAFile, ClientCertFile: target.ClientCertFile,
		ClientKeyFile: target.ClientKeyFile, ServerName: target.ServerName,
	})
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	status, err := client.Health(ctx)
	reachable := err == nil && status >= 200 && status < 300
	if encode(stdout, map[string]any{"target": target.Name, "reachable": reachable, "status": status}) != application.ExitOK {
		return application.ExitOperation
	}
	if !reachable {
		return application.ExitTarget
	}
	return application.ExitOK
}

func (r Runner) status(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options, err := parseOptions(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target == "" {
		return fail(stderr, application.ExitArguments, "--target is required")
	}
	registry := r.registry(options.targets)
	target, err := registry.Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return r.testTarget(ctx, target, stdout, stderr)
}

func (r Runner) operation(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "get" && args[0] != "watch") {
		return fail(stderr, application.ExitArguments, "operation get|watch is required")
	}
	if len(args) < 2 {
		return fail(stderr, application.ExitArguments, "operation ID is required")
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
	client, err := r.CoreFactory(connection)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	service := application.OperationService{Core: client}
	var result domain.OperationResponse
	if args[0] == "get" {
		result, err = service.Get(ctx, args[1])
	} else {
		result, err = service.WatchObserved(ctx, args[1], 250*time.Millisecond, 60*time.Second, studioOperationObserver(ctx, stdout, options.target))
	}
	if result.ID != "" {
		_ = encode(stdout, operationSummary{
			OperationID: result.ID, Target: options.target, State: result.State,
			ReplicaOutcomes: result.ReplicaOutcomes,
		})
	}
	if err != nil {
		var stateError application.OperationStateError
		if errors.As(err, &stateError) && stateError.State == "degraded" {
			return fail(stderr, application.ExitDegraded, err.Error())
		}
		var targetError application.TargetError
		if errors.As(err, &targetError) {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		var httpError domain.CoreHTTPError
		if errors.As(err, &httpError) {
			return fail(stderr, httpCode(httpError.Status), err.Error())
		}
		var contractError domain.CoreContractError
		if errors.As(err, &contractError) {
			return fail(stderr, application.ExitValidation, err.Error())
		}
		return fail(stderr, application.ExitOperation, err.Error())
	}
	return application.ExitOK
}

func (r Runner) core(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "core subcommand is required")
	}
	if args[0] == "binary" {
		return r.coreBinary(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "settings" {
		return r.coreSettings(ctx, args[1:], stdout, stderr)
	}
	optionArgs := args[1:]
	if args[0] == "database" && len(args) >= 2 {
		optionArgs = args[2:]
	}
	options, err := parseOptions(optionArgs)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target == "" {
		return fail(stderr, application.ExitArguments, "--target is required")
	}
	registry := r.registry(options.targets)
	target, err := registry.Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	switch args[0] {
	case "start":
		return r.coreDockerCommand(ctx, "start", target, stdout, stderr)
	case "stop":
		return r.coreDockerCommand(ctx, "stop", target, stdout, stderr)
	case "restart":
		return r.coreDockerCommand(ctx, "restart", target, stdout, stderr)
	case "status":
		return r.coreDockerCommand(ctx, "status", target, stdout, stderr)
	case "logs":
		return r.coreDockerCommand(ctx, "logs", target, stdout, stderr)
	case "database":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "database action is required")
		}
		if r.CoreState == nil {
			return fail(stderr, application.ExitTarget, "Core state file adapter is not configured")
		}
		if r.Docker == nil {
			return fail(stderr, application.ExitTarget, "Docker runtime is not configured")
		}
		state, statusErr := r.dockerCoreStatus(ctx, target, runtimeLabel("target", target.Name))
		if statusErr != nil {
			return fail(stderr, application.ExitTarget, statusErr.Error())
		}
		if running, _ := state["running"].(bool); running {
			return fail(stderr, application.ExitTarget, "stop Docker Core before database backup or restore")
		}
		switch args[1] {
		case "backup":
			output, ok := optionValue(args[2:], "--output")
			if !ok || output == "" {
				return fail(stderr, application.ExitArguments, "--output is required")
			}
			if err := r.CoreState.Backup(target, output); err != nil {
				return fail(stderr, application.ExitTarget, err.Error())
			}
			return encode(stdout, map[string]any{"target": target.Name, "backup": output})
		case "restore":
			input, ok := optionValue(args[2:], "--input")
			if !ok || input == "" {
				return fail(stderr, application.ExitArguments, "--input is required")
			}
			if err := r.CoreState.Restore(target, input); err != nil {
				return fail(stderr, application.ExitTarget, err.Error())
			}
			return encode(stdout, map[string]any{"target": target.Name, "restored": target.StatePath})
		default:
			return fail(stderr, application.ExitArguments, "unsupported database action")
		}
	default:
		return fail(stderr, application.ExitArguments, "unsupported core subcommand")
	}
}

func (r Runner) coreBinary(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "update") {
		return fail(stderr, application.ExitArguments, "core binary inspect|update is required")
	}
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return fail(stderr, application.ExitOperation, err.Error())
	}
	if args[0] == "update" && (options.target != "local" || !options.confirm || options.digest == "") {
		return fail(stderr, application.ExitArguments, "core binary update requires --target local, exact --digest, and --confirm")
	}
	platform, digest, size, err := inspectCoreBinary(options.path)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	if args[0] == "inspect" {
		return encode(stdout, map[string]any{"digest": digest, "platform": platform, "sizeBytes": size})
	}
	if options.digest != digest {
		return fail(stderr, application.ExitArguments, "Core binary digest changed after review")
	}
	registry := r.registry(options.targets)
	target, err := registry.Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if target.Transport != domain.TransportLocalProcess || target.RuntimePlatform != platform {
		return fail(stderr, application.ExitValidation, "Core binary platform does not match the local target")
	}
	previousTarget := target
	target.CoreBinary = filepath.Clean(options.path)
	target.CoreBinaryDigest = digest
	started, err := r.startDockerCore(ctx, target, runtimeLabel("target", target.Name), nil)
	if err != nil {
		return fail(stderr, application.ExitTarget, fmt.Sprintf("Core binary update failed; previous target configuration was preserved. The existing Core may be stopped; run `liapoldus core start --target %s` to recover it: %v", previousTarget.Name, err))
	}
	if err := registry.Add(target); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopErr := r.Docker.StopContainer(cleanupCtx, coreContainer)
		removeErr := r.Docker.RemoveContainer(cleanupCtx, coreContainer)
		message := fmt.Sprintf("Core binary was started, but target registry could not commit the update; the previous target configuration remains authoritative: %v", err)
		if stopErr != nil || removeErr != nil {
			message += fmt.Sprintf("; candidate cleanup incomplete (stop: %v, remove: %v)", stopErr, removeErr)
		} else {
			message += "; candidate Core container was stopped and removed"
		}
		return fail(stderr, application.ExitTarget, message)
	}
	return encode(stdout, map[string]any{"configured": true, "digest": digest, "platform": platform, "target": target.Name, "running": true, "created": started.Created, "updated": started.Updated})
}

func inspectCoreBinary(path string) (string, string, int64, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || strings.ContainsAny(path, "\x00\r\n") {
		return "", "", 0, errors.New("an absolute Core binary path is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", "", 0, errors.New("core binary must be an existing executable regular file")
	}
	file, err := elf.Open(path)
	if err != nil {
		return "", "", 0, errors.New("core binary must be a Linux ELF executable")
	}
	defer file.Close()
	platform := ""
	switch file.Machine {
	case elf.EM_X86_64:
		platform = "linux/amd64"
	case elf.EM_AARCH64:
		platform = "linux/arm64"
	default:
		return "", "", 0, errors.New("core binary architecture is unsupported")
	}
	if file.Class != elf.ELFCLASS64 || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return "", "", 0, errors.New("core binary must be a 64-bit Linux executable")
	}
	digest, err := digestFile(path)
	if err != nil {
		return "", "", 0, errors.New("core binary digest could not be calculated")
	}
	return platform, digest, info.Size(), nil
}

func (r Runner) inspect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	project, err := projectOption(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	revision, err := r.Source.ResolveRevision(ctx, project, "HEAD")
	if err != nil {
		return fail(stderr, application.ExitRevision, err.Error())
	}
	files, err := r.Source.Files(ctx, project, revision)
	if err != nil {
		return fail(stderr, application.ExitValidation, err.Error())
	}
	return encode(stdout, map[string]any{"project": project, "revision": revision, "files": files})
}

func (r Runner) build(ctx context.Context, args []string, writeFile bool, stdout, stderr io.Writer) int {
	options, err := parseOptions(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.revision == "" {
		return fail(stderr, application.ExitArguments, "--revision is required")
	}
	bundle, err := (application.BundleService{Source: r.Source}).Build(ctx, options.project, options.revision, options.target)
	if err != nil {
		return fail(stderr, revisionCode(err), err.Error())
	}
	if writeFile && options.output != "" {
		body, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return fail(stderr, application.ExitValidation, err.Error())
		}
		if err := os.WriteFile(options.output, append(body, '\n'), 0o600); err != nil {
			return fail(stderr, application.ExitValidation, err.Error())
		}
	}
	return encode(stdout, bundle)
}

func (r Runner) deploy(ctx context.Context, args []string, apply bool, stdout, stderr io.Writer) int {
	options, err := parseOptions(args)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target == "" {
		return fail(stderr, application.ExitArguments, "--target is required")
	}
	if apply && !options.confirm {
		return fail(stderr, application.ExitArguments, "apply requires --confirm")
	}
	approval, err := r.approval(options, apply)
	if err != nil {
		return fail(stderr, application.ExitRevision, err.Error())
	}
	connection, err := r.credentials(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	client, err := r.CoreFactory(connection)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	service := application.DeploymentService{Source: r.Source, Core: client, Bundle: application.BundleService{Source: r.Source}}
	if apply {
		bundle, result, err := service.Apply(ctx, options.project, options.revision, options.target, options.expectedGeneration, approval, options.confirm, options.wait, options.idempotencyKey)
		if err != nil {
			return fail(stderr, deploymentCode(err), err.Error())
		}
		return encode(stdout, map[string]any{"accepted": true, "revision": bundle.Revision, "digest": bundle.Digest, "response": json.RawMessage(result)})
	}
	bundle, result, err := service.Plan(ctx, options.project, options.revision, options.target, options.expectedGeneration)
	if err != nil {
		return fail(stderr, application.ExitOperation, err.Error())
	}
	return encode(stdout, map[string]any{"revision": bundle.Revision, "digest": bundle.Digest, "response": json.RawMessage(result)})
}

func (r Runner) development(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply") {
		return fail(stderr, application.ExitArguments, "usage: liapoldus dev plan|apply --project <path> --target local [--confirm]")
	}
	apply := args[0] == "apply"
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.revision != "" {
		return fail(stderr, application.ExitArguments, "dev always captures the current working tree; --revision is not accepted")
	}
	if options.target != "local" {
		return fail(stderr, application.ExitArguments, "development working-tree operations require --target local")
	}
	if apply && !options.confirm {
		return fail(stderr, application.ExitArguments, "development apply requires --confirm")
	}
	if options.retryFailed && !apply {
		return fail(stderr, application.ExitArguments, "--retry-failed is only valid with dev apply")
	}
	connection, err := r.credentials(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if r.CoreFactory == nil {
		return fail(stderr, application.ExitTarget, "Core Management API client is not configured")
	}
	client, err := r.CoreFactory(connection)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	recovery := r.ApplyRecovery
	if recovery == nil {
		recovery, err = applyqueue.Default()
		if err != nil {
			return fail(stderr, application.ExitOperation, err.Error())
		}
	}
	var secretTarget domain.Target
	if registry := r.registry(options.targets); registry != nil {
		secretTarget, err = registry.Find(options.target)
		if err != nil && !errors.Is(err, domain.ErrTargetNotConfigured) {
			return fail(stderr, application.ExitTarget, err.Error())
		}
	}
	service := application.DevelopmentDeploymentService{
		Source: r.Source, Core: client, Snapshots: r.Snapshots,
		Secrets:  developmentServiceSecretRuntime{runner: r, target: secretTarget},
		Recovery: recovery, RecoveryTargetKey: applyqueue.TargetKey(options.target, connection.Endpoint),
		OnOperationState: studioOperationObserver(ctx, stdout, "local"),
	}
	if apply {
		bundle, response, err := service.Apply(ctx, options.project, options.target, options.expectedGeneration, options.confirm, options.wait, options.retryFailed, options.idempotencyKey)
		if err != nil {
			if len(response.CoreResponse) > 0 {
				_ = encode(stdout, summarizeDevelopmentApply(bundle, response))
			}
			return fail(stderr, deploymentCode(err), err.Error())
		}
		return encode(stdout, summarizeDevelopmentApply(bundle, response))
	}
	bundle, response, err := service.Plan(ctx, options.project, options.target, options.expectedGeneration)
	if err != nil {
		return fail(stderr, deploymentCode(err), err.Error())
	}
	return encode(stdout, map[string]any{"revision": bundle.Revision, "digest": bundle.Digest, "response": json.RawMessage(response)})
}

func studioEventsEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(studioEventsContextKey{}).(bool)
	return enabled
}

func studioOperationObserver(ctx context.Context, output io.Writer, target string) func(string, string) {
	if !studioEventsEnabled(ctx) {
		return nil
	}
	return func(operationID, state string) {
		// Keep the renderer contract limited to safe Core operation metadata.
		_ = encode(output, map[string]string{"operationId": operationID, "target": target, "state": state})
	}
}

func (r Runner) credentials(ctx context.Context, options commandOptions) (domain.CoreConnection, error) {
	connection := domain.CoreConnection{Endpoint: options.endpoint, CAFile: options.caFile, ClientCertFile: options.clientCertFile, ClientKeyFile: options.clientKeyFile, ServerName: options.serverName}
	if options.tokenStdin {
		if r.TokenReader == nil {
			return domain.CoreConnection{}, errors.New("token stdin is not configured")
		}
		body, err := io.ReadAll(io.LimitReader(r.TokenReader, 4096))
		if err != nil {
			return domain.CoreConnection{}, fmt.Errorf("read token stdin: %w", err)
		}
		connection.Token = strings.TrimSpace(string(body))
	}
	if connection.Endpoint == "" {
		connection.Endpoint = os.Getenv("LIAPOLDUS_TARGET_" + envName(options.target) + "_ENDPOINT")
	}
	registry := r.registry(options.targets)
	if registry != nil {
		item, err := registry.Find(options.target)
		if err != nil && !errors.Is(err, domain.ErrTargetNotConfigured) {
			return domain.CoreConnection{}, fmt.Errorf("resolve target: %w", err)
		}
		if err == nil {
			if connection.Endpoint == "" {
				connection.Endpoint = item.Endpoint
			}
			if connection.Token == "" && item.CredentialRef != "" {
				if r.Credentials == nil {
					return domain.CoreConnection{}, domain.ErrCredentialStore
				}
				connection.Token, err = r.Credentials.Resolve(ctx, item.CredentialRef)
				if err != nil {
					return domain.CoreConnection{}, fmt.Errorf("resolve target credential: %w", err)
				}
			}
			if connection.CAFile == "" {
				connection.CAFile = item.CAFile
			}
			if connection.ClientCertFile == "" {
				connection.ClientCertFile = item.ClientCertFile
			}
			if connection.ClientKeyFile == "" {
				connection.ClientKeyFile = item.ClientKeyFile
			}
			if connection.ServerName == "" {
				connection.ServerName = item.ServerName
			}
		}
	}
	if connection.Endpoint == "" {
		return domain.CoreConnection{}, errors.New("--endpoint or target endpoint environment is required")
	}
	if connection.Token == "" {
		if options.tokenStdin {
			return domain.CoreConnection{}, errors.New("token stdin did not contain a credential")
		}
		return domain.CoreConnection{}, errors.New("target management credential is not configured; run liapoldus credential set --target " + options.target)
	}
	return connection, nil
}

type commandOptions struct {
	project, revision, target, output, endpoint, idempotencyKey, targets            string
	id, version, platform, path, digest, command, controlEndpoint, peerEndpoint     string
	service, resource, requestFile                                                  string
	page, section, action, cursor, artifactFile, contentType, metadataFile, ifMatch string
	surfaceDigest                                                                   string
	purpose, label, reference                                                       string
	expectedGeneration                                                              *int64
	approvalEvidence                                                                string
	caFile, clientCertFile, clientKeyFile, serverName                               string
	confirm, wait, tokenStdin, secretStdin, replace, retryFailed                    bool
	projectExplicit                                                                 bool
}

func parseOptions(args []string) (commandOptions, error) {
	options := commandOptions{project: "."}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--confirm" {
			options.confirm = true
			continue
		}
		if arg == "--replace" {
			options.replace = true
			continue
		}
		if arg == "--wait" {
			options.wait = true
			continue
		}
		if arg == "--retry-failed" {
			options.retryFailed = true
			continue
		}
		if arg == "--token-stdin" {
			options.tokenStdin = true
			continue
		}
		if arg == "--secret-stdin" {
			options.secretStdin = true
			continue
		}
		if !strings.HasPrefix(arg, "--") || index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
			return commandOptions{}, fmt.Errorf("invalid argument %q", arg)
		}
		value := args[index+1]
		index++
		switch arg {
		case "--project":
			options.project = value
			options.projectExplicit = true
		case "--revision":
			options.revision = value
		case "--target":
			options.target = value
		case "--output":
			options.output = value
		case "--request-file":
			options.requestFile = value
		case "--endpoint":
			options.endpoint = value
		case "--idempotency-key":
			options.idempotencyKey = value
		case "--surface-digest":
			options.surfaceDigest = value
		case "--targets":
			options.targets = value
		case "--expected-generation":
			generation, err := strconv.ParseInt(value, 10, 64)
			if err != nil || generation < 0 {
				return commandOptions{}, fmt.Errorf("--expected-generation must be a non-negative integer")
			}
			options.expectedGeneration = &generation
		case "--approval-evidence":
			options.approvalEvidence = value
		case "--ca-file":
			options.caFile = value
		case "--client-cert":
			options.clientCertFile = value
		case "--client-key":
			options.clientKeyFile = value
		case "--server-name":
			options.serverName = value
		case "--id":
			options.id = value
		case "--version":
			options.version = value
		case "--platform":
			options.platform = value
		case "--path":
			options.path = value
		case "--digest":
			options.digest = value
		case "--command":
			options.command = value
		case "--control-endpoint":
			options.controlEndpoint = value
		case "--peer-endpoint":
			options.peerEndpoint = value
		case "--service":
			options.service = value
		case "--purpose":
			options.purpose = value
		case "--label":
			options.label = value
		case "--reference":
			options.reference = value
		case "--resource":
			options.resource = value
		case "--page":
			options.page = value
		case "--section":
			options.section = value
		case "--action":
			options.action = value
		case "--cursor":
			options.cursor = value
		case "--artifact-file":
			options.artifactFile = value
		case "--content-type":
			options.contentType = value
		case "--metadata-file":
			options.metadataFile = value
		case "--if-match":
			options.ifMatch = value
		default:
			return commandOptions{}, fmt.Errorf("unsupported argument %q", arg)
		}
	}
	absolute, err := filepath.Abs(options.project)
	if err != nil {
		return commandOptions{}, err
	}
	options.project = absolute
	return options, nil
}

func optionValue(args []string, name string) (string, bool) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1], true
		}
	}
	return "", false
}

func projectOption(args []string) (string, error) {
	options, err := parseOptions(args)
	if err != nil {
		return "", err
	}
	return options.project, nil
}

func (r Runner) registry(path string) domain.TargetRegistry {
	if path != "" && r.RegistryFactory != nil {
		return r.RegistryFactory(path)
	}
	return r.Registry
}

func (r Runner) approval(options commandOptions, apply bool) (*domain.ApprovalEvidence, error) {
	if !apply {
		return nil, nil
	}
	registry := r.registry(options.targets)
	if registry == nil {
		return nil, nil
	}
	target, err := registry.Find(options.target)
	if err != nil {
		if errors.Is(err, domain.ErrTargetNotConfigured) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve target approval policy: %w", err)
	}
	if target.Approval != domain.ApprovalRemoteRequired {
		return nil, nil
	}
	if options.approvalEvidence == "" {
		return nil, errors.New("target requires --approval-evidence")
	}
	body, err := os.ReadFile(options.approvalEvidence)
	if err != nil {
		return nil, fmt.Errorf("read approval evidence: %w", err)
	}
	var evidence domain.ApprovalEvidence
	if err := json.Unmarshal(body, &evidence); err != nil {
		return nil, fmt.Errorf("invalid approval evidence: %w", err)
	}
	return &evidence, nil
}

func envName(value string) string {
	return strings.ToUpper(strings.ReplaceAll(value, "-", "_"))
}

func revisionCode(err error) int {
	if errors.Is(err, domain.ErrInvalidRevision) || errors.Is(err, domain.ErrDirtyWorkingTree) {
		return application.ExitRevision
	}
	return application.ExitValidation
}

func deploymentCode(err error) int {
	if errors.Is(err, domain.ErrInvalidRevision) || errors.Is(err, domain.ErrDirtyWorkingTree) {
		return application.ExitRevision
	}
	var stateError application.OperationStateError
	if errors.As(err, &stateError) && stateError.State == "degraded" {
		return application.ExitDegraded
	}
	var targetError application.TargetError
	if errors.As(err, &targetError) {
		return application.ExitTarget
	}
	var httpError domain.CoreHTTPError
	if errors.As(err, &httpError) {
		return httpCode(httpError.Status)
	}
	var contractError domain.CoreContractError
	if errors.As(err, &contractError) {
		return application.ExitValidation
	}
	return application.ExitOperation
}

func httpCode(status int) int {
	switch status {
	case 401, 403, 408, 429:
		return application.ExitTarget
	case 409:
		return application.ExitConflict
	case 400, 422:
		return application.ExitValidation
	default:
		return application.ExitOperation
	}
}

func encode(writer io.Writer, value any) int {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return application.ExitOperation
	}
	return application.ExitOK
}

func fail(writer io.Writer, code int, message string) int {
	_, _ = fmt.Fprintln(writer, message)
	return code
}
