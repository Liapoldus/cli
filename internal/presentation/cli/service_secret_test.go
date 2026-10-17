package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/coreapi"
	"github.com/Liapoldus/cli/internal/infrastructure/git"
)

func TestServiceSecretCreateStoresValueOutsideProjectAndEmitsMetadataOnly(t *testing.T) {
	const secretValue = "sensitive-marker-value"
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	statePath := filepath.Join(root, "core", "core.sqlite")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{}}
	var created domain.ServiceSecretReference
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services/server/secrets" || request.Method != http.MethodPost {
			t.Fatalf("unexpected Core request: %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || strings.Contains(string(body), secretValue) {
			t.Fatalf("secret value sent to Core: %s error=%v", body, err)
		}
		if err := json.Unmarshal(body, &created); err != nil || created.Reference == "" || created.Purpose != "http.auth.bearer" || created.Label != "edge token" {
			t.Fatalf("metadata=%+v error=%v", created, err)
		}
		created.ServiceID, created.State, created.CreatedAt = "server", "active", "2026-10-17T00:00:00Z"
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(created)
	}))
	defer server.Close()
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: server.URL, StatePath: statePath, CredentialRef: "core-token-ref", Bootstrap: domain.CoreBootstrap{SecretRoot: filepath.Join(root, "core", "secrets")}}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault,
		TokenReader: strings.NewReader(secretValue + "\n"), Source: git.Repository{},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	args := []string{"service", "secret", "create", "--target", "local", "--service", "server", "--purpose", "http.auth.bearer", "--label", "edge token", "--secret-stdin", "--confirm", studioEventsFlag}
	if code := runner.RunContext(context.Background(), args, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), secretValue) {
		t.Fatalf("secret value leaked into CLI output: %s%s", stdout.String(), stderr.String())
	}
	keyringReference := domain.ServiceSecretCredentialReference("local", "server", created.Reference)
	if value, err := vault.Resolve(context.Background(), keyringReference); err != nil || value != secretValue {
		t.Fatalf("vault value=%q error=%v", value, err)
	}
	stagedPath := filepath.Join(secretRoot, created.Reference)
	if relative, err := filepath.Rel(filepath.Dir(statePath), secretRoot); err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		t.Fatalf("secret runtime mount is inside Core state: %q error=%v", relative, err)
	}
	staged, err := os.ReadFile(stagedPath)
	if err != nil || string(staged) != secretValue {
		t.Fatalf("staged secret=%q error=%v", staged, err)
	}
	info, err := os.Stat(stagedPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged secret permissions=%v error=%v", info.Mode().Perm(), err)
	}
	for _, file := range []string{"project.yaml", "settings.json"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Fatalf("secret operation unexpectedly created Project file %s", file)
		}
	}
	if !strings.Contains(stdout.String(), `"run.completed"`) || !strings.Contains(stdout.String(), created.Reference) {
		t.Fatalf("Studio event did not return safe reference metadata: %s", stdout.String())
	}
}

