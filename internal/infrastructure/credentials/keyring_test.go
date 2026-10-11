package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
	"github.com/zalando/go-keyring"
)

func TestKeyringProviderStoresAndResolvesCredentialWithoutSerializingIt(t *testing.T) {
	backend := newMemoryKeyring()
	provider := KeyringProvider{Backend: backend}
	reference := domain.ManagementCredentialReference("local")
	secret := "example-secret-that-must-never-be-output"

	if err := provider.Store(context.Background(), reference, secret); err != nil {
		t.Fatal(err)
	}
	resolved, err := provider.Resolve(context.Background(), reference)
	if err != nil || resolved != secret {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	if got := backend.values[reference]; got != secret {
		t.Fatalf("stored value=%q", got)
	}
	if err := provider.Delete(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	if _, exists := backend.values[reference]; exists {
		t.Fatal("credential was not removed")
	}
}

func TestKeyringProviderRejectsUnknownReferencesAndFailsClosed(t *testing.T) {
	secret := "do-not-leak-this-token"
	backend := newMemoryKeyring()
	backend.err = errors.New(secret)
	provider := KeyringProvider{Backend: backend}

	if _, err := provider.Resolve(context.Background(), "env:CORE_TOKEN"); err == nil {
		t.Fatal("unknown credential reference was accepted")
	}
	_, err := provider.Resolve(context.Background(), domain.ManagementCredentialReference("local"))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("backend error leaked secret or was ignored: %v", err)
	}
	if _, exists := backend.values[domain.ManagementCredentialReference("local")]; exists {
		t.Fatal("provider created a plaintext fallback")
	}
}

type memoryKeyring struct {
	values map[string]string
	err    error
}

func newMemoryKeyring() *memoryKeyring { return &memoryKeyring{values: make(map[string]string)} }

func (memory *memoryKeyring) Get(_, account string) (string, error) {
	if memory.err != nil {
		return "", memory.err
	}
	value, ok := memory.values[account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (memory *memoryKeyring) Set(_, account, value string) error {
	if memory.err != nil {
		return memory.err
	}
	memory.values[account] = value
	return nil
}

func (memory *memoryKeyring) Delete(_, account string) error {
	if memory.err != nil {
		return memory.err
	}
	delete(memory.values, account)
	return nil
}
