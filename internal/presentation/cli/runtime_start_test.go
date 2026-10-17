package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

type startupProjectSource struct {
	domain.ProjectSource
	artifact domain.Artifact
}

func (startupProjectSource) ResolveRevision(context.Context, string, string) (string, error) {
	return "WORKING_TREE", nil
}

func (source startupProjectSource) RuntimeArtifacts(context.Context, string, string) ([]domain.ArtifactReference, error) {
	return []domain.ArtifactReference{{ID: source.artifact.ID, Version: source.artifact.Version, Platform: source.artifact.Platform, Digest: source.artifact.Digest, Command: "server", ControlEndpoint: "https://server:9443", PeerEndpoint: "https://server:9643"}}, nil
}

func TestRuntimeStartRetainsContainersAfterRegistrationFailure(t *testing.T) {
	runner, docker, _, serviceTarget, desired, _ := replacementRuntimeFixture(t, nil)
	target, _ := dockerCoreTestTarget(t, true)
	target.Runtime = serviceTarget.Runtime
	runner.Registry = targetRegistryStub{target: target}
	runner.Credentials = credentialProviderStub{value: strings.Repeat("a", 64)}
	runner.Source = startupProjectSource{artifact: desired.Artifact}
	runner.CoreFactory = func(domain.CoreConnection) (domain.CoreClient, error) {
		return &runtimeRegistrationCoreStub{register: func(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
			return nil, domain.CoreHTTPError{Status: 400, Code: "registration_rejected"}
		}}, nil
	}
	var output strings.Builder
	code := runner.runtimeOperation(context.Background(), "start", []string{"--project", t.TempDir(), "--revision", "WORKING_TREE", "--target", "local"}, &output, &output)
	if code == application.ExitOK || !strings.Contains(output.String(), "registration_rejected") {
		t.Fatalf("expected registration failure: code=%d output=%s", code, output.String())
	}
	if len(docker.requests) != 2 || len(docker.removed) != 0 || len(docker.stopped) != 0 || len(docker.networkRemoved) != 0 {
		t.Fatalf("startup discarded diagnostic resources: requests=%d removed=%v stopped=%v networks=%v", len(docker.requests), docker.removed, docker.stopped, docker.networkRemoved)
	}
	if !strings.Contains(output.String(), "containers retained") {
		t.Fatalf("missing recovery instruction: %s", output.String())
	}
}
