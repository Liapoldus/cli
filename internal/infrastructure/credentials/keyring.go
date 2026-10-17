package credentials

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
	"github.com/zalando/go-keyring"
)

const keyringService = "liapoldus-cli"

type Backend interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}

type SystemBackend struct{}

func (SystemBackend) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (SystemBackend) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

func (SystemBackend) Delete(service, account string) error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// KeyringProvider is the only persistent credential provider. It never falls
// back to environment variables or files.
type KeyringProvider struct {
	Backend Backend
}

func (provider KeyringProvider) backend() Backend {
	if provider.Backend != nil {
		return provider.Backend
	}
	return SystemBackend{}
}

func (provider KeyringProvider) Resolve(ctx context.Context, reference string) (string, error) {
	if err := validateReference(reference); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := provider.backend().Get(keyringService, reference)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", domain.ErrCredentialNotFound
	}
	if err != nil || strings.TrimSpace(value) == "" {
		return "", domain.ErrCredentialStore
	}
	return value, nil
}

func (provider KeyringProvider) Store(ctx context.Context, reference, value string) error {
	if err := validateReference(reference); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return errors.New("credential value is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := provider.backend().Set(keyringService, reference, value); err != nil {
		return domain.ErrCredentialStore
	}
	return nil
}

func (provider KeyringProvider) Delete(ctx context.Context, reference string) error {
	if err := validateReference(reference); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := provider.backend().Delete(keyringService, reference); err != nil {
		return domain.ErrCredentialStore
	}
	return nil
}

func validateReference(reference string) error {
	const managementPrefix = "os-keyring:v1:core-management:"
	const serviceSecretPrefix = "os-keyring:v1:service-secret:"
	prefix := ""
	switch {
	case strings.HasPrefix(reference, managementPrefix):
		prefix = managementPrefix
	case strings.HasPrefix(reference, serviceSecretPrefix):
		prefix = serviceSecretPrefix
	default:
		return errors.New("unsupported credential reference")
	}
	encoded := strings.TrimPrefix(reference, prefix)
	if len(encoded) != 64 {
		return errors.New("invalid credential reference")
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return errors.New("invalid credential reference")
	}
	return nil
}
