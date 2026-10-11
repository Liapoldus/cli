package domain

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

const RuntimeEventsSchema = "runtime-events/v1"

var (
	ErrArtifactNotFound         = errors.New("artifact not found")
	ErrArtifactUntrusted        = errors.New("artifact is not trusted")
	ErrArtifactDigestMismatch   = errors.New("artifact digest mismatch")
	ErrArtifactPlatformMismatch = errors.New("artifact platform mismatch")
)

// Artifact is a reference to an operator-owned immutable binary. The path is
// deliberately kept outside Project files and is only persisted in the local
// CLI registry.
type Artifact struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	Digest          string `json:"digest"`
	Path            string `json:"path"`
	Trusted         bool   `json:"trusted"`
	Command         string `json:"command,omitempty"`
	ControlEndpoint string `json:"controlEndpoint,omitempty"`
	PeerEndpoint    string `json:"peerEndpoint,omitempty"`
}

// ArtifactReference is the immutable binary identity declared by a Project
// service. Local paths and trust decisions remain in the operator-owned CLI
// registry and are never written to Project files.
type ArtifactReference struct {
	ID              string `json:"id" yaml:"id"`
	Version         string `json:"version" yaml:"version"`
	Platform        string `json:"platform" yaml:"platform"`
	Digest          string `json:"digest" yaml:"digest"`
	Command         string `json:"command" yaml:"command"`
	ControlEndpoint string `json:"controlEndpoint" yaml:"controlEndpoint"`
	PeerEndpoint    string `json:"peerEndpoint" yaml:"peerEndpoint"`
}

func (reference ArtifactReference) Validate(serviceID string) error {
	if !validArtifactPart(serviceID) || reference.ID != serviceID || !validArtifactPart(reference.Version) || !validPlatform(reference.Platform) || !validRuntimeCommand(reference.Command) {
		return errors.New("service artifact id, version and platform must match the service")
	}
	if !isSHA256(reference.Digest) {
		return errors.New("service artifact digest must be a sha256 digest")
	}
	if err := validateArtifactEndpoint(reference.ControlEndpoint, serviceID, 9443); err != nil {
		return fmt.Errorf("invalid service control endpoint: %w", err)
	}
	if err := validateArtifactEndpoint(reference.PeerEndpoint, serviceID, 0); err != nil {
		return fmt.Errorf("invalid service peer endpoint: %w", err)
	}
	return nil
}

func validRuntimeCommand(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateArtifactEndpoint(value, serviceID string, expectedPort int) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() != serviceID || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be a private HTTPS endpoint using the service DNS name")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || expectedPort > 0 && port != expectedPort {
		return errors.New("endpoint port is invalid")
	}
	return nil
}

func (artifact Artifact) Validate() error {
	if !validArtifactPart(artifact.ID) || !validArtifactPart(artifact.Version) || !validPlatform(artifact.Platform) {
		return errors.New("artifact id, version and platform are required")
	}
	if !isSHA256(artifact.Digest) {
		return errors.New("artifact digest must be a sha256 digest")
	}
	if filepath.IsAbs(artifact.Path) == false || strings.ContainsRune(artifact.Path, '\x00') {
		return errors.New("artifact path must be absolute")
	}
	if artifact.Command != "" && (strings.ContainsAny(artifact.Command, "/\\") || strings.ContainsRune(artifact.Command, '\x00')) {
		return errors.New("artifact command must be a binary name")
	}
	return nil
}

func validArtifactPart(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return false
	}
	return true
}

func validPlatform(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validArtifactPart(parts[0]) && validArtifactPart(parts[1])
}

func (artifact Artifact) Key() string {
	return fmt.Sprintf("%s@%s/%s", artifact.ID, artifact.Version, artifact.Platform)
}

type ArtifactRegistry interface {
	List() ([]Artifact, error)
	Find(id, version, platform string) (Artifact, error)
	Register(artifact Artifact) error
	Trust(id, version, platform, digest string) error
	Remove(id, version, platform string) error
}

// PluginRegistrationMetadata is the only plugin data the CLI sends to Core.
// Live manifest, schema, identity and readiness are fetched by Core through
// the Plugin SDK REST client and are never requested by this process.
type PluginRegistrationMetadata struct {
	ArtifactID      string `json:"artifactId"`
	ArtifactVersion string `json:"artifactVersion"`
	Digest          string `json:"digest"`
	InstanceID      string `json:"instanceId"`
	ReplicaID       string `json:"replicaId"`
	ControlEndpoint string `json:"controlEndpoint"`
	PeerEndpoint    string `json:"peerEndpoint,omitempty"`
	RuntimeStatus   string `json:"runtimeStatus"`
}

type RuntimeEvent struct {
	SchemaVersion string `json:"schemaVersion"`
	Type          string `json:"type"`
	OperationID   string `json:"operationId,omitempty"`
	ServiceID     string `json:"serviceId,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Status        string `json:"status,omitempty"`
	Message       string `json:"message,omitempty"`
	Digest        string `json:"digest,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
}

type RuntimePlan struct {
	Project  string                   `json:"project"`
	Revision string                   `json:"revision"`
	Target   string                   `json:"target"`
	Services []RuntimeServiceArtifact `json:"services"`
}

// RuntimeServiceArtifact binds a Project's service launch contract to the
// operator-owned binary resolved from the exact artifact reference.
type RuntimeServiceArtifact struct {
	Artifact        Artifact `json:"artifact"`
	Command         string   `json:"command"`
	ControlEndpoint string   `json:"controlEndpoint"`
	PeerEndpoint    string   `json:"peerEndpoint"`
}

type DockerProviderStatus struct {
	ID             string `json:"id"`
	BinaryPath     string `json:"binaryPath"`
	Version        string `json:"version,omitempty"`
	ComposeVersion string `json:"composeVersion,omitempty"`
	Platform       string `json:"platform"`
	Available      bool   `json:"available"`
	ErrorCode      string `json:"errorCode,omitempty"`
}

type DockerContainerRequest struct {
	Name               string            `json:"name"`
	Image              string            `json:"image"`
	Network            string            `json:"network"`
	NetworkAliases     []string          `json:"networkAliases,omitempty"`
	Platform           string            `json:"platform,omitempty"`
	StagedBinary       string            `json:"stagedBinary"`
	StagedBinaryTarget string            `json:"stagedBinaryTarget,omitempty"`
	Entrypoint         string            `json:"entrypoint,omitempty"`
	Command            string            `json:"command"`
	Arguments          []string          `json:"arguments,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	Mounts             []DockerMount     `json:"mounts,omitempty"`
	PublishedPorts     []DockerPort      `json:"publishedPorts,omitempty"`
	ExtraHosts         []string          `json:"extraHosts,omitempty"`
	Label              string            `json:"label"`
}

type DockerMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type DockerPort struct {
	HostIP    string `json:"hostIp,omitempty"`
	Host      int    `json:"host"`
	Container int    `json:"container"`
}

type DockerProvider interface {
	Probe(context.Context) DockerProviderStatus
	NetworkCreate(context.Context, string, string) error
	NetworkRemove(context.Context, string) error
	RunContainer(context.Context, DockerContainerRequest) error
	ListContainers(context.Context, string) ([]string, error)
	StopContainer(context.Context, string) error
	RemoveContainer(context.Context, string) error
	Logs(context.Context, string) ([]byte, error)
	Inspect(context.Context, string) ([]byte, error)
	Exec(context.Context, string, string, []string) ([]byte, error)
}
