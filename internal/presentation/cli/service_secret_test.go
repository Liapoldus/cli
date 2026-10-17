package cli

import (
	"bytes"
	"context"
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
	statePath := filepath.Join(root, "core", "core.sqlite")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	secretRoot := filepath.Join(root, "core", "secrets")
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
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: server.URL, StatePath: statePath, CredentialRef: "core-token-ref", Bootstrap: domain.CoreBootstrap{SecretRoot: secretRoot}}
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
	secretRoot := filepath.Join(root, "core", "secrets")
	if err := os.MkdirAll(secretRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(secretRoot, reference)
	if err := os.WriteFile(stagedPath, []byte("sensitive-marker-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{domain.ServiceSecretCredentialReference("local", "server", reference): "sensitive-marker-value"}}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.URL.Path != "/api/services/server/secrets/"+reference {
			t.Fatalf("Core revoke request=%s %s", request.Method, request.URL.Path)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, Endpoint: server.URL, StatePath: filepath.Join(root, "core", "core.sqlite"), CredentialRef: "core-token-ref", Bootstrap: domain.CoreBootstrap{SecretRoot: secretRoot}}
	var output bytes.Buffer
	runner := Runner{Registry: targetRegistryStub{target: target}, Credentials: credentialProviderStub{value: "core-token"}, CredentialVault: vault, CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) }}
	args := []string{"service", "secret", "revoke", "--target", "local", "--service", "server", "--reference", reference, "--confirm"}
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

func TestServiceSecretCreateRollsBackVaultAndFileWhenCoreRejectsMetadata(t *testing.T) {
	secretValue := "secret-that-must-be-cleaned"
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "core")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(response, `{"code":"invalid_request","detail":"reference conflict"}`)
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
	if len(vault.values) != 0 {
		t.Fatalf("failed secret metadata left values in the vault: %#v", vault.values)
	}
	if entries, err := os.ReadDir(filepath.Join(stateDirectory, "secrets")); err == nil && len(entries) != 0 {
		t.Fatalf("failed secret metadata left staged values: %#v", entries)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), secretValue) {
		t.Fatalf("failed operation leaked secret value: %s", output.String())
	}
}
