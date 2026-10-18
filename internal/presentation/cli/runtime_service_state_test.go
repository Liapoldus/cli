package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/artifacts"
)

type runtimeActionDocker struct {
	domain.DockerProvider
	state string
	logs  []byte
}

type runningRuntimeActionDocker struct{ *dockerRuntimeStub }

type readyServiceCoreClient struct{ healthyCoreClient }

type sequenceServiceCoreClient struct {
	healthyCoreClient
	statuses []json.RawMessage
	calls    int
}

type artifactRuntimeCoreClient struct {
	healthyCoreClient
	status        json.RawMessage
	registrations []domain.ServiceRegistrationMetadata
}

func (client *artifactRuntimeCoreClient) ServiceMetadata(_ context.Context, serviceID, view string) (json.RawMessage, error) {
	if view != "status" || !json.Valid(client.status) {
		return nil, errors.New("unexpected service metadata request")
	}
	var status struct {
		InstanceID string `json:"instanceId"`
	}
	if json.Unmarshal(client.status, &status) != nil || status.InstanceID != serviceID {
		return nil, errors.New("unexpected service id")
	}
	return append(json.RawMessage(nil), client.status...), nil
}

func (client *artifactRuntimeCoreClient) RegisterService(_ context.Context, metadata domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
	client.registrations = append(client.registrations, metadata)
	client.status, _ = json.Marshal(map[string]any{
		"instanceId": metadata.InstanceID, "artifactId": metadata.ArtifactID,
		"artifactVersion": metadata.ArtifactVersion, "artifactDigest": metadata.Digest,
		"replicas": []map[string]any{{"replicaId": metadata.ReplicaID, "ready": true}},
	})
	return json.RawMessage(`{"accepted":true}`), nil
}

type artifactReplacementDocker struct {
	*dockerRuntimeStub
	requests  []domain.DockerContainerRequest
	runErrors []error
}

func (docker *artifactReplacementDocker) RunContainer(_ context.Context, request domain.DockerContainerRequest) error {
	docker.requests = append(docker.requests, request)
	if len(docker.runErrors) == 0 {
		return nil
	}
	err := docker.runErrors[0]
	docker.runErrors = docker.runErrors[1:]
	return err
}

type runtimeStatusDocker struct {
	runtimeActionDocker
	names     []string
	inspected map[string][]byte
}

func (readyServiceCoreClient) ServiceMetadata(_ context.Context, serviceID, view string) (json.RawMessage, error) {
	if view != "status" {
		return nil, errors.New("unexpected metadata view")
	}
	return json.RawMessage(`{"instanceId":"` + serviceID + `","replicas":[{"replicaId":"` + serviceID + `","ready":true}]}`), nil
}

func (client *sequenceServiceCoreClient) ServiceMetadata(_ context.Context, serviceID, view string) (json.RawMessage, error) {
	if view != "status" || len(client.statuses) == 0 {
		return nil, errors.New("unexpected service metadata request")
	}
	index := client.calls
	if index >= len(client.statuses) {
		index = len(client.statuses) - 1
	}
	client.calls++
	return client.statuses[index], nil
}

func (docker runtimeActionDocker) Probe(context.Context) domain.DockerProviderStatus {
	return domain.DockerProviderStatus{ID: "docker", Available: true}
}

func (docker runtimeActionDocker) ListContainers(context.Context, string) ([]string, error) {
	return []string{"liapoldus-echo"}, nil
}

func (docker runtimeStatusDocker) ListContainers(context.Context, string) ([]string, error) {
	return docker.names, nil
}

func (docker runtimeStatusDocker) Inspect(_ context.Context, name string) ([]byte, error) {
	return docker.inspected[name], nil
}

func (runtimeActionDocker) StartContainer(context.Context, string) error   { return nil }
func (runtimeActionDocker) StopContainer(context.Context, string) error    { return nil }
func (runtimeActionDocker) RestartContainer(context.Context, string) error { return nil }

func (docker runtimeActionDocker) Inspect(_ context.Context, name string) ([]byte, error) {
	return []byte(`[{"Name":"/` + name + `","Config":{"Image":"alpine:latest"},"State":{"Status":"` + docker.state + `"}}]`), nil
}

func (runningRuntimeActionDocker) Inspect(_ context.Context, name string) ([]byte, error) {
	return []byte(`[{"Name":"/` + name + `","Config":{"Image":"alpine:latest"},"State":{"Status":"running"}}]`), nil
}

func (docker runtimeActionDocker) Logs(context.Context, string) ([]byte, error) {
	return docker.logs, nil
}

