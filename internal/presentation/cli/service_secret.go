package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

const serviceSecretMaximumBytes = 16 << 10

const serviceSecretIndexVersion = "local-service-secret-references/v1"

type stagedServiceSecretReference struct {
	Reference    string `json:"reference"`
	ServiceID    string `json:"serviceId"`
	Purpose      string `json:"purpose"`
	Label        string `json:"label"`
	RotationFrom string `json:"rotationFrom,omitempty"`
}

type pendingServiceSecretRotation struct {
	PreviousReference string `json:"previousReference"`
	CreatedReference  string `json:"createdReference"`
	ServiceID         string `json:"serviceId"`
	Purpose           string `json:"purpose"`
	State             string `json:"state"`
}

type pendingServiceSecretRegistration struct {
	Reference string `json:"reference"`
	ServiceID string `json:"serviceId"`
	Purpose   string `json:"purpose"`
	Label     string `json:"label"`
	State     string `json:"state"`
}

type stagedServiceSecretIndex struct {
	SchemaVersion string                         `json:"schemaVersion"`
	Target        string                         `json:"target"`
	Items         []stagedServiceSecretReference `json:"items"`
}

func (r Runner) serviceSecret(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "create" && args[0] != "rotate" && args[0] != "register" && args[0] != "revoke") {
		return fail(stderr, application.ExitArguments, "usage: liapoldus service secret list|create|rotate|register|revoke --target local --service <id>")
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
		index, err := readStagedServiceSecretIndex(target)
		if err != nil {
			return fail(stderr, application.ExitTarget, "local secret rotation metadata is unavailable")
		}
		rotations := make([]pendingServiceSecretRotation, 0)
		registrations := make([]pendingServiceSecretRegistration, 0)
		itemsByReference := make(map[string]domain.ServiceSecretReference, len(items))
		for _, item := range items {
			itemsByReference[item.Reference] = item
		}
		for _, staged := range index.Items {
			if staged.ServiceID != options.service || (options.purpose != "" && staged.Purpose != options.purpose) {
				continue
			}
			registered, found := itemsByReference[staged.Reference]
			metadataMatches := found && registered.ServiceID == staged.ServiceID && registered.Purpose == staged.Purpose && registered.Label == staged.Label && registered.State == "active"
			if !metadataMatches {
				state := "pending"
				if found {
					state = "conflict"
				}
				registrations = append(registrations, pendingServiceSecretRegistration{
					Reference: staged.Reference, ServiceID: staged.ServiceID, Purpose: staged.Purpose, Label: staged.Label, State: state,
				})
				continue
			}
			if staged.RotationFrom != "" {
				previous, previousFound := itemsByReference[staged.RotationFrom]
				if previousFound && previous.ServiceID == staged.ServiceID && previous.Purpose == staged.Purpose && previous.State == "active" {
					rotations = append(rotations, pendingServiceSecretRotation{
						PreviousReference: staged.RotationFrom, CreatedReference: staged.Reference,
						ServiceID: staged.ServiceID, Purpose: staged.Purpose, State: "staged",
					})
				}
			}
		}
		sort.Slice(rotations, func(left, right int) bool {
			return rotations[left].CreatedReference < rotations[right].CreatedReference
		})
		return encode(stdout, map[string]any{"schemaVersion": "service-secret-references/v1", "target": target.Name, "serviceId": options.service, "items": items, "pendingRotations": rotations, "pendingRegistrations": registrations})
	case "create":
		return r.createServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	case "rotate":
		return r.rotateServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	case "register":
		return r.registerStagedServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	case "revoke":
		return r.revokeServiceSecret(ctx, target, secretClient, options, stdout, stderr)
	default:
		return fail(stderr, application.ExitArguments, "unsupported service secret operation")
	}
}

