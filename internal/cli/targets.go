package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Target struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Transport   string `json:"transport"`
	Endpoint    string `json:"endpoint,omitempty"`
	TokenEnv    string `json:"tokenEnv,omitempty"`
	CoreBinary  string `json:"coreBinary,omitempty"`
	StatePath   string `json:"statePath,omitempty"`
	PIDPath     string `json:"pidPath,omitempty"`
	LogPath     string `json:"logPath,omitempty"`
}

type targetFile struct {
	Targets []Target `json:"targets"`
}

func targetCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, ExitArguments, "target subcommand is required")
	}
	targets, err := loadTargets(targetsPath(args))
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	switch args[0] {
	case "list":
		return encode(stdout, targets)
	case "inspect":
		if len(args) < 2 {
			return fail(stderr, ExitArguments, "target name is required")
		}
		item, err := findTarget(targets, args[1])
		if err != nil {
			return fail(stderr, ExitTarget, err.Error())
		}
		return encode(stdout, item)
	default:
		return fail(stderr, ExitArguments, "unsupported target subcommand")
	}
}

func coreCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, ExitArguments, "core subcommand is required")
	}
	path := targetsPath(args)
	targets, err := loadTargets(path)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if len(args) < 2 {
		return fail(stderr, ExitArguments, "--target is required")
	}
	name, err := flagValue(args, "--target")
	if err != nil {
		return fail(stderr, ExitArguments, err.Error())
	}
	item, err := findTarget(targets, name)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	switch args[0] {
	case "start":
		return startCore(item, stdout, stderr)
	case "stop":
		return stopCore(item, stdout, stderr)
	case "status":
		return statusCore(item, stdout, stderr)
	case "logs":
		return logsCore(item, stdout, stderr)
	default:
		return fail(stderr, ExitArguments, "unsupported core subcommand")
	}
}

func loadTargets(path string) ([]Target, error) {
	if path == "" {
		return nil, errors.New("target registry path is empty")
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Target{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file targetFile
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("invalid target registry: %w", err)
	}
	for index := range file.Targets {
		if file.Targets[index].Name == "" {
			return nil, errors.New("target name is required")
		}
	}
	return file.Targets, nil
}

func findTarget(targets []Target, name string) (Target, error) {
	for _, item := range targets {
		if item.Name == name {
			return item, nil
		}
	}
	return Target{}, fmt.Errorf("target %q is not configured", name)
}

func targetsPath(args []string) string {
	if value, ok := flagValueOptional(args, "--targets"); ok {
		return value
	}
	if value := os.Getenv("LIAPOLDUS_TARGETS_FILE"); value != "" {
		return value
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "liapoldus", "targets.json")
}

func startCore(target Target, stdout, stderr io.Writer) int {
	if target.Transport != "local-process" {
		return fail(stderr, ExitTarget, "target does not declare local-process transport")
	}
	if target.CoreBinary == "" || target.StatePath == "" || target.PIDPath == "" || target.LogPath == "" {
		return fail(stderr, ExitTarget, "local target requires coreBinary, statePath, pidPath and logPath")
	}
	if running, _ := processRunning(target.PIDPath); running {
		return fail(stderr, ExitTarget, "Core is already running")
	}
	if err := os.MkdirAll(filepath.Dir(target.StatePath), 0o700); err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(target.PIDPath), 0o700); err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(target.LogPath), 0o700); err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	logFile, err := os.OpenFile(target.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	command := exec.Command(target.CoreBinary)
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = append(os.Environ(), "CORE_SQLITE_PATH="+target.StatePath)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fail(stderr, ExitTarget, err.Error())
	}
	_ = logFile.Close()
	if err := os.WriteFile(target.PIDPath, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600); err != nil {
		_ = command.Process.Kill()
		return fail(stderr, ExitTarget, err.Error())
	}
	return encode(stdout, map[string]any{"target": target.Name, "pid": command.Process.Pid, "statePath": target.StatePath})
}

func stopCore(target Target, stdout, stderr io.Writer) int {
	pid, err := targetPID(target)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fail(stderr, ExitTarget, err.Error())
	}
	_ = os.Remove(target.PIDPath)
	return encode(stdout, map[string]any{"target": target.Name, "stopped": true})
}

func statusCore(target Target, stdout, stderr io.Writer) int {
	running, pid := processRunning(target.PIDPath)
	result := map[string]any{"target": target.Name, "running": running, "pid": pid, "endpoint": target.Endpoint}
	if target.Endpoint != "" {
		request, _ := http.NewRequest(http.MethodGet, strings.TrimRight(target.Endpoint, "/")+"/healthz", nil)
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		result["reachable"] = err == nil && response.StatusCode >= 200 && response.StatusCode < 300
		if response != nil {
			_ = response.Body.Close()
		}
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return ExitOperation
	}
	if target.Endpoint != "" && result["reachable"] == false {
		return ExitTarget
	}
	return ExitOK
}

func logsCore(target Target, stdout, stderr io.Writer) int {
	if target.LogPath == "" {
		return fail(stderr, ExitTarget, "target does not declare logPath")
	}
	body, err := os.ReadFile(target.LogPath)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	_, _ = stdout.Write(body)
	return ExitOK
}

func targetPID(target Target) (int, error) {
	if target.PIDPath == "" {
		return 0, errors.New("target does not declare pidPath")
	}
	body, err := os.ReadFile(target.PIDPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid < 1 {
		return 0, errors.New("invalid Core pid")
	}
	return pid, nil
}

func processRunning(path string) (bool, int) {
	target := Target{PIDPath: path}
	pid, err := targetPID(target)
	if err != nil {
		return false, 0
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		return false, pid
	}
	return true, pid
}

func flagValue(args []string, name string) (string, error) {
	value, ok := flagValueOptional(args, name)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}
func flagValueOptional(args []string, name string) (string, bool) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1], true
		}
	}
	return "", false
}
