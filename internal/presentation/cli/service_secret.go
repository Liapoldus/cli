package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

const serviceSecretMaximumBytes = 16 << 10

func (r Runner) serviceSecret(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "create" && args[0] != "revoke") {
		return fail(stderr, application.ExitArguments, "usage: liapoldus service secret list|create|revoke --target local --service <id>")
	}
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target != "local" || options.service == "" {
		return fail(stderr, application.ExitArguments, "service secret operations require --target local and --service")
	}
	registry := r.registry(options.targets)
	if registry == nil {
		return fail(stderr, application.ExitTarget, "target registry is not configured")
	}
	target, err := registry.Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if target.Name != "local" || target.Transport != domain.TransportLocalProcess {
		return fail(stderr, application.ExitArguments, "service secret operations are available only for the local Core target")
	}
	client, err := r.runtimeCoreClient(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	secretClient, ok := client.(domain.ServiceSecretReferenceClient)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support typed service secret references")
	}
	switch args[0] {
	case "list":
		if options.label != "" || options.reference != "" || options.secretStdin || options.confirm || options.replace || options.tokenStdin || options.endpoint != "" {
			return fail(stderr, application.ExitArguments, "service secret list accepts only --target, --service and optional --purpose")
		}
		items, err := secretClient.ListServiceSecretReferences(ctx, options.service, options.purpose)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"schemaVersion": "service-secret-references/v1", "target": target.Name, "serviceId": options.service, "items": items})
	case "create":
		return r.createServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	case "revoke":
		return r.revokeServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	default:
		return fail(stderr, application.ExitArguments, "unsupported service secret operation")
	}
}

func (r Runner) createServiceSecret(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, stdout, stderr io.Writer) int {
	if !options.confirm || options.purpose == "" || strings.TrimSpace(options.label) == "" || options.reference != "" || options.tokenStdin || options.endpoint != "" || options.replace {
		return fail(stderr, application.ExitArguments, "service secret create requires --purpose, --label and --confirm")
	}
	if studioEventsEnabled(ctx) && !options.secretStdin {
		return fail(stderr, application.ExitArguments, "Studio secret creation requires secure stdin input")
	}
	vault := r.credentialVault()
	if vault == nil {
		return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
	}
	value, err := r.readServiceSecret(options)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	defer clear(value)
	reference, err := newServiceSecretReference()
	if err != nil {
		return fail(stderr, application.ExitTarget, "service secret reference could not be generated")
	}
	credentialReference := domain.ServiceSecretCredentialReference(target.Name, options.service, reference)
	if err := vault.Store(ctx, credentialReference, string(value)); err != nil {
		return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
	}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		_ = vault.Delete(ctx, credentialReference)
		return fail(stderr, application.ExitTarget, "local Core secret storage is unavailable")
	}
	if err := stageServiceSecret(secretRoot, reference, value); err != nil {
		_ = vault.Delete(ctx, credentialReference)
		return fail(stderr, application.ExitTarget, "local Core secret storage is unavailable")
	}
	created, err := client.CreateServiceSecretReference(ctx, options.service, domain.ServiceSecretReference{
		Reference: reference, ServiceID: options.service, Purpose: options.purpose, Label: options.label, State: "active",
	})
	if err != nil {
		removeStagedServiceSecret(secretRoot, reference)
		_ = vault.Delete(ctx, credentialReference)
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, map[string]any{"schemaVersion": "service-secret-create-result/v1", "target": target.Name, "item": created})
}

func (r Runner) revokeServiceSecret(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, stdout, stderr io.Writer) int {
	if !options.confirm || !validServiceSecretReference(options.reference) || options.purpose != "" || options.label != "" || options.secretStdin || options.tokenStdin || options.endpoint != "" || options.replace {
		return fail(stderr, application.ExitArguments, "service secret revoke requires --reference and --confirm")
	}
	if err := client.RevokeServiceSecretReference(ctx, options.service, options.reference); err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		return fail(stderr, application.ExitTarget, "Core revoked the secret, but local secret storage is unavailable")
	}
	if err := removeStagedServiceSecret(secretRoot, options.reference); err != nil {
		return fail(stderr, application.ExitTarget, "Core revoked the secret, but local secret cleanup failed")
	}
	vault := r.credentialVault()
	if vault == nil || vault.Delete(ctx, domain.ServiceSecretCredentialReference(target.Name, options.service, options.reference)) != nil {
		return fail(stderr, application.ExitTarget, "Core revoked the secret, but OS vault cleanup failed")
	}
	return encode(stdout, map[string]any{"schemaVersion": "service-secret-revoke-result/v1", "target": target.Name, "serviceId": options.service, "reference": options.reference, "state": "revoked"})
}

func (r Runner) readServiceSecret(options commandOptions) ([]byte, error) {
	if options.secretStdin {
		if r.TokenReader == nil {
			return nil, errors.New("secure secret input is unavailable")
		}
		body, err := io.ReadAll(io.LimitReader(r.TokenReader, serviceSecretMaximumBytes+2))
		if err != nil || len(body) > serviceSecretMaximumBytes+1 {
			clear(body)
			return nil, errors.New("secure secret input exceeds the size limit")
		}
		body = trimOneLineEnding(body)
		if len(body) == 0 || strings.ContainsRune(string(body), '\x00') {
			clear(body)
			return nil, errors.New("secure secret input is empty or invalid")
		}
		return body, nil
	}
	return r.readSecret("Service secret: ")
}

func trimOneLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}

func newServiceSecretReference() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "ssr:v1:" + hex.EncodeToString(random[:]), nil
}

func serviceSecretRoot(target domain.Target) (string, error) {
	if target.Name != "local" || target.Transport != domain.TransportLocalProcess || target.StatePath == "" {
		return "", errors.New("local Core state path is unavailable")
	}
	statePath, err := filepath.Abs(target.StatePath)
	if err != nil || statePath == filepath.VolumeName(statePath)+string(filepath.Separator) {
		return "", errors.New("secret root path is invalid")
	}
	if info, err := os.Lstat(statePath); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", errors.New("Core state file is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stateDirectory := filepath.Dir(statePath)
	resolvedStateDirectory, err := filepath.EvalSymlinks(stateDirectory)
	if err != nil {
		return "", errors.New("Core state directory is unavailable")
	}
	if info, err := os.Stat(resolvedStateDirectory); err != nil || !info.IsDir() {
		return "", errors.New("secret root is unavailable")
	}
	root := filepath.Join(resolvedStateDirectory, "secrets")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(root, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(root)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("secret root contains an unsafe path component")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func stageServiceSecret(root, reference string, value []byte) error {
	if !validServiceSecretReference(reference) || len(value) == 0 || len(value) > serviceSecretMaximumBytes {
		return errors.New("invalid secret staging request")
	}
	path := filepath.Join(root, reference)
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("secret reference already exists")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(value)
	if writeErr == nil && written != len(value) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return writeErr
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func removeStagedServiceSecret(root, reference string) error {
	if !validServiceSecretReference(reference) {
		return errors.New("invalid secret reference")
	}
	path := filepath.Join(root, reference)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("staged secret is not a regular file")
	}
	return os.Remove(path)
}

func validServiceSecretReference(value string) bool {
	const prefix = "ssr:v1:"
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
