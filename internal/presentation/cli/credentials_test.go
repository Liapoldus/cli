package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/targets"
)

func TestCredentialSetAndStatusNeverEmitOrPersistToken(t *testing.T) {
	registry := targets.Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	if err := registry.Add(domain.Target{Name: "local", Transport: domain.TransportLocalProcess}); err != nil {
		t.Fatal(err)
	}
	secret := "a-test-management-token-that-is-long-enough"
	vault := &memoryCredentialVault{values: make(map[string]string)}
	prompts := [][]byte{[]byte(secret), []byte(secret)}
	runner := Runner{
		Registry: registry, Credentials: vault, CredentialVault: vault,
		PromptSecret: func(string) ([]byte, error) {
			value := prompts[0]
			prompts = prompts[1:]
			return value, nil
		},
	}
	var output bytes.Buffer
	if code := runner.Run([]string{"credential", "set", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("set code=%d output=%s", code, output.String())
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("set output exposed credential: %s", output.String())
	}
	profile, err := os.ReadFile(registry.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(profile), secret) || !strings.Contains(string(profile), "credentialRef") {
		t.Fatalf("target registry leaked token or omitted reference: %s", profile)
	}

	output.Reset()
	if code := runner.Run([]string{"credential", "status", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("status code=%d output=%s", code, output.String())
	}
	var status struct {
		State         string `json:"state"`
		CredentialRef string `json:"credentialRef"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.State != "configured" || status.CredentialRef != "" || strings.Contains(output.String(), secret) || strings.Contains(output.String(), "credentialRef") {
		t.Fatalf("status=%+v output=%s err=%v", status, output.String(), err)
	}
}

func TestStudioCannotSetOrRemoveCredentials(t *testing.T) {
	vault := &memoryCredentialVault{values: make(map[string]string)}
	prompted := false
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Transport: domain.TransportLocalProcess}},
		Credentials: vault, CredentialVault: vault,
		PromptSecret: func(string) ([]byte, error) { prompted = true; return []byte("secret"), nil },
	}
	var output bytes.Buffer
	if code := runner.Run([]string{"credential", "set", "--target", "local", studioEventsFlag}, &output, &output); code == application.ExitOK {
		t.Fatalf("Studio credential write unexpectedly succeeded: %s", output.String())
	}
	if prompted || strings.Contains(output.String(), "secret") {
		t.Fatalf("Studio reached secret prompt or leaked token: prompted=%v output=%s", prompted, output.String())
	}
}

func TestCoreStartCreatesAndStoresTokenOnlyForFreshState(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, false)
	vault := &memoryCredentialVault{values: make(map[string]string)}
	docker := &dockerRuntimeStub{}
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: vault, CredentialVault: vault, Docker: docker,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil },
	}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "start", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("start code=%d output=%s", code, output.String())
	}
	if len(docker.bootstrapToken) != 43 {
		t.Fatalf("bootstrap token length=%d", len(docker.bootstrapToken))
	}
	stored, err := vault.Resolve(context.Background(), target.CredentialRef)
	if err != nil || stored != docker.bootstrapToken {
		t.Fatalf("stored credential does not match bootstrap token: err=%v", err)
	}
	if strings.Contains(output.String(), stored) {
		t.Fatalf("Core start output leaked generated token: %s", output.String())
	}
	if strings.Contains(strings.Join(docker.environment, " "), stored) {
		t.Fatal("bootstrap credential was passed in Docker environment metadata")
	}
	if docker.tokenDirectory == "" {
		t.Fatal("Core container did not receive the one-time token mount")
	}
	if filepath.Dir(docker.tokenDirectory) != filepath.Dir(target.StatePath) {
		t.Fatalf("bootstrap token is outside the mounted Core state directory: token=%q state=%q", docker.tokenDirectory, filepath.Dir(target.StatePath))
	}
	if strings.Contains(strings.Join(docker.environment, " "), "/run/secrets") {
		t.Fatalf("bootstrap token unexpectedly uses a separate host bind mount: %v", docker.environment)
	}
	if _, err := os.Stat(docker.tokenDirectory); !os.IsNotExist(err) {
		t.Fatalf("one-time token directory was not removed: %v", err)
	}
}

func TestCoreStartDoesNotReplaceCredentialForExistingState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "core.sqlite")
	if err := os.WriteFile(statePath, []byte("preserved-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, _ := dockerCoreTestTarget(t, false)
	target.StatePath = statePath
	vault := &memoryCredentialVault{values: make(map[string]string)}
	docker := &dockerRuntimeStub{containers: []string{coreContainer}}
	runner := Runner{Registry: targetRegistryStub{target: target}, Credentials: vault, CredentialVault: vault, Docker: docker,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil }}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "start", "--target", "local"}, &output, &output); code == application.ExitOK || !strings.Contains(output.String(), "Core management credential is unavailable") {
		t.Fatalf("existing state was started without its token: code=%d output=%s", code, output.String())
	}
	if docker.runCount != 0 || len(docker.stopped) != 0 || len(docker.removed) != 0 {
		t.Fatalf("existing Core was modified without its credential: runs=%d stopped=%v removed=%v", docker.runCount, docker.stopped, docker.removed)
	}
	body, err := os.ReadFile(statePath)
	if err != nil || string(body) != "preserved-state" {
		t.Fatalf("state changed: body=%q err=%v", body, err)
	}
}

func TestCoreStartReplacesOnlyTargetOwnedContainerWhenConfiguredBinaryChanges(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	stateBefore, err := os.ReadFile(target.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	coreBinary, err := os.ReadFile(target.CoreBinary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(coreBinary)
	currentDigest := hex.EncodeToString(digest[:])
	label := runtimeLabel("target", target.Name)
	docker := &dockerRuntimeStub{
		containers: []string{coreContainer, "liapoldus-echo", "liapoldus-server"},
		inspectBody: []byte(`[{
			"State":{"Status":"running","Running":true},
			"Config":{"Image":"alpine:latest","Labels":{
				"com.liapoldus.runtime":"` + label + `",
				"com.liapoldus.core.binary.sha256":"` + strings.Repeat("a", 64) + `"
			}}
		}]`),
	}
	vault := &memoryCredentialVault{values: map[string]string{target.CredentialRef: "existing-management-token-which-is-long-enough"}}
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: vault, CredentialVault: vault, Docker: docker,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil },
	}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "start", "--target", "local"}, &output, &output); code != application.ExitOK {
		t.Fatalf("start code=%d output=%s", code, output.String())
	}
	var result struct {
		Created bool `json:"created"`
		Updated bool `json:"updated"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.Created || !result.Updated {
		t.Fatalf("Core update result=%+v output=%s err=%v", result, output.String(), err)
	}
	if len(docker.stopped) != 1 || docker.stopped[0] != coreContainer || len(docker.removed) != 1 || docker.removed[0] != coreContainer {
		t.Fatalf("update affected unexpected containers: stopped=%v removed=%v", docker.stopped, docker.removed)
	}
	if docker.runCount != 1 || docker.coreRequest.CoreBinaryDigest != currentDigest {
		t.Fatalf("updated Core request missing current binary digest: runs=%d request=%+v", docker.runCount, docker.coreRequest)
	}
	stateAfter, err := os.ReadFile(target.StatePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("Core state changed during binary refresh: before=%q after=%q err=%v", stateBefore, stateAfter, err)
	}
}

func TestCoreStartRefusesToReplaceContainerOwnedByAnotherTarget(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	docker := &dockerRuntimeStub{
		containers: []string{coreContainer},
		inspectBody: []byte(`[{"State":{"Status":"running","Running":true},"Config":{"Image":"alpine:latest","Labels":{
			"com.liapoldus.runtime":"` + runtimeLabel("target", "other") + `",
			"com.liapoldus.core.binary.sha256":"` + strings.Repeat("a", 64) + `"
		}}}]`),
	}
	vault := &memoryCredentialVault{values: map[string]string{target.CredentialRef: "existing-management-token-which-is-long-enough"}}
	runner := Runner{Registry: targetRegistryStub{target: target}, Credentials: vault, CredentialVault: vault, Docker: docker,
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return healthyCoreClient{}, nil }}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "start", "--target", "local"}, &output, &output); code == application.ExitOK || !strings.Contains(output.String(), "not owned by the selected target") {
		t.Fatalf("foreign Core container was not rejected: code=%d output=%s", code, output.String())
	}
	if len(docker.stopped) != 0 || len(docker.removed) != 0 || docker.runCount != 0 {
		t.Fatalf("foreign Core container was modified: stopped=%v removed=%v runs=%d", docker.stopped, docker.removed, docker.runCount)
	}
}