func (r Runner) registerStagedServiceSecret(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, stdout, stderr io.Writer) int {
	if !options.confirm || !validServiceSecretReference(options.reference) || options.purpose != "" || options.label != "" || options.secretStdin || options.tokenStdin || options.endpoint != "" || options.replace {
		return fail(stderr, application.ExitArguments, "service secret register requires --reference and --confirm")
	}
	index, err := readStagedServiceSecretIndex(target)
	if err != nil {
		return fail(stderr, application.ExitTarget, "local secret registration metadata is unavailable")
	}
	var staged *stagedServiceSecretReference
	for itemIndex := range index.Items {
		if index.Items[itemIndex].Reference == options.reference && index.Items[itemIndex].ServiceID == options.service {
			staged = &index.Items[itemIndex]
			break
		}
	}
	if staged == nil {
		return fail(stderr, application.ExitArguments, "staged service secret is unavailable for this service")
	}
	items, err := client.ListServiceSecretReferences(ctx, options.service, staged.Purpose)
	if err != nil {
		return fail(stderr, application.ExitTarget, "Core secret catalog is unavailable; the staged secret remains available")
	}
	for _, item := range items {
		if item.Reference != staged.Reference {
			continue
		}
		if item.ServiceID == staged.ServiceID && item.Purpose == staged.Purpose && item.Label == staged.Label && item.State == "active" {
			return encode(stdout, map[string]any{"schemaVersion": "service-secret-register-result/v1", "target": target.Name, "item": item})
		}
		return fail(stderr, application.ExitTarget, "Core secret metadata conflicts with the staged local reference; staged value was preserved")
	}
	vault := r.credentialVault()
	if vault == nil {
		return fail(stderr, application.ExitTarget, domain.ErrCredentialStore.Error())
	}
	credentialReference := domain.ServiceSecretCredentialReference(target.Name, options.service, staged.Reference)
	value, err := vault.Resolve(ctx, credentialReference)
	if err != nil || len(value) == 0 || len(value) > serviceSecretMaximumBytes || strings.ContainsRune(value, '\x00') {
		return fail(stderr, application.ExitTarget, "staged local secret is unavailable; its reference metadata was preserved")
	}
	defer clear([]byte(value))
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		return fail(stderr, application.ExitTarget, "local secret runtime mount is unavailable; staged value was preserved")
	}
	if err := stageServiceSecret(secretRoot, staged.Reference, []byte(value)); err != nil {
		return fail(stderr, application.ExitTarget, "local secret runtime mount is unavailable; staged value was preserved")
	}
	created, err := client.CreateServiceSecretReference(ctx, options.service, domain.ServiceSecretReference{
		Reference: staged.Reference, ServiceID: staged.ServiceID, Purpose: staged.Purpose, Label: staged.Label, State: "active",
	})
	if err != nil {
		return fail(stderr, application.ExitTarget, "Core registration failed; staged secret remains available for retry")
	}
	if created.Reference != staged.Reference || created.ServiceID != staged.ServiceID || created.Purpose != staged.Purpose || created.Label != staged.Label || created.State != "active" {
		return fail(stderr, application.ExitTarget, "Core returned mismatched metadata; staged secret remains available for review")
	}
	return encode(stdout, map[string]any{"schemaVersion": "service-secret-register-result/v1", "target": target.Name, "item": created})
}

func (r Runner) createServiceSecret(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, stdout, stderr io.Writer) int {
	if !options.confirm || options.purpose == "" || strings.TrimSpace(options.label) == "" || options.reference != "" || options.tokenStdin || options.endpoint != "" || options.replace {
		return fail(stderr, application.ExitArguments, "service secret create requires --purpose, --label and --confirm")
	}
	if studioEventsEnabled(ctx) && !options.secretStdin {
		return fail(stderr, application.ExitArguments, "Studio secret creation requires secure stdin input")
	}
	value, err := r.readServiceSecret(options)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	defer clear(value)
	created, err := r.createServiceSecretValue(ctx, target, client, options, value, "")
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, map[string]any{"schemaVersion": "service-secret-create-result/v1", "target": target.Name, "item": created})
}

func (r Runner) rotateServiceSecret(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, stdout, stderr io.Writer) int {
	if !options.confirm || !validServiceSecretReference(options.reference) || options.purpose == "" || strings.TrimSpace(options.label) == "" ||
		options.tokenStdin || options.endpoint != "" || options.replace {
		return fail(stderr, application.ExitArguments, "service secret rotate requires --reference, --purpose, --label and --confirm")
	}
	if studioEventsEnabled(ctx) && !options.secretStdin {
		return fail(stderr, application.ExitArguments, "Studio secret rotation requires secure stdin input")
	}
	items, err := client.ListServiceSecretReferences(ctx, options.service, options.purpose)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	var old *domain.ServiceSecretReference
	for index := range items {
		item := &items[index]
		if item.Reference == options.reference {
			old = item
			break
		}
	}
	if old == nil || !old.Validate() || old.ServiceID != options.service || old.Purpose != options.purpose || old.State != "active" {
		return fail(stderr, application.ExitArguments, "service secret rotation source is unavailable for this service and purpose")
	}
	value, err := r.readServiceSecret(options)
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	defer clear(value)
	created, err := r.createServiceSecretValue(ctx, target, client, options, value, old.Reference)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	return encode(stdout, map[string]any{
		"schemaVersion": "service-secret-rotation-result/v1", "target": target.Name,
		"serviceId": options.service, "previous": *old, "created": created, "state": "staged",
	})
}

