package cli

import (
	"context"
	"crypto/sha256"
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
)

type Runner struct {
	Source          domain.ProjectSource
	Registry        domain.TargetRegistry
	RegistryFactory func(path string) domain.TargetRegistry
	Credentials     domain.CredentialProvider
	TokenReader     io.Reader
	LocalCore       domain.LocalCore
	CoreFactory     CoreClientFactory
}

var Version = "dev"

type CoreClientFactory func(connection domain.CoreConnection) (domain.CoreClient, error)

func New(source domain.ProjectSource, registry domain.TargetRegistry, registryFactory func(path string) domain.TargetRegistry, credentials domain.CredentialProvider, tokenReader io.Reader, localCore domain.LocalCore, coreFactory CoreClientFactory) Runner {
	return Runner{Source: source, Registry: registry, RegistryFactory: registryFactory, Credentials: credentials, TokenReader: tokenReader, LocalCore: localCore, CoreFactory: coreFactory}
}

func (r Runner) Run(args []string, stdout, stderr io.Writer) int {
	return r.RunContext(context.Background(), args, stdout, stderr)
}

func (r Runner) RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	studioMode, cleanArgs := isStudioEventsRequest(args)
	if studioMode {
		runID := studioRunID()
		studioStdout := &studioEventWriter{output: stdout, runID: runID, kind: "stdout"}
		studioStderr := &studioEventWriter{output: stdout, runID: runID, kind: "stderr"}
		if err := emitStudioEvent(stdout, studioEvent{SchemaVersion: "cli-events/v1", Type: "run.started", RunID: runID}); err != nil {
			return application.ExitOperation
		}
		code := r.runCommand(ctx, cleanArgs, studioStdout, studioStderr)
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

func intPointer(value int) *int { return &value }

func (r Runner) runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "command is required")
	}
	switch args[0] {
	case "version":
		return encode(stdout, map[string]string{"version": Version})
	case "target":
		return r.target(ctx, args[1:], stdout, stderr)
	case "core":
		return r.core(args[1:], stdout, stderr)
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
		return encode(stdout, items)
	case "inspect":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "target name is required")
		}
		item, err := registry.Find(args[1])
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, item)
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
		item.Name = args[1]
		if err := registry.Add(item); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, item)
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
	if encode(stdout, map[string]any{"target": target.Name, "endpoint": target.Endpoint, "reachable": reachable, "status": status}) != application.ExitOK {
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
		result, err = service.Watch(ctx, args[1], 250*time.Millisecond, 60*time.Second)
	}
	if len(result.Raw) > 0 {
		_ = json.NewEncoder(stdout).Encode(json.RawMessage(result.Raw))
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

func (r Runner) core(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "core subcommand is required")
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
		value, err := r.LocalCore.Start(target)
		return r.localResult(value, err, stdout, stderr)
	case "stop":
		value, err := r.LocalCore.Stop(target)
		return r.localResult(value, err, stdout, stderr)
	case "restart":
		if _, err := r.LocalCore.Stop(target); err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		value, err := r.LocalCore.Start(target)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "restarted": true, "result": value})
	case "status":
		status, err := r.LocalCore.Status(target)
		code := application.ExitOK
		if err != nil {
			code = application.ExitTarget
		}
		if encode(stdout, status) != application.ExitOK {
			return application.ExitOperation
		}
		return code
	case "logs":
		body, err := r.LocalCore.Logs(target)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		_, _ = stdout.Write(body)
		return application.ExitOK
	case "database":
		if len(args) < 2 {
			return fail(stderr, application.ExitArguments, "database action is required")
		}
		switch args[1] {
		case "backup":
			output, ok := optionValue(args[2:], "--output")
			if !ok || output == "" {
				return fail(stderr, application.ExitArguments, "--output is required")
			}
			if err := r.LocalCore.Backup(target, output); err != nil {
				return fail(stderr, application.ExitTarget, err.Error())
			}
			return encode(stdout, map[string]any{"target": target.Name, "backup": output})
		case "restore":
			input, ok := optionValue(args[2:], "--input")
			if !ok || input == "" {
				return fail(stderr, application.ExitArguments, "--input is required")
			}
			if err := r.LocalCore.Restore(target, input); err != nil {
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

func (r Runner) localResult(value any, err error, stdout, stderr io.Writer) int {
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, value)
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

func (r Runner) credentials(ctx context.Context, options commandOptions) (domain.CoreConnection, error) {
	if options.tokenStdin && options.token != "" {
		return domain.CoreConnection{}, errors.New("--token and --token-stdin cannot be used together")
	}
	connection := domain.CoreConnection{Endpoint: options.endpoint, Token: options.token, CAFile: options.caFile, ClientCertFile: options.clientCertFile, ClientKeyFile: options.clientKeyFile, ServerName: options.serverName}
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
	if connection.Token == "" && r.Credentials != nil {
		value, err := r.Credentials.Resolve(ctx, "LIAPOLDUS_TARGET_"+envName(options.target)+"_TOKEN")
		if err == nil {
			connection.Token = value
		}
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
			if connection.Token == "" && item.TokenEnv != "" {
				if r.Credentials == nil {
					return domain.CoreConnection{}, errors.New("credential provider is not configured")
				}
				connection.Token, err = r.Credentials.Resolve(ctx, item.TokenEnv)
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
		return domain.CoreConnection{}, errors.New("--token or target token environment is required")
	}
	return connection, nil
}

type commandOptions struct {
	project, revision, target, output, endpoint, token, idempotencyKey, targets string
	expectedGeneration                                                          *int64
	approvalEvidence                                                            string
	caFile, clientCertFile, clientKeyFile, serverName                           string
	confirm, wait, tokenStdin                                                   bool
}

func parseOptions(args []string) (commandOptions, error) {
	options := commandOptions{project: "."}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--confirm" {
			options.confirm = true
			continue
		}
		if arg == "--wait" {
			options.wait = true
			continue
		}
		if arg == "--token-stdin" {
			options.tokenStdin = true
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
		case "--revision":
			options.revision = value
		case "--target":
			options.target = value
		case "--output":
			options.output = value
		case "--endpoint":
			options.endpoint = value
		case "--token":
			options.token = value
		case "--idempotency-key":
			options.idempotencyKey = value
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
