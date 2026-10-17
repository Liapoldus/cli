package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	TransportLocalProcess  = "local-process"
	TransportRemoteAPI     = "remote-api"
	ApprovalRemoteRequired = "remote-required"
)

// Target describes one Core execution context. Secrets are referenced by name,
// never stored as values in this model or in the target registry.
type Target struct {
	Name             string `json:"name"`
	Environment      string `json:"environment"`
	Transport        string `json:"transport"`
	Approval         string `json:"approval,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	CredentialRef    string `json:"credentialRef,omitempty"`
	CAFile           string `json:"caFile,omitempty"`
	ClientCertFile   string `json:"clientCertFile,omitempty"`
	ClientKeyFile    string `json:"clientKeyFile,omitempty"`
	ServerName       string `json:"serverName,omitempty"`
	CoreBinary       string `json:"coreBinary,omitempty"`
	CoreBinaryDigest string `json:"coreBinaryDigest,omitempty"`
	RuntimeImage     string `json:"runtimeImage,omitempty"`
	RuntimePlatform  string `json:"runtimePlatform,omitempty"`
	StatePath        string `json:"statePath,omitempty"`
	PIDPath          string `json:"pidPath,omitempty"`
	LogPath          string `json:"logPath,omitempty"`
	// TrafficReportPath points to a project-relative report produced by the
	// CLI/Core observation pipeline. Studio imports and redacts the report after
	// the CLI announces it; it is not a live Core connection or a credential.
	TrafficReportPath string        `json:"trafficReportPath,omitempty"`
	Bootstrap         CoreBootstrap `json:"bootstrap,omitempty"`
	Runtime           RuntimeConfig `json:"runtime,omitempty"`
}

// ManagementCredentialReference derives a non-secret, stable OS-vault key for
// one target. The reference contains no token and cannot select another target.
func ManagementCredentialReference(targetName string) string {
	digest := sha256.Sum256([]byte(targetName))
	return "os-keyring:v1:core-management:" + hex.EncodeToString(digest[:])
}

// RuntimeConfig contains non-secret references and deterministic network
// policy used only when the local CLI stages already-built service binaries in
// Docker. Credentials remain file references owned by the target profile.
type RuntimeConfig struct {
	CoreURL                string `json:"coreURL,omitempty"`
	CoreServerName         string `json:"coreServerName,omitempty"`
	CoreCommonName         string `json:"coreCommonName,omitempty"`
	CoreURI                string `json:"coreURI,omitempty"`
	CoreClientCommonName   string `json:"coreClientCommonName,omitempty"`
	CoreClientURI          string `json:"coreClientURI,omitempty"`
	TLSDirectory           string `json:"tlsDirectory,omitempty"`
	ControlPortBase        int    `json:"controlPortBase,omitempty"`
	PeerPort               int    `json:"peerPort,omitempty"`
	PublicPort             int    `json:"publicPort,omitempty"`
	PublicContainerPort    int    `json:"publicContainerPort,omitempty"`
	PublicAPIHostPort      int    `json:"publicApiHostPort,omitempty"`
	PublicAPIContainerPort int    `json:"publicApiContainerPort,omitempty"`
	PublicServiceID        string `json:"publicServiceId,omitempty"`
	PeerAllowedCaller      string `json:"peerAllowedCaller,omitempty"`
}

// CoreBootstrap contains non-secret references used only to initialize a new
// local Core state. The Core process reads the resulting environment once and
// persists the settings in SQLite; the CLI never parses or writes that SQLite
// state during deployment.
type CoreBootstrap struct {
	ManagementListen      string `json:"managementListen,omitempty"`
	ManagementCertificate string `json:"managementCertificate"`
	ManagementKey         string `json:"managementKey"`
	ManagementClientCA    string `json:"managementClientCA,omitempty"`
	ControlListen         string `json:"controlListen,omitempty"`
	ControlPublicURL      string `json:"controlPublicURL,omitempty"`
	ControlCertificate    string `json:"controlCertificate"`
	ControlKey            string `json:"controlKey"`
	ReplicaClientCA       string `json:"replicaClientCA"`
	ReplicaServerCA       string `json:"replicaServerCA"`
	SecretRoot            string `json:"secretRoot"`
}
