package credentials

import (
	"context"
	"errors"
	"os"
	"strings"
)

// EnvironmentProvider resolves references from an injected environment lookup.
// It is suitable for CI secret variables and keeps the secret value outside
// target profiles, Git, bundles and command output.
type EnvironmentProvider struct {
	Lookup func(string) (string, bool)
}

func (p EnvironmentProvider) Resolve(_ context.Context, reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		return "", errors.New("credential reference is empty")
	}
	lookup := p.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, ok := lookup(reference)
	if !ok || strings.TrimSpace(value) == "" {
		return "", errors.New("credential reference is unavailable")
	}
	return value, nil
}
