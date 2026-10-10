package targets

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

type Registry struct {
	Path string
}

func Default() Registry {
	if value := os.Getenv("LIAPOLDUS_TARGETS_FILE"); value != "" {
		return Registry{Path: value}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return Registry{}
	}
	return Registry{Path: filepath.Join(base, "liapoldus", "targets.json")}
}

type file struct {
	Targets []domain.Target `json:"targets"`
}

func (r Registry) List() ([]domain.Target, error) {
	if r.Path == "" {
		return nil, errors.New("target registry path is empty")
	}
	body, err := os.ReadFile(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Target{}, nil
	}
	if err != nil {
		return nil, err
	}
	var value file
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid target registry: %w", err)
	}
	base := filepath.Dir(r.Path)
	seen := make(map[string]struct{}, len(value.Targets))
	for index := range value.Targets {
		if value.Targets[index].Name == "" {
			return nil, errors.New("target name is required")
		}
		if _, ok := seen[value.Targets[index].Name]; ok {
			return nil, fmt.Errorf("target %q is declared more than once", value.Targets[index].Name)
		}
		seen[value.Targets[index].Name] = struct{}{}
		if !validTransport(value.Targets[index].Transport) {
			return nil, fmt.Errorf("target %q has unsupported transport %q", value.Targets[index].Name, value.Targets[index].Transport)
		}
		if err := validateEndpoint(value.Targets[index]); err != nil {
			return nil, err
		}
		if err := validateProjectRelativeReportPath(value.Targets[index]); err != nil {
			return nil, err
		}
		if value.Targets[index].Approval != "" && value.Targets[index].Approval != domain.ApprovalRemoteRequired {
			return nil, fmt.Errorf("target %q has unsupported approval policy %q", value.Targets[index].Name, value.Targets[index].Approval)
		}
		value.Targets[index].CoreBinary = resolvePath(base, value.Targets[index].CoreBinary)
		value.Targets[index].StatePath = resolvePath(base, value.Targets[index].StatePath)
		value.Targets[index].PIDPath = resolvePath(base, value.Targets[index].PIDPath)
		value.Targets[index].LogPath = resolvePath(base, value.Targets[index].LogPath)
		value.Targets[index].CAFile = resolvePath(base, value.Targets[index].CAFile)
		value.Targets[index].ClientCertFile = resolvePath(base, value.Targets[index].ClientCertFile)
		value.Targets[index].ClientKeyFile = resolvePath(base, value.Targets[index].ClientKeyFile)
		value.Targets[index].Bootstrap = resolveBootstrap(base, value.Targets[index].Bootstrap)
	}
	return value.Targets, nil
}

func (r Registry) Find(name string) (domain.Target, error) {
	items, err := r.List()
	if err != nil {
		return domain.Target{}, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return domain.Target{}, fmt.Errorf("%w: %q", domain.ErrTargetNotConfigured, name)
}

func (r Registry) Add(target domain.Target) error {
	if r.Path == "" {
		return errors.New("target registry path is empty")
	}
	if target.Name == "" {
		return errors.New("target name is required")
	}
	if !validTransport(target.Transport) {
		return fmt.Errorf("target %q has unsupported transport %q", target.Name, target.Transport)
	}
	if err := validateEndpoint(target); err != nil {
		return err
	}
	if err := validateProjectRelativeReportPath(target); err != nil {
		return err
	}
	if target.Approval != "" && target.Approval != domain.ApprovalRemoteRequired {
		return fmt.Errorf("target %q has unsupported approval policy %q", target.Name, target.Approval)
	}
	items, err := r.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Name == target.Name {
			return fmt.Errorf("target %q already exists", target.Name)
		}
	}
	items = append(items, target)
	data, err := json.MarshalIndent(file{Targets: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o700); err != nil {
		return err
	}
	temporaryFile, err := os.CreateTemp(filepath.Dir(r.Path), ".targets-*.tmp")
	if err != nil {
		return err
	}
	temporary := temporaryFile.Name()
	defer os.Remove(temporary)
	if err := temporaryFile.Chmod(0o600); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if _, err := temporaryFile.Write(append(data, '\n')); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Sync(); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, r.Path); err != nil {
		return err
	}
	return nil
}

func validTransport(value string) bool {
	return value == domain.TransportLocalProcess || value == domain.TransportRemoteAPI
}

func validateEndpoint(target domain.Target) error {
	if target.Endpoint == "" {
		if target.Transport == domain.TransportRemoteAPI {
			return fmt.Errorf("target %q requires an endpoint", target.Name)
		}
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(target.Endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("target %q has invalid HTTP(S) endpoint", target.Name)
	}
	if parsed.Scheme == "http" && (target.CAFile != "" || target.ClientCertFile != "" || target.ClientKeyFile != "" || target.ServerName != "") {
		return fmt.Errorf("target %q has TLS settings for an HTTP endpoint", target.Name)
	}
	if parsed.Scheme == "https" && (target.ClientCertFile == "" || target.ClientKeyFile == "") {
		return fmt.Errorf("target %q HTTPS endpoint requires client certificate and key", target.Name)
	}
	return nil
}

func validateProjectRelativeReportPath(target domain.Target) error {
	value := strings.TrimSpace(target.TrafficReportPath)
	if value == "" {
		return nil
	}
	if filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("target %q traffic report path must be project-relative", target.Name)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target %q traffic report path escapes the project", target.Name)
	}
	return nil
}

func resolvePath(base, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(base, value))
}

func resolveBootstrap(base string, bootstrap domain.CoreBootstrap) domain.CoreBootstrap {
	bootstrap.ManagementCertificate = resolvePath(base, bootstrap.ManagementCertificate)
	bootstrap.ManagementKey = resolvePath(base, bootstrap.ManagementKey)
	bootstrap.ManagementClientCA = resolvePath(base, bootstrap.ManagementClientCA)
	bootstrap.ControlCertificate = resolvePath(base, bootstrap.ControlCertificate)
	bootstrap.ControlKey = resolvePath(base, bootstrap.ControlKey)
	bootstrap.ReplicaClientCA = resolvePath(base, bootstrap.ReplicaClientCA)
	bootstrap.ReplicaServerCA = resolvePath(base, bootstrap.ReplicaServerCA)
	bootstrap.SecretRoot = resolvePath(base, bootstrap.SecretRoot)
	return bootstrap
}