func (r Runner) createServiceSecretValue(ctx context.Context, target domain.Target, client domain.ServiceSecretReferenceClient, options commandOptions, value []byte, rotationFrom string) (domain.ServiceSecretReference, error) {
	if len(value) == 0 || len(value) > serviceSecretMaximumBytes {
		return domain.ServiceSecretReference{}, errors.New("secret input is invalid")
	}
	vault := r.credentialVault()
	if vault == nil {
		return domain.ServiceSecretReference{}, domain.ErrCredentialStore
	}
	reference, err := newServiceSecretReference()
	if err != nil {
		return domain.ServiceSecretReference{}, errors.New("service secret reference could not be generated")
	}
	credentialReference := domain.ServiceSecretCredentialReference(target.Name, options.service, reference)
	if err := vault.Store(ctx, credentialReference, string(value)); err != nil {
		return domain.ServiceSecretReference{}, domain.ErrCredentialStore
	}
	secretRoot, err := serviceSecretRoot(target)
	if err != nil {
		_ = vault.Delete(ctx, credentialReference)
		return domain.ServiceSecretReference{}, errors.New("local secret runtime mount is unavailable")
	}
	if err := addStagedServiceSecretReference(target, stagedServiceSecretReference{
		Reference: reference, ServiceID: options.service, Purpose: options.purpose, Label: options.label, RotationFrom: rotationFrom,
	}); err != nil {
		_ = vault.Delete(ctx, credentialReference)
		return domain.ServiceSecretReference{}, errors.New("local secret reference index is unavailable")
	}
	if err := stageServiceSecret(secretRoot, reference, value); err != nil {
		return domain.ServiceSecretReference{}, errors.New("local secret runtime mount is unavailable; reference and value remain staged for retry")
	}
	created, err := client.CreateServiceSecretReference(ctx, options.service, domain.ServiceSecretReference{
		Reference: reference, ServiceID: options.service, Purpose: options.purpose, Label: options.label, State: "active",
	})
	if err != nil {
		return domain.ServiceSecretReference{}, errors.New("Core metadata registration failed; reference and value remain staged for retry")
	}
	if created.Reference != reference || created.ServiceID != options.service || created.Purpose != options.purpose || created.State != "active" {
		return domain.ServiceSecretReference{}, errors.New("Core returned mismatched metadata; reference and value remain staged for review")
	}
	return created, nil
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
	if err := removeStagedServiceSecretReference(target, options.reference); err != nil {
		return fail(stderr, application.ExitTarget, "Core revoked the secret, but local secret metadata cleanup failed")
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
		return "", errors.New("local Core target is unavailable")
	}
	statePath, err := filepath.Abs(target.StatePath)
	if err != nil || statePath == filepath.VolumeName(statePath)+string(filepath.Separator) {
		return "", errors.New("Core state path is invalid")
	}
	if info, err := os.Lstat(statePath); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", errors.New("Core state file is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stateDirectory := filepath.Dir(statePath)
	root, err := runtimeSecretMountDirectory(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(stateDirectory, root)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", errors.New("secret runtime mount must be outside Core state")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(root)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("secret runtime mount contains an unsafe path component")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	return filepath.Clean(root), nil
}

func runtimeSecretMountDirectory(target domain.Target) (string, error) {
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(staging, "secrets"), nil
}

func stagedServiceSecretIndexPath(target domain.Target) (string, error) {
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		return "", err
	}
	return filepath.Join(staging, "service-secret-references.json"), nil
}

func readStagedServiceSecretIndex(target domain.Target) (stagedServiceSecretIndex, error) {
	indexPath, err := stagedServiceSecretIndexPath(target)
	if err != nil {
		return stagedServiceSecretIndex{}, err
	}
	encoded, err := os.ReadFile(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return stagedServiceSecretIndex{SchemaVersion: serviceSecretIndexVersion, Target: target.Name}, nil
	}
	info, infoErr := os.Lstat(indexPath)
	if err != nil || infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || len(encoded) > 1<<20 {
		return stagedServiceSecretIndex{}, errors.New("local secret reference index is unavailable")
	}
	var index stagedServiceSecretIndex
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&index) != nil || index.SchemaVersion != serviceSecretIndexVersion || index.Target != target.Name || len(index.Items) > 4096 {
		return stagedServiceSecretIndex{}, errors.New("local secret reference index is invalid")
	}
	seen := make(map[string]struct{}, len(index.Items))
	byReference := make(map[string]stagedServiceSecretReference, len(index.Items))
	for _, item := range index.Items {
		if !validStagedServiceSecretReference(item) {
			return stagedServiceSecretIndex{}, errors.New("local secret reference index is invalid")
		}
		if _, exists := seen[item.Reference]; exists {
			return stagedServiceSecretIndex{}, errors.New("local secret reference index is invalid")
		}
		seen[item.Reference] = struct{}{}
		byReference[item.Reference] = item
	}
	for _, item := range index.Items {
		if item.RotationFrom == "" {
			continue
		}
		previous, exists := byReference[item.RotationFrom]
		// The source reference may have been created on another machine or
		// before the local reference index existed. When present locally, still
		// enforce that the relationship cannot cross service or purpose.
		if exists && (previous.ServiceID != item.ServiceID || previous.Purpose != item.Purpose) {
			return stagedServiceSecretIndex{}, errors.New("local secret rotation index is invalid")
		}
	}
	return index, nil
}

