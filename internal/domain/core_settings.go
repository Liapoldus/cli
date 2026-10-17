package domain

import "encoding/json"

// CoreSettingsSnapshot is the trusted CLI-side Core Management response. Its
// Settings bytes may contain runtime filesystem paths and must never be written
// to Studio event output without an explicit safe projection.
type CoreSettingsSnapshot struct {
	DesiredRevision   int64
	EffectiveRevision int64
	Pending           bool
	RestartRequired   bool
	ETag              string
	Settings          json.RawMessage
}

// CoreSettingsPatch is a deliberately narrow, typed operator edit. It cannot
// carry paths, credentials, certificates, or arbitrary JSON into Core.
type CoreSettingsPatch struct {
	SchemaVersion    string                      `json:"schemaVersion"`
	ExpectedRevision int64                       `json:"expectedRevision"`
	Management       CoreManagementSettingsPatch `json:"management"`
	ServiceControl   CoreServiceControlPatch     `json:"serviceControl"`
}

type CoreManagementSettingsPatch struct {
	Listen         *string `json:"listen,omitempty"`
	MaxBodyBytes   *int64  `json:"maxBodyBytes,omitempty"`
	HeaderTimeout  *string `json:"headerTimeout,omitempty"`
	RequestTimeout *string `json:"requestTimeout,omitempty"`
}

type CoreServiceControlPatch struct {
	Listen    *string `json:"listen,omitempty"`
	PublicURL *string `json:"publicURL,omitempty"`
}
