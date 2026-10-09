package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	SchemaVersion string    `json:"schemaVersion"`
	ProjectPath   string    `json:"projectPath"`
	Revision      string    `json:"revision"`
	Target        string    `json:"target"`
	Digest        string    `json:"digest"`
	Files         []File    `json:"files"`
	Services      []Service `json:"services,omitempty"`
	Links         []any     `json:"links,omitempty"`
}

type Service struct {
	ID       string          `json:"id"`
	Settings json.RawMessage `json:"settings"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, ExitArguments, "command is required")
	}
	switch args[0] {
	case "target":
		return targetCommand(args[1:], stdout, stderr)
	case "core":
		return coreCommand(args[1:], stdout, stderr)
	case "project":
		if len(args) >= 2 && args[1] == "inspect" {
			return inspectProject(args[2:], stdout, stderr)
		}
	case "validate":
		return buildCommand(args[1:], stdout, stderr, false)
	case "bundle":
		if len(args) >= 2 && args[1] == "build" {
			return buildCommand(args[2:], stdout, stderr, true)
		}
	case "plan":
		return planCommand(args[1:], stdout, stderr)
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
	project, revision, target, output, _, _, _, err := options(args)
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
	project, revision, target, _, endpoint, token, idempotencyKey, err := options(args)
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
	if endpoint == "" {
		return fail(stderr, ExitTarget, "--endpoint or target endpoint environment is required for apply")
	}
	if token == "" {
		return fail(stderr, ExitTarget, "--token or target token environment is required for apply")
	}
	if idempotencyKey == "" {
		idempotencyKey = "apply-" + strings.TrimPrefix(bundle.Digest, "sha256:")
	}
	result, status, err := sendBundle(endpoint, token, "/api/config-bundles/apply", bundle, idempotencyKey)
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if status < 200 || status >= 300 {
		return fail(stderr, ExitOperation, string(result))
	}
	if hasFlag(args, "--wait") {
		if code, err := waitForOperations(endpoint, token, result); err != nil {
			return fail(stderr, ExitTarget, err.Error())
		} else if code != ExitOK {
			return fail(stderr, code, "Core operation did not complete successfully")
		}
	}
	return encode(stdout, map[string]any{"accepted": true, "bundle": bundle, "response": json.RawMessage(result)})
}

func planCommand(args []string, stdout, stderr io.Writer) int {
	project, revision, target, _, endpoint, token, _, err := options(args)
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
	if endpoint == "" {
		return fail(stderr, ExitTarget, "--endpoint or target endpoint environment is required for plan")
	}
	if token == "" {
		return fail(stderr, ExitTarget, "--token or target token environment is required for plan")
	}
	result, status, err := sendBundle(endpoint, token, "/api/config-bundles/plan", bundle, "plan-"+strings.TrimPrefix(bundle.Digest, "sha256:"))
	if err != nil {
		return fail(stderr, ExitTarget, err.Error())
	}
	if status < 200 || status >= 300 {
		return fail(stderr, ExitOperation, string(result))
	}
	return encode(stdout, json.RawMessage(result))
}

func options(args []string) (string, string, string, string, string, string, string, error) {
	var project, revision, target, output, endpoint, token, idempotencyKey string
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
			case "--endpoint":
				endpoint = args[i+1]
			case "--token":
				token = args[i+1]
			case "--idempotency-key":
				idempotencyKey = args[i+1]
			}
			i++
		}
	}
	if project == "" {
		return "", "", "", "", "", "", "", errors.New("--project is required")
	}
	project, err := filepath.Abs(project)
	if endpoint == "" && target != "" {
		endpoint = os.Getenv("LIAPOLDUS_TARGET_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_ENDPOINT")
	}
	if token == "" && target != "" {
		token = os.Getenv("LIAPOLDUS_TARGET_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_TOKEN")
	}
	if target != "" && (endpoint == "" || token == "") {
		if configured, loadErr := loadTargets(targetsPath(args)); loadErr == nil {
			if item, findErr := findTarget(configured, target); findErr == nil {
				if endpoint == "" {
					endpoint = item.Endpoint
				}
				if token == "" && item.TokenEnv != "" {
					token = os.Getenv(item.TokenEnv)
				}
			}
		}
	}
	return project, revision, target, output, endpoint, token, idempotencyKey, err
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
	services, err := serviceSettings(project)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{SchemaVersion: "core-config/v2", ProjectPath: project, Revision: revision, Target: target, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Files: entries, Services: services}, nil
}

func serviceSettings(project string) ([]Service, error) {
	root := filepath.Join(project, "services")
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var services []Service
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || parts[1] != "settings.json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !json.Valid(body) {
			return fmt.Errorf("service %s settings.json is not valid JSON", parts[0])
		}
		services = append(services, Service{ID: parts[0], Settings: json.RawMessage(body)})
		return nil
	})
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	return services, err
}

func sendBundle(endpoint, token, path string, bundle Bundle, idempotencyKey string) ([]byte, int, error) {
	projectID := projectID(bundle.ProjectPath)
	repository, _ := git(bundle.ProjectPath, "config", "--get", "remote.origin.url")
	request := map[string]any{
		"project": map[string]string{"id": projectID, "repository": strings.TrimSpace(repository), "revision": bundle.Revision},
		"bundle":  map[string]any{"schemaVersion": bundle.SchemaVersion, "digest": bundle.Digest, "services": bundle.Services, "links": bundle.Links},
		"target":  map[string]any{"environment": bundle.Target},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, 0, err
	}
	url := strings.TrimRight(endpoint, "/") + path
	httpRequest, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Idempotency-Key", idempotencyKey)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	return result, response.StatusCode, err
}

func waitForOperations(endpoint, token string, responseBody []byte) (int, error) {
	var accepted struct {
		Operations []string `json:"operations"`
	}
	if err := json.Unmarshal(responseBody, &accepted); err != nil {
		return ExitOperation, err
	}
	for _, operationID := range accepted.Operations {
		deadline := time.Now().Add(60 * time.Second)
		for {
			body, status, err := getOperation(endpoint, token, operationID)
			if err != nil {
				return ExitTarget, err
			}
			if status < 200 || status >= 300 {
				return ExitOperation, fmt.Errorf("operation %s returned HTTP %d", operationID, status)
			}
			var operation struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(body, &operation); err != nil {
				return ExitOperation, err
			}
			switch operation.State {
			case "succeeded", "completed":
				break
			case "failed":
				return ExitOperation, nil
			case "degraded":
				return ExitDegraded, nil
			default:
				if time.Now().After(deadline) {
					return ExitOperation, fmt.Errorf("operation %s timed out", operationID)
				}
				time.Sleep(250 * time.Millisecond)
				continue
			}
			break
		}
	}
	return ExitOK, nil
}

func getOperation(endpoint, token, operationID string) ([]byte, int, error) {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(endpoint, "/")+"/api/operations/"+operationID, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return body, response.StatusCode, err
}

func projectID(project string) string {
	body, err := os.ReadFile(filepath.Join(project, "project.yaml"))
	if err != nil {
		return filepath.Base(project)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "id:") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "id:"))
		}
	}
	return filepath.Base(project)
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
