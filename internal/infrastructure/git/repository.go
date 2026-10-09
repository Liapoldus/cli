package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

// Repository is the Git-backed project source used by the application layer.
type Repository struct{}

func (Repository) ResolveRevision(ctx context.Context, projectPath, revision string) (string, error) {
	if strings.TrimSpace(revision) == "" {
		return "", domain.ErrInvalidRevision
	}
	output, err := run(ctx, projectPath, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
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
	output, err := run(ctx, projectPath, "ls-tree", "-r", "-z", "--name-only", revision)
	if err != nil {
		return nil, fmt.Errorf("list revision tree: %w", err)
	}
	parts := bytes.Split([]byte(output), []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			paths = append(paths, filepath.ToSlash(string(part)))
		}
	}
	return paths, nil
}

func show(ctx context.Context, projectPath, revision, path string) ([]byte, error) {
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
