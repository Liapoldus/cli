package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
	"gopkg.in/yaml.v3"
)

// Repository is the Git-backed project source used by the application layer.
type Repository struct{}

const WorkingTreeRevision = domain.WorkingTreeRevision

func (Repository) ResolveRevision(ctx context.Context, projectPath, revision string) (string, error) {
	if strings.TrimSpace(revision) == "" {
		return "", domain.ErrInvalidRevision
	}
	if revision == WorkingTreeRevision {
		return WorkingTreeRevision, nil
	}
	output, err := run(ctx, projectPath, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		if revision == "HEAD" {
			if _, statErr := os.Stat(filepath.Join(projectPath, "project.yaml")); statErr == nil {
				return WorkingTreeRevision, nil
			}
		}
		return "", domain.ErrInvalidRevision
	}
	return strings.TrimSpace(output), nil
}

func (Repository) WorkingTreeStatus(ctx context.Context, projectPath string) (string, error) {
	return run(ctx, projectPath, "status", "--porcelain")
}

func (Repository) RemoteOrigin(ctx context.Context, projectPath string) (string, error) {
	output, err := run(ctx, projectPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(output), nil
}

func (Repository) Files(ctx context.Context, projectPath, revision string) ([]domain.File, error) {
	paths, err := treePaths(ctx, projectPath, revision)
	if err != nil {
		return nil, err
	}
	files := make([]domain.File, 0, len(paths))
	for _, path := range paths {
		if ignoredPath(path) {
			continue
		}
		body, err := show(ctx, projectPath, revision, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		digest := sha256.Sum256(body)
		files = append(files, domain.File{Path: path, Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: len(body)})
	}
	if !containsPath(files, "project.yaml") {
		return nil, errors.New("project.yaml is required")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// RuntimeArtifacts reads the exact immutable binaries declared by each
// service in a Project revision. Legacy service manifests are rejected so a
// runtime can never silently substitute another local binary by service ID.
func (Repository) RuntimeArtifacts(ctx context.Context, projectPath, revision string) ([]domain.ArtifactReference, error) {
	paths, err := treePaths(ctx, projectPath, revision)
	if err != nil {
		return nil, err
	}
	artifacts := make([]domain.ArtifactReference, 0)
	seen := make(map[string]struct{})
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "services" || parts[2] != "service.yaml" {
			continue
		}
		serviceID := parts[1]
		if serviceID == "" || serviceID == "." || serviceID == ".." || strings.ContainsAny(serviceID, "/\\\x00") {
			return nil, fmt.Errorf("invalid service path %q", path)
		}
		if _, ok := seen[serviceID]; ok {
			return nil, fmt.Errorf("duplicate service manifest for %s", serviceID)
		}
		seen[serviceID] = struct{}{}
		body, err := show(ctx, projectPath, revision, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var manifest struct {
			ID       string `yaml:"id"`
			Version  string `yaml:"version"`
			Artifact struct {
				ID       string `yaml:"id"`
				Version  string `yaml:"version"`
				Platform string `yaml:"platform"`
				Digest   string `yaml:"digest"`
			} `yaml:"artifact"`
			Runtime struct {
				Command         string `yaml:"command"`
				ControlEndpoint string `yaml:"controlEndpoint"`
				PeerEndpoint    string `yaml:"peerEndpoint"`
			} `yaml:"runtime"`
		}
		decoder := yaml.NewDecoder(bytes.NewReader(body))
		decoder.KnownFields(true)
		if err := decoder.Decode(&manifest); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, fmt.Errorf("invalid %s: multiple YAML documents are not supported", path)
			}
			return nil, fmt.Errorf("invalid %s: %w", path, err)
		}
		artifact := domain.ArtifactReference{
			ID: manifest.Artifact.ID, Version: manifest.Artifact.Version,
			Platform: manifest.Artifact.Platform, Digest: manifest.Artifact.Digest,
			Command: manifest.Runtime.Command, ControlEndpoint: manifest.Runtime.ControlEndpoint,
			PeerEndpoint: manifest.Runtime.PeerEndpoint,
		}
		if manifest.ID != serviceID || manifest.Version != artifact.Version {
			return nil, fmt.Errorf("invalid %s: service id/version must match its artifact reference", path)
		}
		if err := artifact.Validate(serviceID); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", path, err)
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	return artifacts, nil
}

func (Repository) ServiceSettings(ctx context.Context, projectPath, revision string) ([]domain.Service, error) {
	paths, err := treePaths(ctx, projectPath, revision)
	if err != nil {
		return nil, err
	}
	services := make([]domain.Service, 0)
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "services" || parts[2] != "settings.json" || parts[1] == "" {
			continue
		}
		body, err := show(ctx, projectPath, revision, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if !json.Valid(body) {
			return nil, fmt.Errorf("service %s settings.json is not valid JSON", parts[1])
		}
		if !jsonObject(body) {
			return nil, fmt.Errorf("service %s settings.json must be a JSON object", parts[1])
		}
		services = append(services, domain.Service{ID: parts[1], Settings: json.RawMessage(body)})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	return services, nil
}

func (Repository) Links(ctx context.Context, projectPath, revision string) ([]json.RawMessage, error) {
	paths, err := treePaths(ctx, projectPath, revision)
	if err != nil {
		return nil, err
	}
	type linkFile struct {
		path string
		body json.RawMessage
	}
	files := make([]linkFile, 0)
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) < 4 || parts[0] != "services" || parts[2] != "links" || !strings.HasSuffix(parts[len(parts)-1], ".json") {
			continue
		}
		body, err := show(ctx, projectPath, revision, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if !json.Valid(body) || !jsonObject(body) {
			return nil, fmt.Errorf("link %s must be a JSON object", path)
		}
		files = append(files, linkFile{path: path, body: json.RawMessage(body)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	links := make([]json.RawMessage, 0, len(files))
	for _, file := range files {
		links = append(links, file.body)
	}
	return links, nil
}

func (Repository) ProjectID(ctx context.Context, projectPath, revision string) (string, error) {
	body, err := show(ctx, projectPath, revision, "project.yaml")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "id:") {
			value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "id:")), "\"'")
			if value != "" {
				return value, nil
			}
		}
	}
	return "", errors.New("project.yaml id is required")
}

func treePaths(ctx context.Context, projectPath, revision string) ([]string, error) {
	if revision == WorkingTreeRevision {
		output, err := run(ctx, projectPath, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, fmt.Errorf("list working tree: %w", err)
		}
		paths, err := projectPaths(output)
		if err != nil {
			return nil, err
		}
		present := paths[:0]
		for _, path := range paths {
			if _, err := os.Lstat(filepath.Join(projectPath, filepath.FromSlash(path))); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("inspect working tree path %s: %w", path, err)
			}
			present = append(present, path)
		}
		return present, nil
	}
	output, err := run(ctx, projectPath, "ls-tree", "-r", "-z", "--name-only", revision)
	if err != nil {
		return nil, fmt.Errorf("list revision tree: %w", err)
	}
	return projectPaths(output)
}