func dockerCoreTestTarget(t *testing.T, existingState bool) (domain.Target, string) {
	t.Helper()
	root := t.TempDir()
	previousCache := runtimeUserCacheDir
	runtimeUserCacheDir = func() (string, error) { return filepath.Join(root, "cache"), nil }
	t.Cleanup(func() { runtimeUserCacheDir = previousCache })
	coreBinary := filepath.Join(root, "core-linux-arm64")
	if err := os.WriteFile(coreBinary, []byte("synthetic-core-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "state", "core.sqlite")
	if existingState {
		if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, []byte("existing-state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tlsDir := filepath.Join(root, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"core.crt", "core.key", "ca.pem"} {
		if err := os.WriteFile(filepath.Join(tlsDir, name), []byte("synthetic-tls-asset"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return domain.Target{
		Name: "local", Transport: domain.TransportLocalProcess, Endpoint: "https://127.0.0.1:18180",
		ServerName: "localhost", CAFile: filepath.Join(tlsDir, "ca.pem"),
		ClientCertFile: filepath.Join(tlsDir, "core.crt"), ClientKeyFile: filepath.Join(tlsDir, "core.key"),
		CoreBinary: coreBinary, RuntimeImage: "alpine:latest", RuntimePlatform: "linux/arm64", StatePath: statePath,
		CredentialRef: domain.ManagementCredentialReference("local"),
		Bootstrap: domain.CoreBootstrap{
			ManagementListen: "0.0.0.0:18180", ManagementCertificate: filepath.Join(tlsDir, "core.crt"),
			ManagementKey: filepath.Join(tlsDir, "core.key"), ManagementClientCA: filepath.Join(tlsDir, "ca.pem"),
			ControlListen: "0.0.0.0:18181", ControlPublicURL: "https://localhost:18181",
			ControlCertificate: filepath.Join(tlsDir, "core.crt"), ControlKey: filepath.Join(tlsDir, "core.key"),
			ReplicaClientCA: filepath.Join(tlsDir, "ca.pem"), ReplicaServerCA: filepath.Join(tlsDir, "ca.pem"),
		},
	}, root
}

type healthyCoreClient struct{}

func (healthyCoreClient) Health(context.Context) (int, error) { return 200, nil }
func (healthyCoreClient) Plan(context.Context, domain.ConfigBundleRequest) (domain.PlanResponse, error) {
	return domain.PlanResponse{}, nil
}
func (healthyCoreClient) Apply(context.Context, domain.ConfigBundleRequest, string) (domain.ApplyResponse, error) {
	return domain.ApplyResponse{}, nil
}
func (healthyCoreClient) Operation(context.Context, string) (domain.OperationResponse, error) {
	return domain.OperationResponse{}, nil
}
func (healthyCoreClient) ResolveOperations(context.Context, string, []string) ([]domain.OperationReference, error) {
	return nil, nil
}
func (healthyCoreClient) ServiceMetadata(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (healthyCoreClient) ServiceAdminQuery(context.Context, string, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[]}`), nil
}
func (healthyCoreClient) RegisterService(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (healthyCoreClient) EnsureServiceLink(context.Context, domain.ServiceLinkPolicyRequest, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

type dockerRuntimeStub struct {
	bootstrapToken string
	tokenDirectory string
	environment    []string
	runCount       int
	containers     []string
	stopped        []string
	started        []string
	restarted      []string
	removed        []string
	networkRemoved []string
	inspectBody    []byte
	coreRequest    domain.DockerContainerRequest
}

func (*dockerRuntimeStub) Probe(context.Context) domain.DockerProviderStatus {
	return domain.DockerProviderStatus{ID: "docker", Available: true}
}
func (*dockerRuntimeStub) NetworkCreate(context.Context, string, string) error { return nil }
func (stub *dockerRuntimeStub) NetworkRemove(_ context.Context, name string) error {
	stub.networkRemoved = append(stub.networkRemoved, name)
	return nil
}
func (stub *dockerRuntimeStub) RunContainer(_ context.Context, request domain.DockerContainerRequest) error {
	stub.runCount++
	if request.Name == coreContainer {
		stub.coreRequest = request
	}
	for key, value := range request.Environment {
		stub.environment = append(stub.environment, key+"="+value)
	}
	var stateMount string
	for _, mount := range request.Mounts {
		if mount.Target == "/var/lib/liapoldus" {
			stateMount = mount.Source
			break
		}
	}
	if tokenFile := request.Environment["CORE_INIT_MANAGEMENT_TOKEN_FILE"]; tokenFile != "" {
		const containerStateRoot = "/var/lib/liapoldus/"
		if stateMount == "" || !strings.HasPrefix(tokenFile, containerStateRoot) {
			return errors.New("Core bootstrap token is not scoped to the state mount")
		}
		path := filepath.Join(stateMount, filepath.FromSlash(strings.TrimPrefix(tokenFile, containerStateRoot)))
		stub.tokenDirectory = filepath.Dir(path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		stub.bootstrapToken = string(body)
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	stub.containers = append(stub.containers, request.Name)
	return nil
}
func (stub *dockerRuntimeStub) ListContainers(context.Context, string) ([]string, error) {
	return append([]string(nil), stub.containers...), nil
}
func (stub *dockerRuntimeStub) StopContainer(_ context.Context, name string) error {
	stub.stopped = append(stub.stopped, name)
	return nil
}
func (stub *dockerRuntimeStub) StartContainer(_ context.Context, name string) error {
	stub.started = append(stub.started, name)
	return nil
}
func (stub *dockerRuntimeStub) RestartContainer(_ context.Context, name string) error {
	stub.restarted = append(stub.restarted, name)
	return nil
}
func (stub *dockerRuntimeStub) RemoveContainer(_ context.Context, name string) error {
	stub.removed = append(stub.removed, name)
	return nil
}
func (*dockerRuntimeStub) Logs(context.Context, string) ([]byte, error) { return nil, nil }
func (stub *dockerRuntimeStub) Inspect(context.Context, string) ([]byte, error) {
	if len(stub.inspectBody) != 0 {
		return stub.inspectBody, nil
	}
	return []byte(`[{"State":{"Status":"running","Running":true}}]`), nil
}
func (*dockerRuntimeStub) Exec(context.Context, string, string, []string) ([]byte, error) {
	return nil, nil
}

type memoryCredentialVault struct{ values map[string]string }

func (vault *memoryCredentialVault) Resolve(_ context.Context, reference string) (string, error) {
	value, ok := vault.values[reference]
	if !ok {
		return "", domain.ErrCredentialNotFound
	}
	return value, nil
}
func (vault *memoryCredentialVault) Store(_ context.Context, reference, value string) error {
	vault.values[reference] = value
	return nil
}
func (vault *memoryCredentialVault) Delete(_ context.Context, reference string) error {
	delete(vault.values, reference)
	return nil
}
