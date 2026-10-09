package localcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/coreapi"
)

type ProcessManager struct{}

func (ProcessManager) Start(target domain.Target) (map[string]any, error) {
	if target.Transport != domain.TransportLocalProcess {
		return nil, errors.New("target does not declare local-process transport")
	}
	if target.CoreBinary == "" || target.StatePath == "" || target.PIDPath == "" || target.LogPath == "" {
		return nil, errors.New("local target requires coreBinary, statePath, pidPath and logPath")
	}
	if err := validateBootstrap(target.Bootstrap); err != nil {
		return nil, err
	}
	if running, _ := processRunning(target.PIDPath); running {
		return nil, errors.New("core is already running")
	}
	for _, path := range []string{target.StatePath, target.PIDPath, target.LogPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	logFile, err := os.OpenFile(target.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(target.CoreBinary)
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = append(os.Environ(), bootstrapEnvironment(target.StatePath, target.Bootstrap)...)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = logFile.Close()
	if err := os.WriteFile(target.PIDPath, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600); err != nil {
		_ = command.Process.Kill()
		return nil, err
	}
	if target.Endpoint != "" {
		if err := waitForHealth(target, 10*time.Second); err != nil {
			_ = terminateProcess(command.Process, command.Process.Pid)
			_ = os.Remove(target.PIDPath)
			return nil, fmt.Errorf("core did not become ready: %w", err)
		}
	}
	return map[string]any{"target": target.Name, "pid": command.Process.Pid, "statePath": target.StatePath}, nil
}

func validateBootstrap(bootstrap domain.CoreBootstrap) error {
	for name, value := range map[string]string{
		"managementCertificate": bootstrap.ManagementCertificate,
		"managementKey":         bootstrap.ManagementKey,
		"controlCertificate":    bootstrap.ControlCertificate,
		"controlKey":            bootstrap.ControlKey,
		"replicaClientCA":       bootstrap.ReplicaClientCA,
		"replicaServerCA":       bootstrap.ReplicaServerCA,
		"secretRoot":            bootstrap.SecretRoot,
	} {
		if value == "" || !filepath.IsAbs(value) {
			return errors.New("local target bootstrap requires absolute " + name)
		}
	}
	return nil
}

func bootstrapEnvironment(statePath string, bootstrap domain.CoreBootstrap) []string {
	values := []string{"CORE_SQLITE_PATH=" + statePath}
	for name, value := range map[string]string{
		"CORE_INIT_MANAGEMENT_LISTEN":      bootstrap.ManagementListen,
		"CORE_INIT_MANAGEMENT_CERTIFICATE": bootstrap.ManagementCertificate,
		"CORE_INIT_MANAGEMENT_KEY":         bootstrap.ManagementKey,
		"CORE_INIT_MANAGEMENT_CLIENT_CA":   bootstrap.ManagementClientCA,
		"CORE_INIT_CONTROL_LISTEN":         bootstrap.ControlListen,
		"CORE_INIT_CONTROL_PUBLIC_URL":     bootstrap.ControlPublicURL,
		"CORE_INIT_CONTROL_CERTIFICATE":    bootstrap.ControlCertificate,
		"CORE_INIT_CONTROL_KEY":            bootstrap.ControlKey,
		"CORE_INIT_REPLICA_CLIENT_CA":      bootstrap.ReplicaClientCA,
		"CORE_INIT_REPLICA_SERVER_CA":      bootstrap.ReplicaServerCA,
		"CORE_INIT_SECRET_ROOT":            bootstrap.SecretRoot,
	} {
		if value != "" {
			values = append(values, name+"="+value)
		}
	}
	return values
}

func (ProcessManager) Stop(target domain.Target) (map[string]any, error) {
	pid, err := targetPID(target.PIDPath)
	if err != nil {
		return nil, err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	if err := terminateProcess(process, pid); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(process, pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(process, pid) {
		return nil, errors.New("core did not stop within 10 seconds")
	}
	_ = os.Remove(target.PIDPath)
	return map[string]any{"target": target.Name, "stopped": true}, nil
}

func (ProcessManager) Status(target domain.Target) (domain.LocalCoreStatus, error) {
	running, pid := processRunning(target.PIDPath)
	status := domain.LocalCoreStatus{Target: target.Name, Running: running, PID: pid, Endpoint: target.Endpoint}
	if target.Endpoint == "" {
		return status, nil
	}
	client, err := coreapi.New(domain.CoreConnection{
		Endpoint: target.Endpoint, CAFile: target.CAFile,
		ClientCertFile: target.ClientCertFile, ClientKeyFile: target.ClientKeyFile,
		ServerName: target.ServerName,
	})
	if err != nil {
		return status, err
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, healthErr := client.Health(requestContext)
	reachable := healthErr == nil
	status.Reachable = &reachable
	if !reachable {
		if healthErr != nil {
			return status, fmt.Errorf("target Core is not reachable: %w", healthErr)
		}
		return status, errors.New("target Core is not reachable")
	}
	return status, nil
}

func waitForHealth(target domain.Target, timeout time.Duration) error {
	client, err := coreapi.New(domain.CoreConnection{
		Endpoint: target.Endpoint, CAFile: target.CAFile,
		ClientCertFile: target.ClientCertFile, ClientKeyFile: target.ClientKeyFile,
		ServerName: target.ServerName,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		_, lastErr = client.Health(ctx)
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (ProcessManager) Logs(target domain.Target) ([]byte, error) {
	if target.LogPath == "" {
		return nil, errors.New("target does not declare logPath")
	}
	return os.ReadFile(target.LogPath)
}

func (ProcessManager) Backup(target domain.Target, output string) error {
	if target.Transport != domain.TransportLocalProcess {
		return errors.New("database execution requires local-process target")
	}
	if running, _ := processRunning(target.PIDPath); running {
		return errors.New("stop Core before database backup or restore")
	}
	if target.StatePath == "" {
		return errors.New("target does not declare statePath")
	}
	if _, err := os.Stat(output); err == nil {
		return errors.New("backup output already exists")
	}
	return copyFile(target.StatePath, output, false)
}

func (ProcessManager) Restore(target domain.Target, input string) error {
	if target.Transport != domain.TransportLocalProcess {
		return errors.New("database execution requires local-process target")
	}
	if running, _ := processRunning(target.PIDPath); running {
		return errors.New("stop Core before database backup or restore")
	}
	if target.StatePath == "" {
		return errors.New("target does not declare statePath")
	}
	if _, err := os.Stat(input); err != nil {
		return err
	}
	temporary := target.StatePath + ".restore.tmp"
	if err := copyFile(input, temporary, true); err != nil {
		return err
	}
	if err := os.Rename(temporary, target.StatePath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func copyFile(source, destination string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(destination); err == nil {
			return errors.New("destination already exists")
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func targetPID(path string) (int, error) {
	if path == "" {
		return 0, errors.New("target does not declare pidPath")
	}
	body, err := os.ReadFile(path)
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
	pid, err := targetPID(path)
	if err != nil {
		return false, 0
	}
	process, err := os.FindProcess(pid)
	if err != nil || !processAlive(process, pid) {
		return false, pid
	}
	return true, pid
}
