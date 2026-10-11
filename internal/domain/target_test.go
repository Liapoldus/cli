package domain

import (
	"strings"
	"testing"
)

func TestManagementCredentialReferenceIsStableAndTargetScoped(t *testing.T) {
	local := ManagementCredentialReference("local")
	if local == "" || local != ManagementCredentialReference("local") {
		t.Fatalf("reference is empty or unstable: %q", local)
	}
	if local == ManagementCredentialReference("production") {
		t.Fatal("different targets share a credential reference")
	}
	if !strings.HasPrefix(local, "os-keyring:v1:core-management:") {
		t.Fatalf("unexpected credential reference format %q", local)
	}
}