func TestRuntimeServiceRestartFailsWhenContainerExits(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	runner := Runner{
		Registry:  targetRegistryStub{target: target},
		Artifacts: artifacts.Registry{},
		Docker: runtimeActionDocker{
			state: "exited",
			logs:  []byte("Server bootstrap error: service SDK Core replica lifecycle unavailable\n"),
		},
	}
	var output strings.Builder
	code := runner.runtimeOperation(context.Background(), "restart", []string{"--target", "local", "--service", "echo"}, &output, &output)
	if code == application.ExitOK {
		t.Fatalf("restart reported success although the container exited: %s", output.String())
	}
	if !strings.Contains(output.String(), "did not remain running after restart") ||
		!strings.Contains(output.String(), "Server bootstrap error: service SDK Core replica lifecycle unavailable") {
		t.Fatalf("restart omitted actionable failure and bounded logs: %s", output.String())
	}
}

func TestRuntimeServiceRestartReportsRunningContainerState(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	runner := Runner{
		Registry:    targetRegistryStub{target: target},
		Credentials: credentialProviderStub{value: "management-token"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return readyServiceCoreClient{}, nil },
		Artifacts:   artifacts.Registry{},
		Docker:      runtimeActionDocker{state: "running"},
	}
	var output strings.Builder
	code := runner.runtimeOperation(context.Background(), "restart", []string{"--target", "local", "--service", "echo"}, &output, &output)
	if code != application.ExitOK || !strings.Contains(output.String(), `"containerState":"running"`) {
		t.Fatalf("restart did not report the verified container state: code=%d output=%s", code, output.String())
	}
}

func TestServiceStatusReadyRequiresEveryCoreReplica(t *testing.T) {
	ready, err := serviceStatusReady(json.RawMessage(`{"instanceId":"server","replicas":[{"replicaId":"server","ready":true},{"replicaId":"server-2","ready":true}]}`), "server", "server")
	if err != nil || !ready {
		t.Fatalf("all-ready status = %v, %v", ready, err)
	}
	ready, err = serviceStatusReady(json.RawMessage(`{"instanceId":"server","replicas":[{"replicaId":"server","ready":true},{"replicaId":"server-2","ready":false}]}`), "server", "server")
	if err != nil || ready {
		t.Fatalf("mixed readiness status = %v, %v; want not ready", ready, err)
	}
	if _, err := serviceStatusReady(json.RawMessage(`{"instanceId":"echo","replicas":[{"replicaId":"server","ready":true}]}`), "server", "server"); err == nil {
		t.Fatal("status for another service was accepted")
	}
	ready, err = serviceStatusReady(json.RawMessage(`{"instanceId":"server","replicas":[{"replicaId":"server-2","ready":true}]}`), "server", "server")
	if err != nil || ready {
		t.Fatalf("status without restarted replica = %v, %v; want not ready", ready, err)
	}
}