func TestServiceSecretRevokeRemovesVaultAndStagedValueAfterCoreRevocation(t *testing.T) {
	const reference = "ssr:v1:00112233445566778899aabbccddeeff"
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	project := filepath.Join(root, "project")
	settings := []byte(`{"enabled":false}`)
	writeServiceSecretProject(t, project, settings)
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: "", StatePath: filepath.Join(root, "core", "core.sqlite"), Bootstrap: domain.CoreBootstrap{SecretRoot: filepath.Join(root, "core", "secrets")}}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(secretRoot, reference)
	if err := os.WriteFile(stagedPath, []byte("sensitive-marker-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addStagedServiceSecretReference(target, stagedServiceSecretReference{Reference: reference, ServiceID: "server", Purpose: "http.auth.bearer", Label: "edge token"}); err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{domain.ServiceSecretCredentialReference("local", "server", reference): "sensitive-marker-value"}}
	settingsDigest := secretSettingsDigest(t, settings)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/secrets":
			_ = json.NewEncoder(response).Encode(map[string]any{"items": []domain.ServiceSecretReference{{
				Reference: reference, ServiceID: "server", Purpose: "http.auth.bearer", Label: "edge token", State: "active", CreatedAt: "2026-10-17T00:00:00Z",
			}}})
		case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/status":
			_, _ = response.Write(serviceSecretStatus(settingsDigest, true))
		case request.Method == http.MethodDelete && request.URL.Path == "/api/services/server/secrets/"+reference:
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Core request=%s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	target.Endpoint = server.URL
	target.CredentialRef = "core-token-ref"
	var output bytes.Buffer
	runner := Runner{Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault,
		Source: git.Repository{}, Snapshots: git.Snapshotter{},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
	args := []string{"service", "secret", "revoke", "--target", "local", "--project", project, "--service", "server", "--reference", reference, "--confirm"}
	if code := runner.Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("revoke failed: code=%d output=%s", code, output.String())
	}
	if _, err := vault.Resolve(context.Background(), domain.ServiceSecretCredentialReference("local", "server", reference)); err == nil {
		t.Fatal("revoked secret remained in OS vault")
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("revoked secret remained staged: %v", err)
	}
	if strings.Contains(output.String(), "sensitive-marker-value") {
		t.Fatalf("revoke leaked secret value: %s", output.String())
	}
}

func TestServiceSecretRevokeFailsClosedUntilProjectAndAllReplicasAgree(t *testing.T) {
	const reference = "ssr:v1:00112233445566778899aabbccddeeff"
	for _, test := range []struct {
		name                  string
		settings              []byte
		statusDigest          string
		secondReplicaNotReady bool
	}{
		{name: "saved Project still references secret", settings: []byte(`{"auth":{"secretRef":"` + reference + `"}}`)},
		{name: "replica digest differs", settings: []byte(`{"enabled":false}`), statusDigest: strings.Repeat("b", 64)},
		{name: "one replica has not acknowledged", settings: []byte(`{"enabled":false}`), secondReplicaNotReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			useRuntimeCache(t, filepath.Join(root, "cache"))
			project := filepath.Join(root, "project")
			writeServiceSecretProject(t, project, test.settings)
			target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: "", StatePath: filepath.Join(root, "core", "core.sqlite"), CredentialRef: "core-token-ref", Bootstrap: domain.CoreBootstrap{SecretRoot: filepath.Join(root, "core", "secrets")}}
			secretRoot, err := serviceSecretRoot(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(secretRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := stageServiceSecret(secretRoot, reference, []byte("secret-marker")); err != nil {
				t.Fatal(err)
			}
			if err := addStagedServiceSecretReference(target, stagedServiceSecretReference{Reference: reference, ServiceID: "server", Purpose: "http.auth.bearer", Label: "edge token"}); err != nil {
				t.Fatal(err)
			}
			vault := &memoryCredentialVault{values: map[string]string{domain.ServiceSecretCredentialReference("local", "server", reference): "secret-marker"}}
			coreRevokes, statusReads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/secrets":
					_ = json.NewEncoder(response).Encode(map[string]any{"items": []domain.ServiceSecretReference{{
						Reference: reference, ServiceID: "server", Purpose: "http.auth.bearer", Label: "edge token", State: "active", CreatedAt: "2026-10-17T00:00:00Z",
					}}})
				case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/status":
					statusReads++
					digest := secretSettingsDigest(t, test.settings)
					if test.statusDigest != "" {
						digest = test.statusDigest
					}
					_, _ = response.Write(serviceSecretStatus(digest, !test.secondReplicaNotReady))
				case request.Method == http.MethodDelete:
					coreRevokes++
					response.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected Core request=%s %s", request.Method, request.URL.Path)
				}
			}))
			defer server.Close()
			target.Endpoint = server.URL
			runner := Runner{Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault,
				Source: git.Repository{}, Snapshots: git.Snapshotter{},
				CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
			var output bytes.Buffer
			args := []string{"service", "secret", "revoke", "--target", "local", "--project", project, "--service", "server", "--reference", reference, "--confirm"}
			if code := runner.Run(args, &output, &output); code == application.ExitOK {
				t.Fatalf("unsafe revocation succeeded: %s", output.String())
			}
			if coreRevokes != 0 {
				t.Fatalf("Core revoke was called %d times", coreRevokes)
			}
			if test.secondReplicaNotReady && statusReads != 1 {
				t.Fatalf("expected one Core status read, got %d", statusReads)
			}
			if _, err := vault.Resolve(context.Background(), domain.ServiceSecretCredentialReference("local", "server", reference)); err != nil {
				t.Fatalf("failed revocation removed the OS vault value: %v", err)
			}
		})
	}
}