func validStagedServiceSecretReference(item stagedServiceSecretReference) bool {
	if !validServiceSecretReference(item.Reference) || !validRuntimeServiceID(item.ServiceID) || item.Purpose == "" ||
		strings.TrimSpace(item.Purpose) != item.Purpose || len(item.Purpose) > 192 || (item.RotationFrom != "" && (!validServiceSecretReference(item.RotationFrom) || item.RotationFrom == item.Reference)) ||
		strings.TrimSpace(item.Label) == "" || strings.TrimSpace(item.Label) != item.Label || len([]rune(item.Label)) > 128 || strings.ContainsAny(item.Label, "\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(item.Purpose, ".") {
		if part == "" || part[0] < 'a' || part[0] > 'z' {
			return false
		}
		for _, character := range part {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", character) {
				return false
			}
		}
	}
	return true
}

func writeStagedServiceSecretIndex(target domain.Target, index stagedServiceSecretIndex) error {
	indexPath, err := stagedServiceSecretIndexPath(target)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(index)
	if err != nil || len(encoded) > 1<<20 {
		return errors.New("local secret reference index is unavailable")
	}
	staging, err := runtimeStagingDirectory(target)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(staging, ".secret-index-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), indexPath)
}

func addStagedServiceSecretReference(target domain.Target, item stagedServiceSecretReference) error {
	if !validStagedServiceSecretReference(item) {
		return errors.New("invalid local secret reference metadata")
	}
	index, err := readStagedServiceSecretIndex(target)
	if err != nil {
		return err
	}
	for _, current := range index.Items {
		if current.Reference == item.Reference {
			return errors.New("local secret reference already exists")
		}
	}
	index.Items = append(index.Items, item)
	sort.Slice(index.Items, func(i, j int) bool { return index.Items[i].Reference < index.Items[j].Reference })
	return writeStagedServiceSecretIndex(target, index)
}

func removeStagedServiceSecretReference(target domain.Target, reference string) error {
	if !validServiceSecretReference(reference) {
		return errors.New("invalid local secret reference")
	}
	index, err := readStagedServiceSecretIndex(target)
	if err != nil {
		return err
	}
	filtered := index.Items[:0]
	for _, item := range index.Items {
		if item.Reference == reference {
			continue
		}
		if item.RotationFrom == reference {
			item.RotationFrom = ""
		}
		filtered = append(filtered, item)
	}
	index.Items = filtered
	return writeStagedServiceSecretIndex(target, index)
}

func (r Runner) materializeServiceSecretMount(ctx context.Context, target domain.Target, reset bool) error {
	root, err := serviceSecretRoot(target)
	if err != nil {
		return errors.New("local secret runtime mount is unavailable")
	}
	if reset {
		if err := clearServiceSecretMount(root); err != nil {
			return errors.New("local secret runtime mount could not be refreshed")
		}
	}
	index, err := readStagedServiceSecretIndex(target)
	if err != nil {
		return err
	}
	if len(index.Items) == 0 {
		return nil
	}
	vault := r.credentialVault()
	if vault == nil {
		return domain.ErrCredentialStore
	}
	for _, item := range index.Items {
		value, err := vault.Resolve(ctx, domain.ServiceSecretCredentialReference(target.Name, item.ServiceID, item.Reference))
		if err != nil || value == "" || len(value) > serviceSecretMaximumBytes {
			return errors.New("a local service secret is unavailable in the operating-system vault")
		}
		secret := []byte(value)
		path := filepath.Join(root, item.Reference)
		if fileInfo, statErr := os.Lstat(path); statErr == nil && (fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular()) {
			clear(secret)
			return errors.New("local service secret runtime mount contains an unsafe file")
		}
		if existing, readErr := os.ReadFile(path); readErr == nil {
			if subtle.ConstantTimeCompare(existing, secret) != 1 {
				clear(existing)
				clear(secret)
				return errors.New("local service secret runtime mount does not match the operating-system vault")
			}
			clear(existing)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			clear(secret)
			return errors.New("local service secret runtime mount is unavailable")
		} else if err := stageServiceSecret(root, item.Reference, secret); err != nil {
			clear(secret)
			return errors.New("local service secret runtime mount could not be refreshed")
		}
		clear(secret)
	}
	return nil
}

func clearServiceSecretMount(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("invalid local secret runtime mount")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(root, 0o700)
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if !validServiceSecretReference(entry.Name()) || infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("local secret runtime mount contains an unsafe entry")
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
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
