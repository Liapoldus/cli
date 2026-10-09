package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

type sourceStub struct{}

func (sourceStub) ResolveRevision(context.Context, string, string) (string, error) {
	return "abc123", nil
}
func (sourceStub) WorkingTreeStatus(context.Context, string) (string, error) { return "", nil }
func (sourceStub) RemoteOrigin(context.Context, string) (string, error)      { return "origin", nil }
func (sourceStub) Files(context.Context, string, string) ([]domain.File, error) {
	return []domain.File{{Path: "project.yaml", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 1}}, nil
}
func (sourceStub) ServiceSettings(context.Context, string, string) ([]domain.Service, error) {
	return nil, nil
}
func (sourceStub) Links(context.Context, string, string) ([]json.RawMessage, error) { return nil, nil }
func (sourceStub) ProjectID(context.Context, string, string) (string, error)        { return "demo", nil }

func TestBundleBuildUsesResolvedRevision(t *testing.T) {
	bundle, err := (BundleService{Source: sourceStub{}}).Build(context.Background(), "/project", "HEAD", "production")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Revision != "abc123" || bundle.Target != "production" || bundle.Digest == "" {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
}