// Reject obsolete runtime inputs rather than silently building an incomplete
// bundle. Product settings and historical reports remain opaque bytes.
func projectPaths(output string) ([]string, error) {
	paths := splitPaths(output)
	sort.Strings(paths)
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(path))
		if path == "" || filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsRune(path, '\\') {
			return nil, fmt.Errorf("invalid project path %q", path)
		}
		parts := strings.Split(path, "/")
		if len(parts) < 3 {
			continue
		}
		retiredManifest := len(parts) == 3 && parts[0] == "services" && parts[2] == "plugin.yaml"
		retiredRuntime := parts[0] == "plugins" && (parts[2] == "plugin.yaml" || parts[2] == "service.yaml" || parts[2] == "settings.json" || parts[2] == "links")
		if retiredManifest || retiredRuntime {
			return nil, fmt.Errorf("retired runtime path %q: migrate to services/<id>/service.yaml, settings.json and links before delivery; preserve IDs and payload bytes", path)
		}
	}
	return paths, nil
}

func splitPaths(output string) []string {
	parts := bytes.Split([]byte(output), []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			paths = append(paths, filepath.ToSlash(string(part)))
		}
	}
	return paths
}

func show(ctx context.Context, projectPath, revision, path string) ([]byte, error) {
	if revision == WorkingTreeRevision {
		root, err := filepath.Abs(projectPath)
		if err != nil {
			return nil, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		clean, err := safeRegularFile(root, path)
		if err != nil {
			return nil, fmt.Errorf("working tree file is not safe: %w", err)
		}
		return os.ReadFile(clean)
	}
	command := exec.CommandContext(ctx, "git", "show", revision+":"+path)
	command.Dir = projectPath
	return command.Output()
}

func run(ctx context.Context, projectPath string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = projectPath
	output, err := command.Output()
	return string(output), err
}

func ignoredPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(path, ".studio/") ||
		strings.HasPrefix(base, ".env") ||
		strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".pem") ||
		strings.HasSuffix(base, ".secret") ||
		strings.Contains("/"+strings.ToLower(path)+"/", "/secrets/")
}

func containsPath(files []domain.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func jsonObject(body []byte) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(body, &value) == nil && value != nil
}
