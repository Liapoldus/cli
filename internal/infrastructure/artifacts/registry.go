package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

type Registry struct{ Path string }

func Default() Registry {
	if value := strings.TrimSpace(os.Getenv("LIAPOLDUS_ARTIFACTS_FILE")); value != "" {
		return Registry{Path: value}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return Registry{}
	}
	return Registry{Path: filepath.Join(base, "liapoldus", "artifacts.json")}
}

type file struct {
	Artifacts []domain.Artifact `json:"artifacts"`
}

func (registry Registry) List() ([]domain.Artifact, error) {
	if registry.Path == "" {
		return nil, errors.New("artifact registry path is empty")
	}
	body, err := os.ReadFile(registry.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Artifact{}, nil
	}
	if err != nil {
		return nil, err
	}
	var stored file
	if err := json.Unmarshal(body, &stored); err != nil {
		return nil, fmt.Errorf("invalid artifact registry: %w", err)
	}
	seen := map[string]struct{}{}
	for _, artifact := range stored.Artifacts {
		if err := artifact.Validate(); err != nil {
			return nil, fmt.Errorf("artifact %s: %w", artifact.Key(), err)
		}
		if _, ok := seen[artifact.Key()]; ok {
			return nil, fmt.Errorf("artifact %s is declared more than once", artifact.Key())
		}
		seen[artifact.Key()] = struct{}{}
	}
	sort.Slice(stored.Artifacts, func(left, right int) bool { return stored.Artifacts[left].Key() < stored.Artifacts[right].Key() })
	return stored.Artifacts, nil
}

func (registry Registry) Find(id, version, platform string) (domain.Artifact, error) {
	items, err := registry.List()
	if err != nil {
		return domain.Artifact{}, err
	}
	key := domain.Artifact{ID: id, Version: version, Platform: platform}.Key()
	for _, item := range items {
		if item.Key() == key {
			return item, nil
		}
	}
	return domain.Artifact{}, fmt.Errorf("%w: %s", domain.ErrArtifactNotFound, key)
}

func (registry Registry) Register(artifact domain.Artifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		return fmt.Errorf("artifact binary: %w", err)
	}
	items, err := registry.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Key() == artifact.Key() {
			return fmt.Errorf("artifact %s already exists", artifact.Key())
		}
	}
	items = append(items, artifact)
	return registry.write(items)
}

func (registry Registry) Trust(id, version, platform, digest string) error {
	items, err := registry.List()
	if err != nil {
		return err
	}
	found := false
	for index := range items {
		if items[index].ID == id && items[index].Version == version && items[index].Platform == platform {
			if items[index].Digest != digest {
				return domain.ErrArtifactDigestMismatch
			}
			items[index].Trusted = true
			found = true
		}
	}
	if !found {
		return domain.ErrArtifactNotFound
	}
	return registry.write(items)
}

func (registry Registry) Remove(id, version, platform string) error {
	items, err := registry.List()
	if err != nil {
		return err
	}
	key := domain.Artifact{ID: id, Version: version, Platform: platform}.Key()
	filtered := items[:0]
	found := false
	for _, item := range items {
		if item.Key() == key {
			found = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !found {
		return domain.ErrArtifactNotFound
	}
	return registry.write(filtered)
}

func (registry Registry) write(items []domain.Artifact) error {
	if registry.Path == "" {
		return errors.New("artifact registry path is empty")
	}
	data, err := json.MarshalIndent(file{Artifacts: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(registry.Path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(registry.Path), ".artifacts-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, registry.Path)
}

func Digest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 1<<34)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