func writeServiceSecretProject(t *testing.T, root string, settings []byte) {
	t.Helper()
	for path, content := range map[string][]byte{
		"project.yaml":                  []byte("id: secret-retirement-test\n"),
		"services/server/settings.json": settings,
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func secretSettingsDigest(t *testing.T, settings []byte) string {
	t.Helper()
	encoded, err := json.Marshal(json.RawMessage(settings))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func serviceSecretStatus(digest string, secondReplicaReady bool) []byte {
	configuration := func() map[string]any {
		return map[string]any{"state": "acknowledged", "desiredGeneration": 7, "appliedGeneration": 7, "lastFailureCode": ""}
	}
	replica := func(id string, ready bool) map[string]any {
		return map[string]any{
			"replicaId": id, "artifactVersion": "1.2.3", "artifactDigest": "sha256:" + strings.Repeat("a", 64), "ready": ready,
			"identity":      map[string]any{"instanceId": "server", "replicaId": id},
			"readiness":     map[string]any{"instanceId": "server", "replicaId": id, "generation": "generation-7", "pendingGeneration": "", "schemaVersion": "service-readiness/v1", "sha256": digest, "ready": ready},
			"configuration": configuration(),
		}
	}
	encoded, _ := json.Marshal(map[string]any{"instanceId": "server", "replicas": []any{replica("replica-1", true), replica("replica-2", secondReplicaReady)}})
	return encoded
}

func TestServiceSecretCreatePreservesStagedValueWhenCoreRejectsMetadataAndCanRetry(t *testing.T) {
	const secretValue = "secret-that-must-be-cleaned"
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	stateDirectory := filepath.Join(root, "core")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	registered := false
	var stagedReference domain.ServiceSecretReference
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/secrets":
			items := []domain.ServiceSecretReference{}
			if registered {
				items = append(items, stagedReference)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"items": items})
		case request.Method == http.MethodPost && request.URL.Path == "/api/services/server/secrets":
			body, err := io.ReadAll(request.Body)
			if err != nil || strings.Contains(string(body), secretValue) {
				t.Fatalf("secret value sent to Core: %s error=%v", body, err)
			}
			if err := json.Unmarshal(body, &stagedReference); err != nil {
				t.Fatal(err)
			}
			stagedReference.ServiceID = "server"
			stagedReference.CreatedAt = "2026-10-17T00:00:00Z"
			stagedReference.State = "active"
			if registered {
				response.WriteHeader(http.StatusCreated)
				stagedReference.State = "active"
				_ = json.NewEncoder(response).Encode(stagedReference)
				return
			}
			response.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(response, `{"code":"invalid_request","detail":"temporary registration failure"}`)
		default:
			t.Fatalf("unexpected Core request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	vault := &memoryCredentialVault{values: map[string]string{}}
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: server.URL, StatePath: filepath.Join(stateDirectory, "core.sqlite"), CredentialRef: "core-token-ref"}
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault,
		TokenReader: strings.NewReader(secretValue), CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	var output bytes.Buffer
	args := []string{"service", "secret", "create", "--target", "local", "--service", "server", "--purpose", "http.auth.bearer", "--label", "edge token", "--secret-stdin", "--confirm"}
	if code := runner.Run(args, &output, &output); code == application.ExitOK {
		t.Fatal("Core conflict did not reject secret creation")
	}
	if len(vault.values) != 1 {
		t.Fatalf("failed registration did not preserve the OS vault value: %#v", vault.values)
	}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(secretRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != stagedReference.Reference {
		t.Fatalf("failed registration did not preserve staged value: %#v error=%v", entries, err)
	}
	if stagedReference.Reference == "" {
		t.Fatal("Core did not receive the staged opaque reference")
	}
	index, err := readStagedServiceSecretIndex(target)
	if err != nil || len(index.Items) != 1 || index.Items[0].Reference != stagedReference.Reference {
		t.Fatalf("staged metadata was not recoverable: %+v error=%v", index, err)
	}
	indexPath, err := stagedServiceSecretIndexPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("staged reference index was not preserved: %v", err)
	}
	if strings.Contains(output.String(), secretValue) {
		t.Fatalf("failed operation leaked secret value: %s", output.String())
	}

	var listOutput bytes.Buffer
	listArgs := []string{"service", "secret", "list", "--target", "local", "--service", "server", studioEventsFlag}
	if code := runner.RunContext(context.Background(), listArgs, &listOutput, &listOutput); code != application.ExitOK {
		t.Fatalf("list staged registration failed: code=%d output=%s", code, listOutput.String())
	}
	for _, expected := range []string{`"pendingRegistrations":[`, stagedReference.Reference, `"state":"pending"`} {
		if !strings.Contains(listOutput.String(), expected) {
			t.Fatalf("secret catalog omitted retryable registration %q: %s", expected, listOutput.String())
		}
	}

	registered = true
	var retryOutput bytes.Buffer
	retryArgs := []string{"service", "secret", "register", "--target", "local", "--service", "server", "--reference", stagedReference.Reference, "--confirm", studioEventsFlag}
	if code := runner.RunContext(context.Background(), retryArgs, &retryOutput, &retryOutput); code != application.ExitOK {
		t.Fatalf("retry registration failed: code=%d output=%s", code, retryOutput.String())
	}
	if strings.Contains(retryOutput.String(), secretValue) || !strings.Contains(retryOutput.String(), `"service-secret-register-result/v1"`) {
		t.Fatalf("registration retry returned unsafe output: %s", retryOutput.String())
	}
}

func TestServiceSecretRotateStagesNewReferenceWithoutRevokingOld(t *testing.T) {
	const oldReference = "ssr:v1:00112233445566778899aabbccddeeff"
	const secretValue = "rotated-secret-marker"
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	statePath := filepath.Join(root, "core", "core.sqlite")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{}}
	var created domain.ServiceSecretReference
	deleteRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/secrets":
			if purpose := request.URL.Query().Get("purpose"); purpose != "" && purpose != "http.auth.bearer" {
				t.Fatalf("rotation list purpose=%q", request.URL.Query().Get("purpose"))
			}
			items := []domain.ServiceSecretReference{{
				Reference: oldReference, ServiceID: "server", Purpose: "http.auth.bearer", Label: "edge token", State: "active", CreatedAt: "2026-10-17T00:00:00Z",
			}}
			if created.Reference != "" {
				items = append(items, created)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"items": items})
		case request.Method == http.MethodPost && request.URL.Path == "/api/services/server/secrets":
			body, err := io.ReadAll(request.Body)
			if err != nil || strings.Contains(string(body), secretValue) {
				t.Fatalf("rotation value reached Core metadata: %s error=%v", body, err)
			}
			var metadata struct {
				Reference string `json:"reference"`
				Purpose   string `json:"purpose"`
				Label     string `json:"label"`
			}
			if err := json.Unmarshal(body, &metadata); err != nil || metadata.Reference == oldReference || metadata.Purpose != "http.auth.bearer" || metadata.Label != "edge token rotated" {
				t.Fatalf("rotation metadata=%+v error=%v", metadata, err)
			}
			created = domain.ServiceSecretReference{Reference: metadata.Reference, ServiceID: "server", Purpose: metadata.Purpose, Label: metadata.Label, State: "active", CreatedAt: "2026-10-17T00:00:01Z"}
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(created)
		case request.Method == http.MethodDelete:
			deleteRequests++
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Core request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: server.URL, StatePath: statePath, CredentialRef: "core-token-ref", Bootstrap: domain.CoreBootstrap{SecretRoot: filepath.Join(root, "core", "secrets")}}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner := Runner{
		Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault,
		TokenReader: strings.NewReader(secretValue + "\n"), Source: git.Repository{},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	args := []string{"service", "secret", "rotate", "--target", "local", "--service", "server", "--reference", oldReference, "--purpose", "http.auth.bearer", "--label", "edge token rotated", "--secret-stdin", "--confirm", studioEventsFlag}
	if code := runner.RunContext(context.Background(), args, &stdout, &stderr); code != application.ExitOK {
		t.Fatalf("rotate failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if deleteRequests != 0 || created.Reference == "" || strings.Contains(stdout.String()+stderr.String(), secretValue) {
		t.Fatalf("rotation revoked old metadata or leaked value: deletes=%d output=%s%s", deleteRequests, stdout.String(), stderr.String())
	}
	for _, expected := range []string{`"schemaVersion":"service-secret-rotation-result/v1"`, oldReference, created.Reference, `"state":"staged"`, `"run.completed"`} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("rotation output omitted %q: %s", expected, stdout.String())
		}
	}
	if value, err := vault.Resolve(context.Background(), domain.ServiceSecretCredentialReference("local", "server", created.Reference)); err != nil || value != secretValue {
		t.Fatalf("new vault value=%q error=%v", value, err)
	}
	if _, err := os.Stat(filepath.Join(secretRoot, created.Reference)); err != nil {
		t.Fatalf("new reference was not staged for local Core: %v", err)
	}
	var listOutput, listError bytes.Buffer
	listArgs := []string{"service", "secret", "list", "--target", "local", "--service", "server", studioEventsFlag}
	if code := runner.RunContext(context.Background(), listArgs, &listOutput, &listError); code != application.ExitOK {
		t.Fatalf("list staged rotation failed: code=%d stdout=%s stderr=%s", code, listOutput.String(), listError.String())
	}
	for _, expected := range []string{`"pendingRotations":[`, oldReference, created.Reference, `"state":"staged"`} {
		if !strings.Contains(listOutput.String(), expected) {
			t.Fatalf("secret list omitted persisted rotation metadata %q: %s", expected, listOutput.String())
		}
	}

	index, err := readStagedServiceSecretIndex(target)
	if err != nil {
		t.Fatalf("rotation metadata did not survive reload: %v", err)
	}
	var found bool
	for _, item := range index.Items {
		if item.Reference == created.Reference {
			found = item.RotationFrom == oldReference && item.ServiceID == "server" && item.Purpose == "http.auth.bearer"
		}
	}
	if !found {
		t.Fatalf("persisted index did not retain the staged rotation: %+v", index.Items)
	}
	if err := removeStagedServiceSecretReference(target, oldReference); err != nil {
		t.Fatalf("cleanup after successful old-reference revoke: %v", err)
	}
	index, err = readStagedServiceSecretIndex(target)
	if err != nil {
		t.Fatalf("rotation metadata could not be reloaded after old-reference revoke: %v", err)
	}
	for _, item := range index.Items {
		if item.Reference == created.Reference && item.RotationFrom != "" {
			t.Fatalf("completed rotation remained pending after old-reference revoke: %+v", item)
		}
	}
}

