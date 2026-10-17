package targets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestRegistryAddAndResolveRelativePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config", "targets.json")
	registry := Registry{Path: path}
	target := domain.Target{Name: "local", Environment: "local", Transport: domain.TransportLocalProcess, StatePath: "state/core.sqlite", CAFile: "tls/ca.pem"}
	if err := registry.Add(target); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Find("local")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(path), "state", "core.sqlite")
	if resolved.StatePath != expected {
		t.Fatalf("state path=%q expected=%q", resolved.StatePath, expected)
	}
	if resolved.CAFile != filepath.Join(filepath.Dir(path), "tls", "ca.pem") {
		t.Fatalf("CA path=%q", resolved.CAFile)
	}
	if resolved.CredentialRef != domain.ManagementCredentialReference("local") {
		t.Fatalf("credential reference=%q", resolved.CredentialRef)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	target.StatePath = "state/updated.sqlite"
	if err := registry.Add(target); err != nil {
		t.Fatal("expected target replacement to succeed:", err)
	}
	updated, err := registry.Find("local")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(updated.StatePath, filepath.Join("state", "updated.sqlite")) {
		t.Fatalf("replacement state path=%q", updated.StatePath)
	}
}

func TestRegistryDoesNotAcceptCredentialReferenceForAnotherTarget(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	target := domain.Target{
		Name: "local", Transport: domain.TransportLocalProcess,
		CredentialRef: domain.ManagementCredentialReference("production"),
	}
	if err := registry.Add(target); err == nil || !strings.Contains(err.Error(), "invalid credential reference") {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryRejectsRemoteTargetWithoutEndpoint(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	err := registry.Add(domain.Target{Name: "production", Transport: domain.TransportRemoteAPI})
	if err == nil || !strings.Contains(err.Error(), "requires an endpoint") {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryRejectsInvalidEndpoint(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	err := registry.Add(domain.Target{Name: "production", Transport: domain.TransportRemoteAPI, Endpoint: "core.example"})
	if err == nil || !strings.Contains(err.Error(), "invalid HTTP(S) endpoint") {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryRejectsCredentialBearingEndpoint(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	for _, endpoint := range []string{
		"https://user:password@core.example.test",
		"https://core.example.test?token=secret",
		"https://core.example.test/#secret",
	} {
		err := registry.Add(domain.Target{
			Name: "production", Transport: domain.TransportRemoteAPI, Endpoint: endpoint,
			ClientCertFile: "/tls/client.crt", ClientKeyFile: "/tls/client.key",
		})
		if err == nil || !strings.Contains(err.Error(), "must not contain credentials") {
			t.Errorf("endpoint %q error=%v", endpoint, err)
		}
	}
}

func TestRegistryRejectsHTTPSWithoutClientCertificate(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	err := registry.Add(domain.Target{Name: "production", Transport: domain.TransportRemoteAPI, Endpoint: "https://core.example"})
	if err == nil || !strings.Contains(err.Error(), "requires client certificate and key") {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryRejectsTrafficReportOutsideProject(t *testing.T) {
	registry := Registry{Path: filepath.Join(t.TempDir(), "targets.json")}
	err := registry.Add(domain.Target{Name: "local", Transport: domain.TransportLocalProcess, TrafficReportPath: "../traffic.json"})
	if err == nil || !strings.Contains(err.Error(), "escapes the project") {
		t.Fatalf("error=%v", err)
	}
}
