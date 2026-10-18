package domain

import (
	"context"
	"encoding/json"
	"io"
)

// ProjectSource reads immutable Git revisions and explicitly selected private
// snapshots used by local development operations.
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

// ProjectSnapshot is an immutable, private copy of one development working
// tree. Closing it removes only the temporary snapshot owned by its creator.
type ProjectSnapshot interface {
	RootPath() string
	Close() error
}

// ProjectSnapshotter captures the safe, bundle-relevant files of a project
// without changing the user's checkout.
type ProjectSnapshotter interface {
	Capture(ctx context.Context, projectPath string) (ProjectSnapshot, error)
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
	ResolveOperations(ctx context.Context, scope string, keys []string) ([]OperationReference, error)
	ServiceMetadata(ctx context.Context, serviceID, view string) (json.RawMessage, error)
	ServiceAdminQuery(ctx context.Context, serviceID, pageID, resourceID string) (json.RawMessage, error)
	RegisterService(ctx context.Context, metadata ServiceRegistrationMetadata) (json.RawMessage, error)
	EnsureServiceLink(ctx context.Context, policy ServiceLinkPolicyRequest, idempotencyKey string) (json.RawMessage, error)
}

// CoreSettingsReader is the narrow Management API capability used by the
// operator settings workflow; service deployment use cases do not depend on it.
type CoreSettingsReader interface {
	ReadCoreSettings(ctx context.Context) (CoreSettingsSnapshot, error)
}

// CoreSettingsWriter updates the desired Core-owned settings document with
// Management API revision CAS. Callers must resolve only typed local refs
// before invoking it; raw paths must never cross the Studio boundary.
type CoreSettingsWriter interface {
	UpdateCoreSettings(ctx context.Context, ifMatch string, document json.RawMessage) (CoreSettingsSnapshot, error)
}

// OperationReference is the safe subset returned by Core's idempotency
// recovery lookup. It deliberately excludes request payloads and credentials.
type OperationReference struct {
	ID         string
	State      string
	ResourceID string
	ErrorCode  string
}

// ServiceArtifactPublisher sends an explicitly selected service-owned artifact
// action through Core Management API. The caller supplies a bounded Project
// artifact stream and safe action metadata; implementations must not resolve or
// call a service endpoint directly.
type ServiceArtifactPublisher interface {
	PublishServiceArtifact(ctx context.Context, request ServiceArtifactPublication) (json.RawMessage, error)
}

// ServiceAdminSurfaceReader exposes the bounded, Core-backed descriptor used
// to render service-owned administration forms. Implementations must not call
// a service endpoint directly; Core owns SDK REST access and descriptor state.
type ServiceAdminSurfaceReader interface {
	ReadServiceAdminSurface(ctx context.Context, serviceID string) (ServiceAdminSurface, error)
}

type ServiceAdminSurface struct {
	Digest     string
	Descriptor json.RawMessage
}

// ServiceAdminQueryPager reads one descriptor-declared Admin Surface table
// through Core. The caller must pass the digest it inspected so a concurrent
// descriptor change is rejected before the query is forwarded.
type ServiceAdminQueryPager interface {
	QueryServiceAdminSection(ctx context.Context, request ServiceAdminSectionQuery) (json.RawMessage, error)
}

type ServiceAdminSectionQuery struct {
	ServiceID     string
	PageID        string
	SectionID     string
	ResourceID    string
	Cursor        string
	SurfaceDigest string
}

// ServiceAdminActionInvoker executes a live descriptor-declared action through
// Core; it never resolves or contacts a service endpoint directly.
type ServiceAdminActionInvoker interface {
	InvokeServiceAdminAction(ctx context.Context, request ServiceAdminActionInvocation) (json.RawMessage, error)
}

type ServiceAdminActionInvocation struct {
	ServiceID      string
	PageID         string
	SectionID      string
	ActionID       string
	Input          json.RawMessage
	IfMatch        string
	IdempotencyKey string
	SurfaceDigest  string
}

type ServiceArtifactPublication struct {
	ServiceID      string
	PageID         string
	ActionID       string
	Metadata       json.RawMessage
	ContentType    string
	Artifact       io.Reader
	IdempotencyKey string
	IfMatch        string
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
	ID              string
	State           string
	ReplicaOutcomes []OperationReplicaOutcome
}

type OperationReplicaOutcome struct {
	ReplicaID         string `json:"replicaId"`
	DesiredGeneration int64  `json:"desiredGeneration"`
	State             string `json:"state"`
	FailureCode       string `json:"failureCode,omitempty"`
}

type ApplyIntent struct {
	Scope string   `json:"scope"`
	Keys  []string `json:"keys"`
}

// ApplyRecoveryStore serializes local callers and persists only opaque Core
// idempotency references, never the settings bundle or operation payload.
type ApplyRecoveryStore interface {
	Acquire(ctx context.Context, targetKey string) (func() error, error)
	Load(targetKey string) (ApplyIntent, bool, error)
	Save(targetKey string, intent ApplyIntent) error
	Clear(targetKey string) error
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
