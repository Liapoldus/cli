package domain

import (
	"context"
	"encoding/json"
)

// ProjectSource reads an immutable project revision from Git.
type ProjectSource interface {
	ResolveRevision(ctx context.Context, projectPath, revision string) (string, error)
	WorkingTreeStatus(ctx context.Context, projectPath string) (string, error)
	RemoteOrigin(ctx context.Context, projectPath string) (string, error)
	Files(ctx context.Context, projectPath, revision string) ([]File, error)
	RuntimeArtifacts(ctx context.Context, projectPath, revision string) ([]ArtifactReference, error)
	ServiceSettings(ctx context.Context, projectPath, revision string) ([]Service, error)
	Links(ctx context.Context, projectPath, revision string) ([]json.RawMessage, error)
	ProjectID(ctx context.Context, projectPath, revision string) (string, error)
}

// CredentialProvider resolves a secret reference without exposing the storage
// mechanism to command policy. Implementations must never log the returned
// value or persist it in a target profile, bundle or report.
type CredentialProvider interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

// CredentialVault is the CLI-owned local secret store. Implementations must
// use the operating-system vault and fail closed when it is unavailable.
type CredentialVault interface {
	CredentialProvider
	Store(ctx context.Context, reference, value string) error
	Delete(ctx context.Context, reference string) error
}

// CoreClient is the narrow Management API port used by the application layer.
type CoreClient interface {
	Health(ctx context.Context) (int, error)
	Plan(ctx context.Context, request ConfigBundleRequest) (PlanResponse, error)
	Apply(ctx context.Context, request ConfigBundleRequest, idempotencyKey string) (ApplyResponse, error)
	Operation(ctx context.Context, operationID string) (OperationResponse, error)
	PluginMetadata(ctx context.Context, pluginID, view string) (json.RawMessage, error)
	RegisterPlugin(ctx context.Context, metadata PluginRegistrationMetadata) (json.RawMessage, error)
	EnsurePluginLink(ctx context.Context, policy PluginLinkPolicyRequest, idempotencyKey string) (json.RawMessage, error)
}

type CoreConnection struct {
	Endpoint       string
	Token          string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	ServerName     string
}

type PlanResponse struct {
	Raw json.RawMessage
}

type ApplyResponse struct {
	OperationIDs []string
	Raw          json.RawMessage
}

type OperationResponse struct {
	State string
	Raw   json.RawMessage
}

type TargetRegistry interface {
	List() ([]Target, error)
	Find(name string) (Target, error)
	Add(target Target) error
}

type CoreStateFiles interface {
	Backup(target Target, output string) error
	Restore(target Target, input string) error
}

// ConfigBundleRequest is the wire-neutral request assembled by the application.
type ConfigBundleRequest struct {
	Project ProjectRef `json:"project"`
	Bundle  BundleRef  `json:"bundle"`
	Target  TargetRef  `json:"target"`
}

type ProjectRef struct {
	ID         string `json:"id"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision"`
}

type BundleRef struct {
	SchemaVersion string            `json:"schemaVersion"`
	Digest        string            `json:"digest"`
	Services      []Service         `json:"services,omitempty"`
	Links         []json.RawMessage `json:"links,omitempty"`
}

type TargetRef struct {
	Environment        string `json:"environment"`
	ExpectedGeneration *int64 `json:"expectedGeneration,omitempty"`
}
