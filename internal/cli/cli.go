package cli

import (
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
)

const (
	ExitOK         = 0
	ExitArguments  = 2
	ExitRevision   = 3
	ExitValidation = 4
	ExitTarget     = 5
	ExitOperation  = 6
	ExitDegraded   = 7
	ExitConflict   = 8
)

type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}

type Bundle struct {
	SchemaVersion string `json:"schemaVersion"`
	ProjectPath   string `json:"projectPath"`
	Revision      string `json:"revision"`
	Target        string `json:"target"`
	Digest        string `json:"digest"`
	Files         []File `json:"files"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, ExitArguments, "command is required")
	}
	switch args[0] {
	case "project":
		if len(args) == 2 && args[1] == "inspect" {
			return inspectProject(args[2:], stdout, stderr)
		}
	case "validate":
		return buildCommand(args[1:], stdout, stderr, false)
	case "bundle":
		if len(args) >= 2 && args[1] == "build" {
			return buildCommand(args[2:], stdout, stderr, true)
		}
	case "plan":
		return buildCommand(args[1:], stdout, stderr, false)
	case "apply":
		return applyCommand(args[1:], stdout, stderr)
	}
	return fail(stderr, ExitArguments, "unsupported command")
}

func inspectProject(args []string, stdout, stderr io.Writer) int {
	project, err := projectFlag(args)
	if err != nil {
		return fail(stderr, ExitArguments, err.Error())
	}
	entries, err := files(project)
	if err != nil {
		return fail(stderr, ExitValidation, err.Error())
	}
	return encode(stdout, map[string]any{"project": project, "files": entries})
}

func buildCommand(args []string, stdout, stderr io.Writer, writeFile bool) int {
	project, revision, target, output, err := options(args)
	if err != nil {
		return fail(stderr, ExitArguments, err.Error())
	}
	if revision == "" {
		return fail(stderr, ExitArguments, "--revision is required")
	}
	resolved, err := resolveRevision(project, revision)
	if err != nil {
		return fail(stderr, ExitRevision, err.Error())
	}
	bundle, err := makeBundle(project, resolved, target)
	if err != nil {
		return fail(stderr, ExitValidation, err.Error())
	}
	if writeFile && output != "" {
		body, _ := json.MarshalIndent(bundle, "", "  ")
		if err := os.WriteFile(output, append(body, '\n'), 0o600); err != nil {
			return fail(stderr, ExitValidation, err.Error())
		}
	}
	return encode(stdout, bundle)
}

func applyCommand(args []string, stdout, stderr io.Writer) int {
	project, revision, target, _, err := options(args)
	if err != nil {
		return fail(stderr, ExitArguments, err.Error())
	}
	if revision == "" {
		return fail(stderr, ExitArguments, "--revision is required for apply")
	}
	if target == "" {
		return fail(stderr, ExitArguments, "--target is required for apply")
	}
	if !hasFlag(args, "--confirm") {
		return fail(stderr, ExitArguments, "apply requires --confirm")
	}
	if dirty, err := git(project, "status", "--porcelain"); err != nil {
		return fail(stderr, ExitRevision, err.Error())
	} else if strings.TrimSpace(dirty) != "" {
		return fail(stderr, ExitRevision, "apply requires a clean working tree")
	}
	resolved, err := resolveRevision(project, revision)
	if err != nil {
		return fail(stderr, ExitRevision, err.Error())
	}
	bundle, err := makeBundle(project, resolved, target)
	if err != nil {
		return fail(stderr, ExitValidation, err.Error())
	}
	return encode(stdout, map[string]any{"accepted": false, "bundle": bundle, "next": "send this bundle to the target Management API"})
}

func options(args []string) (string, string, string, string, error) {
	var project, revision, target, output string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			switch args[i] {
			case "--project":
				project = args[i+1]
			case "--revision":
				revision = args[i+1]
			case "--target":
				target = args[i+1]
			case "--output":
				output = args[i+1]
			}
			i++
		}
	}
	if project == "" {
		return "", "", "", "", errors.New("--project is required")
	}
	project, err := filepath.Abs(project)
	return project, revision, target, output, err
}

func projectFlag(args []string) (string, error) {
	project := "."
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--project" {
			project = args[i+1]
		}
	}
	return filepath.Abs(project)
}

func resolveRevision(project, revision string) (string, error) {
	if strings.TrimSpace(revision) == "" {
		return "", errors.New("empty revision")
	}
	resolved, err := git(project, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", errors.New("revision is not a Git commit")
	}
	return strings.TrimSpace(resolved), nil
}

func makeBundle(project, revision, target string) (Bundle, error) {
	entries, err := files(project)
	if err != nil {
		return Bundle{}, err
	}
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", entry.Path, entry.Digest, entry.Bytes)
	}
	return Bundle{SchemaVersion: "core-config/v2", ProjectPath: project, Revision: revision, Target: target, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Files: entries}, nil
}

func files(project string) ([]File, error) {
	info, err := os.Stat(filepath.Join(project, "project.yaml"))
	if err != nil || info.IsDir() {
		return nil, errors.New("project.yaml is required")
	}
	var result []File
	err = filepath.Walk(project, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(project, path)
		if info.IsDir() {
			if rel == ".git" || rel == ".studio" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, ".studio/") || strings.HasSuffix(rel, ".key") || strings.HasSuffix(rel, ".pem") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		result = append(result, File{Path: filepath.ToSlash(rel), Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: len(body)})
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, err
}

func git(project string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = project
	output, err := command.Output()
	return string(output), err
}
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
func encode(writer io.Writer, value any) int {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return ExitOperation
	}
	return ExitOK
}
func fail(writer io.Writer, code int, message string) int {
	_, _ = fmt.Fprintln(writer, message)
	return code
}
