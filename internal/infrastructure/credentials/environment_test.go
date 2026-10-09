package credentials

import (
	"context"
	"strings"
	"testing"
)

func TestEnvironmentProviderResolvesReferenceWithoutPersistingValue(t *testing.T) {
	provider := EnvironmentProvider{Lookup: func(reference string) (string, bool) {
		if reference == "CORE_TOKEN" {
			return "secret-value", true
		}
		return "", false
	}}
	value, err := provider.Resolve(context.Background(), "CORE_TOKEN")
	if err != nil || value != "secret-value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	_, err = provider.Resolve(context.Background(), "MISSING")
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("provider error leaked a resolved credential")
	}
}
