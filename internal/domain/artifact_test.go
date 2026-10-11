package domain

import (
	"strings"
	"testing"
)

func TestArtifactReferenceValidationBoundsProjectRuntimeInputs(t *testing.T) {
	valid := ArtifactReference{
		ID: "server", Version: "1.1.0", Platform: "linux/arm64",
		Digest: "sha256:" + strings.Repeat("a", 64), Command: "server",
		ControlEndpoint: "https://server:9443", PeerEndpoint: "https://server:9643",
	}
	if err := valid.Validate("server"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ArtifactReference)
	}{
		{name: "service-id-mismatch", mutate: func(value *ArtifactReference) { value.ID = "echo" }},
		{name: "invalid-digest", mutate: func(value *ArtifactReference) { value.Digest = "sha256:unknown" }},
		{name: "unsafe-command", mutate: func(value *ArtifactReference) { value.Command = "server;curl" }},
		{name: "external-control-endpoint", mutate: func(value *ArtifactReference) { value.ControlEndpoint = "https://example.com:9443" }},
		{name: "wrong-control-port", mutate: func(value *ArtifactReference) { value.ControlEndpoint = "https://server:9444" }},
		{name: "external-peer-endpoint", mutate: func(value *ArtifactReference) { value.PeerEndpoint = "https://echo:9643" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate("server"); err == nil {
				t.Fatal("unsafe runtime artifact reference was accepted")
			}
		})
	}
}
