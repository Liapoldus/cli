package domain

import "encoding/json"

// WorkingTreeRevision is the explicit source selector accepted only when a
// caller has captured an immutable local development snapshot.
const WorkingTreeRevision = "WORKING_TREE"

// File describes one project file included in a configuration bundle.
type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}

// Service contains the declarative settings of one Core service.
type Service struct {
	ID       string          `json:"id"`
	Settings json.RawMessage `json:"settings"`
}

// Bundle is the immutable configuration input sent to Core.
type Bundle struct {
	SchemaVersion string            `json:"schemaVersion"`
	ProjectPath   string            `json:"-"`
	Revision      string            `json:"revision"`
	Target        string            `json:"target"`
	Digest        string            `json:"digest"`
	Files         []File            `json:"files"`
	Services      []Service         `json:"services,omitempty"`
	Links         []json.RawMessage `json:"links,omitempty"`
}