func TestMaterializeServiceSecretMountUsesOnlyVaultReferences(t *testing.T) {
	const reference = "ssr:v1:00112233445566778899aabbccddeeff"
	const stale = "ssr:v1:ffeeddccbbaa99887766554433221100"
	const secretValue = "runtime-only-secret-marker"
	root := t.TempDir()
	useRuntimeCache(t, filepath.Join(root, "cache"))
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, StatePath: filepath.Join(root, "core", "core.sqlite")}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := addStagedServiceSecretReference(target, stagedServiceSecretReference{Reference: reference, ServiceID: "server", Purpose: "server.http.auth.credential", Label: "edge credential"}); err != nil {
		t.Fatal(err)
	}
	if err := stageServiceSecret(secretRoot, stale, []byte("stale-mount-value")); err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{
		domain.ServiceSecretCredentialReference("local", "server", reference): secretValue,
	}}
	runner := Runner{CredentialVault: vault}
	if err := runner.materializeServiceSecretMount(context.Background(), target, true); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(secretRoot, reference))
	if err != nil || string(actual) != secretValue {
		t.Fatalf("materialized secret=%q error=%v", actual, err)
	}
	if _, err := os.Stat(filepath.Join(secretRoot, stale)); !os.IsNotExist(err) {
		t.Fatalf("unindexed stale value survived runtime rematerialization: %v", err)
	}
	indexPath, err := stagedServiceSecretIndexPath(target)
	if err != nil {
		t.Fatal(err)
	}
	indexContents, err := os.ReadFile(indexPath)
	if err != nil || strings.Contains(string(indexContents), secretValue) {
		t.Fatalf("secret value reached local reference metadata: %s error=%v", indexContents, err)
	}
	if err := os.Remove(filepath.Join(secretRoot, reference)); err != nil {
		t.Fatal(err)
	}
	if err := runner.materializeServiceSecretMount(context.Background(), target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secretRoot, reference)); err != nil {
		t.Fatalf("missing runtime material was not restored from the vault: %v", err)
	}
}

func useRuntimeCache(t *testing.T, path string) {
	t.Helper()
	previous := runtimeUserCacheDir
	runtimeUserCacheDir = func() (string, error) { return path, nil }
	t.Cleanup(func() { runtimeUserCacheDir = previous })
}
