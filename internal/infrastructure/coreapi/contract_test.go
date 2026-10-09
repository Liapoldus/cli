package coreapi

import (
	"os"
	"strings"
	"testing"
)

func TestCanonicalCoreManagementContract(t *testing.T) {
	path := os.Getenv("LIAPOLDUS_CORE_OPENAPI")
	if path == "" {
		t.Skip("LIAPOLDUS_CORE_OPENAPI is not set")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contract := string(body)
	required := []string{
		"/api/config-bundles/plan:",
		"operationId: planConfigBundle",
		"/api/config-bundles/apply:",
		"operationId: applyConfigBundle",
		"/api/operations/{operationId}",
		"operationId: getOperation",
		"Idempotency-Key",
		"ConfigBundleRequest:",
	}
	for _, marker := range required {
		if !strings.Contains(contract, marker) {
			t.Errorf("canonical Core contract is missing %q", marker)
		}
	}
}