func TestRuntimeServiceWaitsForCoreReplicaReadiness(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	core := &sequenceServiceCoreClient{statuses: []json.RawMessage{
		json.RawMessage(`{"instanceId":"server","replicas":[{"replicaId":"server","ready":false}]}`),
		json.RawMessage(`{"instanceId":"server","replicas":[{"replicaId":"server","ready":true},{"replicaId":"server-2","ready":true}]}`),
	}}
	runner := Runner{
		Registry:    targetRegistryStub{target: target},
		Credentials: credentialProviderStub{value: "management-token"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
	}
	if err := runner.waitForCoreServiceReady(context.Background(), commandOptions{target: "local"}, "server"); err != nil {
		t.Fatalf("readiness wait failed: %v", err)
	}
	if core.calls != 2 {
		t.Fatalf("Core status queried %d times, want one not-ready and one ready response", core.calls)
	}
}

func TestStartReplacementReconcilesOnlyStoppedServiceToExactProjectArtifact(t *testing.T) {
	runner, docker, core, target, desired, old := replacementRuntimeFixture(t, nil)
	var output strings.Builder
	code := runner.replaceStoppedRuntimeService(context.Background(), "server", "liapoldus-server", "studio-local", commandOptions{target: "local"}, target, desired, old.Version, old.Digest, core, &output, &output)
	if code != application.ExitOK {
		t.Fatalf("replacement failed: code=%d output=%s", code, output.String())
	}
	if len(docker.removed) != 1 || docker.removed[0] != "liapoldus-server" || len(docker.requests) != 1 || docker.requests[0].Name != "liapoldus-server" {
		t.Fatalf("replacement escaped Server scope: removed=%v requests=%+v", docker.removed, docker.requests)
	}
	if len(core.registrations) != 1 || core.registrations[0].Digest != desired.Artifact.Digest || core.registrations[0].ArtifactVersion != desired.Artifact.Version {
		t.Fatalf("Core did not receive the exact desired registration: %+v", core.registrations)
	}
	if !strings.Contains(output.String(), `"artifactReplaced":true`) || !strings.Contains(output.String(), desired.Artifact.Digest) {
		t.Fatalf("typed completion omitted replacement provenance: %s", output.String())
	}
}

func TestStartReplacementRestoresPreviousTrustedArtifactOnContainerFailure(t *testing.T) {
	runner, docker, core, target, desired, old := replacementRuntimeFixture(t, []error{errors.New("candidate failed"), nil})
	var output strings.Builder
	code := runner.replaceStoppedRuntimeService(context.Background(), "server", "liapoldus-server", "studio-local", commandOptions{target: "local"}, target, desired, old.Version, old.Digest, core, &output, &output)
	if code == application.ExitOK {
		t.Fatalf("failed candidate was reported successful: %s", output.String())
	}
	if len(docker.removed) != 2 || len(docker.requests) != 2 || docker.requests[0].StagedBinary == docker.requests[1].StagedBinary {
		t.Fatalf("rollback did not replace only the failed candidate with the previous binary: removed=%v requests=%+v", docker.removed, docker.requests)
	}
	if len(core.registrations) != 1 || core.registrations[0].Digest != old.Digest {
		t.Fatalf("rollback did not restore Core to the previous registered artifact: %+v", core.registrations)
	}
	if !strings.Contains(output.String(), "previous container was restored") {
		t.Fatalf("rollback result is not actionable: %s", output.String())
	}
}

func TestPreviousArtifactForStoppedServiceComesFromVerifiedReadOnlyMount(t *testing.T) {
	root := t.TempDir()
	previousCache := runtimeUserCacheDir
	runtimeUserCacheDir = func() (string, error) { return filepath.Join(root, "cache"), nil }
	t.Cleanup(func() { runtimeUserCacheDir = previousCache })
	target := domain.Target{Name: "local"}
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("verified-old-server")
	digest := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(binary))
	source := filepath.Join(staging, "server-"+strings.TrimPrefix(digest, "sha256:"))
	if err := os.WriteFile(source, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	trusted := domain.Artifact{ID: "server", Version: "1.1.5", Platform: "linux/arm64", Digest: digest, Trusted: true}
	inspect, err := json.Marshal([]any{map[string]any{
		"Name":   "/liapoldus-server",
		"Config": map[string]any{"Labels": map[string]string{"com.liapoldus.runtime": "studio-local"}},
		"Mounts": []map[string]any{{"Source": source, "Destination": "/liapoldus/server", "RW": false}},
		"State":  map[string]any{"Status": "exited", "Running": false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := serviceArtifactFromContainer(inspect, "liapoldus-server", "studio-local", target, "server", "linux/arm64", []domain.Artifact{trusted})
	if err != nil || got.Version != trusted.Version || got.Digest != digest {
		t.Fatalf("previous artifact = %+v, %v; want verified trusted artifact", got, err)
	}

	rollbackSource := filepath.Join(staging, "server-rollback-"+strings.TrimPrefix(digest, "sha256:"))
	if err := os.WriteFile(rollbackSource, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	inspect, err = json.Marshal([]any{map[string]any{
		"Name":   "/liapoldus-server",
		"Config": map[string]any{"Labels": map[string]string{"com.liapoldus.runtime": "studio-local"}},
		"Mounts": []map[string]any{{"Source": rollbackSource, "Destination": "/liapoldus/server", "RW": false}},
		"State":  map[string]any{"Status": "exited", "Running": false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = serviceArtifactFromContainer(inspect, "liapoldus-server", "studio-local", target, "server", "linux/arm64", []domain.Artifact{trusted})
	if err != nil || got.Version != trusted.Version || got.Digest != digest {
		t.Fatalf("rollback artifact = %+v, %v; want verified trusted artifact", got, err)
	}

	trusted.Trusted = false
	if _, err := serviceArtifactFromContainer(inspect, "liapoldus-server", "studio-local", target, "server", "linux/arm64", []domain.Artifact{trusted}); err == nil {
		t.Fatal("untrusted old artifact was accepted for rollback")
	}
}

func replacementRuntimeFixture(t *testing.T, runErrors []error) (Runner, *artifactReplacementDocker, *artifactRuntimeCoreClient, domain.Target, domain.RuntimeServiceArtifact, domain.Artifact) {
	t.Helper()
	root := t.TempDir()
	previousCache := runtimeUserCacheDir
	runtimeUserCacheDir = func() (string, error) { return filepath.Join(root, "cache"), nil }
	t.Cleanup(func() { runtimeUserCacheDir = previousCache })
	tlsDirectory := filepath.Join(root, "tls")
	if err := os.MkdirAll(tlsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := "spiffe://liapoldus/service/server/server/server-incarnation"
	writeRuntimeIdentityPair(t, tlsDirectory, "server.crt", "server.key", identity)
	writeRuntimeIdentityPair(t, tlsDirectory, "server-peer.crt", "server-peer.key", identity)
	if err := os.WriteFile(filepath.Join(tlsDirectory, "revoked.pem"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactRegistry := artifacts.Registry{Path: filepath.Join(root, "artifacts.json")}
	makeArtifact := func(version string) domain.Artifact {
		path := filepath.Join(root, "server-"+version)
		if err := os.WriteFile(path, []byte("binary-"+version), 0o700); err != nil {
			t.Fatal(err)
		}
		digest, err := digestFile(path)
		if err != nil {
			t.Fatal(err)
		}
		artifact := domain.Artifact{ID: "server", Version: version, Platform: "linux/arm64", Digest: digest, Path: path}
		if err := artifactRegistry.Register(artifact); err != nil {
			t.Fatal(err)
		}
		if err := artifactRegistry.Trust(artifact.ID, artifact.Version, artifact.Platform, artifact.Digest); err != nil {
			t.Fatal(err)
		}
		artifact.Trusted = true
		return artifact
	}
	old := makeArtifact("1.1.5")
	current := makeArtifact("1.1.6")
	core := &artifactRuntimeCoreClient{}
	core.status, _ = json.Marshal(map[string]any{
		"instanceId": "server", "artifactId": "server", "artifactVersion": old.Version,
		"artifactDigest": old.Digest, "replicas": []map[string]any{{"replicaId": "server", "ready": true}},
	})
	docker := &artifactReplacementDocker{dockerRuntimeStub: &dockerRuntimeStub{}, runErrors: append([]error(nil), runErrors...)}
	target := domain.Target{
		Name: "local", Transport: domain.TransportLocalProcess, Endpoint: "https://localhost:18180", CredentialRef: domain.ManagementCredentialReference("local"), CAFile: caFile, RuntimeImage: "runtime:local", RuntimePlatform: "linux/arm64",
		Bootstrap: domain.CoreBootstrap{ControlListen: "0.0.0.0:18181"},
		Runtime:   domain.RuntimeConfig{TLSDirectory: tlsDirectory, CoreServerName: "localhost", PeerPort: 9643},
	}
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "management-token"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
		Artifacts:   artifactRegistry, Docker: docker,
	}
	desired := domain.RuntimeServiceArtifact{Artifact: current, Command: "server", ControlEndpoint: "https://server:9443", PeerEndpoint: "https://server:9643"}
	return runner, docker, core, target, desired, old
}

func TestRuntimeStatusReportsInspectedContainerState(t *testing.T) {
	target, _ := dockerCoreTestTarget(t, true)
	runner := Runner{
		Registry:  targetRegistryStub{target: target},
		Artifacts: artifacts.Registry{},
		Docker: runtimeStatusDocker{
			names: []string{"liapoldus-core", "liapoldus-server"},
			inspected: map[string][]byte{
				"liapoldus-core":   []byte(`[{"Name":"/liapoldus-core","Config":{"Image":"core:local"},"State":{"Status":"running","Health":{"Status":"healthy"}},"ConfigEnv":["TOKEN=must-not-leak"]}]`),
				"liapoldus-server": []byte(`[{"Name":"/liapoldus-server","Config":{"Image":"runtime:local"},"State":{"Status":"exited"}}]`),
			},
		},
	}
	var output strings.Builder
	code := runner.runtimeOperation(context.Background(), "status", []string{"--target", "local"}, &output, &output)
	body := output.String()
	if code != application.ExitOK || !strings.Contains(body, `"name":"liapoldus-core","image":"core:local","state":"running","health":"healthy"`) || !strings.Contains(body, `"name":"liapoldus-server","image":"runtime:local","state":"exited"`) {
		t.Fatalf("runtime status omitted actual container states: code=%d output=%s", code, body)
	}
	if strings.Contains(body, "must-not-leak") || strings.Contains(body, "ConfigEnv") {
		t.Fatalf("runtime status exposed non-allowlisted inspect fields: %s", body)
	}
}
