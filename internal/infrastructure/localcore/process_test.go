package localcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestBackupAndRestoreRequireStoppedLocalTarget(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "core.sqlite")
	backup := filepath.Join(root, "backup.sqlite")
	if err := os.WriteFile(state, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Target{Name: "local", Transport: domain.TransportLocalProcess, StatePath: state, PIDPath: filepath.Join(root, "core.pid")}
	manager := ProcessManager{}
	if err := manager.Backup(target, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(target, backup); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(state)
	if err != nil || string(body) != "two" {
		t.Fatalf("state=%q err=%v", body, err)
	}
}

func TestBootstrapEnvironmentContainsOnlyReferences(t *testing.T) {
	bootstrap := domain.CoreBootstrap{
		ManagementCertificate: "/tls/management.crt",
		ManagementKey:         "/tls/management.key",
		ControlCertificate:    "/tls/control.crt",
		ControlKey:            "/tls/control.key",
		ReplicaClientCA:       "/tls/replica-client-ca.pem",
		ReplicaServerCA:       "/tls/replica-server-ca.pem",
		SecretRoot:            "/var/lib/liapoldus/secrets",
	}
	if err := validateBootstrap(bootstrap); err != nil {
		t.Fatal(err)
	}
	environment := bootstrapEnvironment("/state/core.sqlite", bootstrap)
	for _, value := range environment {
		if strings.Contains(value, "token") || strings.Contains(value, "password") {
			t.Fatalf("secret-looking value leaked into bootstrap environment: %q", value)
		}
	}
	if !contains(environment, "CORE_SQLITE_PATH=/state/core.sqlite") || !contains(environment, "CORE_INIT_SECRET_ROOT=/var/lib/liapoldus/secrets") {
		t.Fatalf("bootstrap environment=%v", environment)
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
