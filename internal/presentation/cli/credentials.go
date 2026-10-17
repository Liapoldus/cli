package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"golang.org/x/term"
)

func (r Runner) credential(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, application.ExitArguments, "credential status|set|remove is required")
	}
	options, err := parseOptions(args[1:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target == "" {
		return fail(stderr, application.ExitArguments, "--target is required")
	}
	target, err := r.registry(options.targets).Find(options.target)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if target.CredentialRef == "" {
		target.CredentialRef = domain.ManagementCredentialReference(target.Name)
	}

	switch args[0] {
	case "status":
		provider := r.credentialProvider()
		if provider == nil {
			return encode(stdout, map[string]any{"target": target.Name, "state": "store_unavailable"})
		}
		_, resolveErr := provider.Resolve(ctx, target.CredentialRef)
		state := "configured"
		switch {
		case resolveErr == nil:
		case errors.Is(resolveErr, domain.ErrCredentialNotFound):
			state = "missing"
		case errors.Is(resolveErr, domain.ErrCredentialStore):
			state = "store_unavailable"
		default:
			return fail(stderr, application.ExitTarget, "credential status could not be read")
		}
		return encode(stdout, map[string]any{"target": target.Name, "state": state})
	case "set":
		vault := r.credentialVault()
		if vault == nil {
			return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
		}
		if provider := r.credentialProvider(); provider != nil {
			_, resolveErr := provider.Resolve(ctx, target.CredentialRef)
			if resolveErr == nil && !options.replace {
				return fail(stderr, application.ExitArguments, "credential already exists; pass --replace to update it")
			}
			if resolveErr != nil && !errors.Is(resolveErr, domain.ErrCredentialNotFound) {
				return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
			}
		}
		first, err := r.readSecret("Core management token: ")
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		defer clear(first)
		second, err := r.readSecret("Repeat token: ")
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		defer clear(second)
		if len(first) < 32 || len(first) > 2048 {
			return fail(stderr, application.ExitArguments, "token length must be between 32 and 2048 bytes")
		}
		if subtle.ConstantTimeCompare(first, second) != 1 {
			return fail(stderr, application.ExitArguments, "tokens do not match")
		}
		if err := vault.Store(ctx, target.CredentialRef, string(first)); err != nil {
			return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "state": "configured"})
	case "remove":
		if !options.confirm {
			return fail(stderr, application.ExitArguments, "credential remove requires --confirm")
		}
		vault := r.credentialVault()
		if vault == nil {
			return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
		}
		if err := vault.Delete(ctx, target.CredentialRef); err != nil {
			return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
		}
		return encode(stdout, map[string]any{"target": target.Name, "state": "missing"})
	default:
		return fail(stderr, application.ExitArguments, "unsupported credential subcommand")
	}
}

func (r Runner) credentialProvider() domain.CredentialProvider {
	if r.Credentials != nil {
		return r.Credentials
	}
	if vault, ok := r.CredentialVault.(domain.CredentialProvider); ok {
		return vault
	}
	return nil
}

func (r Runner) credentialVault() domain.CredentialVault {
	if r.CredentialVault != nil {
		return r.CredentialVault
	}
	if vault, ok := r.Credentials.(domain.CredentialVault); ok {
		return vault
	}
	return nil
}

func (r Runner) readSecret(prompt string) ([]byte, error) {
	if r.PromptSecret != nil {
		return r.PromptSecret(prompt)
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("credential setup requires an interactive terminal")
	}
	_, _ = fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, errors.New("could not read credential from terminal")
	}
	return secret, nil
}

func generateManagementToken() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		clear(secret)
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	clear(secret)
	return token, nil
}
