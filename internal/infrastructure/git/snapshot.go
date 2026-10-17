package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

const (
	maxSnapshotFileBytes  = 64 << 20
	maxSnapshotTotalBytes = 512 << 20
)

// Snapshotter creates a private Git-backed snapshot of the current project
// without staging, committing, or otherwise modifying the user's checkout.
type Snapshotter struct{}

type projectSnapshot struct {
	root string
	dir  string
}

func (s projectSnapshot) RootPath() string { return s.root }

func (s projectSnapshot) Close() error {
	if s.dir == "" {
		return nil
	}
	_ = filepath.WalkDir(s.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(s.dir)
}

func (Snapshotter) Capture(ctx context.Context, projectPath string) (domain.ProjectSnapshot, error) {
	root, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project path: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("project path must be an existing directory")
	}
	if _, err := os.Stat(filepath.Join(root, "project.yaml")); err != nil {
		return nil, errors.New("project.yaml is required")
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open project root safely: %w", err)
	}
	defer sourceRoot.Close()

	temporaryRoot, err := os.MkdirTemp("", "liapoldus-dev-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create private snapshot directory: %w", err)
	}
	snapshotRoot := filepath.Join(temporaryRoot, "project")
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		_ = os.RemoveAll(temporaryRoot)
		return nil, fmt.Errorf("create snapshot project: %w", err)
	}
	snapshot := projectSnapshot{root: snapshotRoot, dir: temporaryRoot}
	paths, err := snapshotPaths(ctx, root)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	paths = filterSnapshotPaths(paths)
	if len(paths) == 0 {
		_ = snapshot.Close()
		return nil, errors.New("project contains no eligible files")
	}

	initialDigests := make(map[string]string, len(paths))
	var total int64
	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			_ = snapshot.Close()
			return nil, err
		}
		_, err := safeRegularFile(root, relative)
		if errors.Is(err, os.ErrNotExist) {
			// A deleted tracked path is a deletion in the draft, not a snapshot error.
			continue
		}
		if err != nil {
			_ = snapshot.Close()
			return nil, fmt.Errorf("snapshot %s: %w", relative, err)
		}
		digest, size, err := copySnapshotFile(sourceRoot, root, snapshotRoot, relative)
		if err != nil {
			_ = snapshot.Close()
			return nil, fmt.Errorf("snapshot %s: %w", relative, err)
		}
		total += size
		if total > maxSnapshotTotalBytes {
			_ = snapshot.Close()
			return nil, fmt.Errorf("project snapshot exceeds %d MiB safety limit", maxSnapshotTotalBytes>>20)
		}
		initialDigests[relative] = digest
	}
	if _, ok := initialDigests["project.yaml"]; !ok {
		_ = snapshot.Close()
		return nil, errors.New("project.yaml is missing from the project snapshot")
	}

	verifiedPaths, err := snapshotPaths(ctx, root)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	verifiedPaths = filterSnapshotPaths(verifiedPaths)
	for _, relative := range verifiedPaths {
		if _, copied := initialDigests[relative]; !copied {
			// The file may be a tracked deletion. Any newly appearing file means
			// the checkout changed during capture, so abort rather than mix states.
			if _, statErr := safeRegularFile(root, relative); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			_ = snapshot.Close()
			return nil, errors.New("project changed while its development snapshot was being captured; retry the operation")
		}
	}
	if len(verifiedPaths) != len(paths) {
		// Missing entries are expected for tracked deletions; digest verification
		// below proves that every copied file still matches its source.
		for _, relative := range paths {
			if _, copied := initialDigests[relative]; !copied {
				continue
			}
			if _, err := safeRegularFile(root, relative); errors.Is(err, os.ErrNotExist) {
				_ = snapshot.Close()
				return nil, errors.New("project changed while its development snapshot was being captured; retry the operation")
			}
		}
	}
	for relative, expected := range initialDigests {
		_, err := safeRegularFile(root, relative)
		if err != nil {
			_ = snapshot.Close()
			return nil, errors.New("project changed while its development snapshot was being captured; retry the operation")
		}
		actual, err := digestProjectFile(sourceRoot, root, relative)
		if err != nil || actual != expected {
			_ = snapshot.Close()
			return nil, errors.New("project changed while its development snapshot was being captured; retry the operation")
		}
	}

	if err := runSnapshotGit(ctx, snapshotRoot, "init", "--quiet"); err != nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("initialize private snapshot index: %w", err)
	}
	if err := runSnapshotGit(ctx, snapshotRoot, "add", "--all", "--force", "--", "."); err != nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("index private snapshot: %w", err)
	}
	if err := makeSnapshotReadOnly(snapshotRoot); err != nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("seal private snapshot: %w", err)
	}
	return snapshot, nil
}

func snapshotPaths(ctx context.Context, root string) ([]string, error) {
	output, err := run(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err == nil {
		return projectPaths(output)
	}
	// Projects without Git are still editable in Studio. Walk the filesystem in
	// that case, while excluding VCS metadata and all secret/private paths.
	paths := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() && (entry.Name() == ".git" || relative == ".studio" || strings.HasPrefix(relative, ".studio/")) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed in a development snapshot: %s", relative)
		}
		if !entry.IsDir() && !ignoredPath(relative) {
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk project for development snapshot: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func filterSnapshotPaths(paths []string) []string {
	filtered := paths[:0]
	for _, relative := range paths {
		if !ignoredPath(relative) {
			filtered = append(filtered, relative)
		}
	}
	return filtered
}

func safeRegularFile(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, '\\') || strings.ContainsRune(relative, '\x00') {
		return "", errors.New("invalid project-relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("project path escapes its root")
	}
	candidate := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("project path escapes its root")
	}
	current := root
	parts := strings.Split(clean, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symbolic links are not allowed in a development snapshot")
		}
		if index != len(parts)-1 && !info.IsDir() {
			return "", errors.New("project path has a non-directory parent")
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", errors.New("project path is not a regular file")
		}
	}
	return candidate, nil
}

func copySnapshotFile(sourceRoot *os.Root, sourceRootPath, destinationRoot, relative string) (string, int64, error) {
	if _, err := safeRegularFile(sourceRootPath, relative); err != nil {
		return "", 0, err
	}
	input, err := sourceRoot.Open(filepath.FromSlash(relative))
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() > maxSnapshotFileBytes {
		return "", 0, fmt.Errorf("file exceeds %d MiB safety limit", maxSnapshotFileBytes>>20)
	}
	destination := filepath.Join(destinationRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", 0, err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, maxSnapshotFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return "", size, copyErr
	}
	if closeErr != nil {
		return "", size, closeErr
	}
	if size > maxSnapshotFileBytes {
		return "", size, fmt.Errorf("file exceeds %d MiB safety limit", maxSnapshotFileBytes>>20)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func digestProjectFile(sourceRoot *os.Root, sourceRootPath, relative string) (string, error) {
	if _, err := safeRegularFile(sourceRootPath, relative); err != nil {
		return "", err
	}
	file, err := sourceRoot.Open(filepath.FromSlash(relative))
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxSnapshotFileBytes+1))
	if err != nil {
		return "", err
	}
	if size > maxSnapshotFileBytes {
		return "", errors.New("file exceeded snapshot safety limit")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func runSnapshotGit(ctx context.Context, root string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func makeSnapshotReadOnly(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		return os.Chmod(path, 0o400)
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o500); err != nil {
			return err
		}
	}
	return nil
}
