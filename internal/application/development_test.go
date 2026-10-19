package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestDevelopmentIdempotencyKeyTracksCurrentServiceRevisions(t *testing.T) {
	request := domain.ConfigBundleRequest{
		Project: domain.ProjectRef{ID: "demo", Revision: "working-tree:abc"},
		Bundle:  domain.BundleRef{Digest: "sha256:bundle", Services: []domain.Service{{ID: "server"}, {ID: "echo"}}},
		Target:  domain.TargetRef{Environment: "local"},
	}
	bundle := domain.Bundle{Digest: request.Bundle.Digest}
	firstPlan := []byte(`{"plan":{"valid":true,"digest":"sha256:bundle","services":[{"id":"server","currentRevision":7},{"id":"echo","currentRevision":2}]}}`)
	reorderedPlan := []byte(`{"plan":{"valid":true,"digest":"sha256:bundle","services":[{"id":"echo","currentRevision":2},{"id":"server","currentRevision":7}]}}`)
	updatedPlan := []byte(`{"plan":{"valid":true,"digest":"sha256:bundle","services":[{"id":"server","currentRevision":8},{"id":"echo","currentRevision":2}]}}`)

	first, err := developmentIdempotencyKey(request, bundle, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := developmentIdempotencyKey(request, bundle, reorderedPlan)
	if err != nil {
		t.Fatal(err)
	}
	if first != reordered {
		t.Fatalf("plan ordering changed idempotency key: %q != %q", first, reordered)
	}
	updated, err := developmentIdempotencyKey(request, bundle, updatedPlan)
	if err != nil {
		t.Fatal(err)
	}
	if first == updated {
		t.Fatal("new Core service revision reused the previous apply key")
	}
}

type recoveryStoreStub struct {
	intent  domain.ApplyIntent
	found   bool
	cleared bool
	saved   int
}

func (store *recoveryStoreStub) Acquire(context.Context, string) (func() error, error) {
	return func() error { return nil }, nil
}
func (store *recoveryStoreStub) Load(string) (domain.ApplyIntent, bool, error) {
	return store.intent, store.found, nil
}
func (store *recoveryStoreStub) Save(string, domain.ApplyIntent) error {
	store.saved++
	return nil
}
func (store *recoveryStoreStub) Clear(string) error {
	store.cleared = true
	return nil
}

type recoveryCoreStub struct {
	domain.CoreClient
	operations []domain.OperationReference
}

func (core recoveryCoreStub) ResolveOperations(context.Context, string, []string) ([]domain.OperationReference, error) {
	return core.operations, nil
}

func TestDevelopmentRecoveryRequiresExplicitRetryAfterFailedOperation(t *testing.T) {
	journal := &recoveryStoreStub{
		intent: domain.ApplyIntent{Scope: "config-bundle/revision", Keys: []string{"idempotency-key"}}, found: true,
	}
	service := DevelopmentDeploymentService{
		Recovery: journal, RecoveryTargetKey: "target",
		Core: recoveryCoreStub{operations: []domain.OperationReference{{ID: "op-1", State: "failed", ErrorCode: "apply_failed"}}},
	}
	_, err := service.recoverPending(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "--retry-failed") || journal.cleared {
		t.Fatalf("failed operation must pause without clearing its journal: err=%v cleared=%v", err, journal.cleared)
	}
	retrying, err := service.recoverPending(context.Background(), true)
	if err != nil || !retrying {
		t.Fatalf("explicit retry should reconcile terminal failed operation: %v", err)
	}
	if !journal.cleared {
		t.Fatal("explicit retry did not clear the terminal recovery marker")
	}
}

func TestDevelopmentRecoveryNeverRetriesDegradedRollout(t *testing.T) {
	journal := &recoveryStoreStub{
		intent: domain.ApplyIntent{Scope: "config-bundle/revision", Keys: []string{"idempotency-key"}}, found: true,
	}
	service := DevelopmentDeploymentService{
		Recovery: journal, RecoveryTargetKey: "target",
		Core: recoveryCoreStub{operations: []domain.OperationReference{{ID: "op-1", State: "degraded"}}},
	}
	if retrying, err := service.recoverPending(context.Background(), true); err == nil || retrying || journal.cleared {
		t.Fatalf("degraded rollout must remain paused: err=%v cleared=%v", err, journal.cleared)
	}
}

func TestFreshRetryIdempotencyKeyDoesNotReplayFailedOperation(t *testing.T) {
	first, err := freshRetryIdempotencyKey("same-payload-key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := freshRetryIdempotencyKey("same-payload-key")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == "same-payload-key" || second == "same-payload-key" {
		t.Fatalf("explicit retries reused idempotency key: first=%q second=%q", first, second)
	}
}

func TestDevelopmentIdempotencyKeyRejectsIncompleteCorePlan(t *testing.T) {
	request := domain.ConfigBundleRequest{Bundle: domain.BundleRef{Services: []domain.Service{{ID: "server"}}}}
	_, err := developmentIdempotencyKey(request, domain.Bundle{}, []byte(`{"plan":{"valid":true,"digest":"","services":[]}}`))
	if err == nil {
		t.Fatal("incomplete Core plan unexpectedly produced an idempotency key")
	}
}

func TestDevelopmentIdempotencyKeyRejectsCorePlanWithSubmissionBlocker(t *testing.T) {
	request := domain.ConfigBundleRequest{
		Project: domain.ProjectRef{ID: "demo", Revision: "working-tree:abc"},
		Bundle:  domain.BundleRef{Digest: "sha256:bundle", Services: []domain.Service{{ID: "server"}}},
		Target:  domain.TargetRef{Environment: "local"},
	}
	plan := []byte(`{"plan":{"valid":true,"digest":"sha256:bundle","services":[{"id":"server","currentRevision":7,"blockedBy":["traffic_rollout_incomplete"]}]}}`)
	_, err := developmentIdempotencyKey(request, domain.Bundle{Digest: "sha256:bundle"}, plan)
	if err == nil || !strings.Contains(err.Error(), "traffic_rollout_incomplete") {
		t.Fatalf("blocked Core plan was not rejected with its safe reason: %v", err)
	}
}

func TestDevelopmentApplyChecksBlockersEvenWithCallerIdempotencyKey(t *testing.T) {
	source := developmentProjectSourceStub{}
	files := []domain.File{{
		Path: "services/server/settings.json", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 1,
	}}
	digest := domain.BundleDigest(files)
	core := &blockedDevelopmentCoreStub{planRaw: []byte(fmt.Sprintf(
		`{"plan":{"valid":true,"digest":%q,"services":[{"id":"server","currentRevision":95,"blockedBy":["traffic_rollout_incomplete"]}]}}`, digest,
	))}
	recovery := &recoveryStoreStub{}
	secrets := &developmentSecretsStub{}
	service := DevelopmentDeploymentService{
		Source:            source,
		Core:              core,
		Snapshots:         developmentSnapshotterStub{},
		Secrets:           secrets,
		Recovery:          recovery,
		RecoveryTargetKey: "local",
	}
	_, _, err := service.Apply(
		context.Background(), "project", "local", nil, true, false, false, "caller-provided-key",
	)
	if err == nil || !strings.Contains(err.Error(), "traffic_rollout_incomplete") {
		t.Fatalf("blocked Core plan was not surfaced: %v", err)
	}
	if core.planCalls != 1 || core.applyCalls != 0 {
		t.Fatalf("blocked plan must stop before Core apply: plans=%d applies=%d", core.planCalls, core.applyCalls)
	}
	if secrets.prepared || recovery.saved != 0 {
		t.Fatalf("blocked plan must not stage secrets or write recovery intent: prepared=%v saved=%d", secrets.prepared, recovery.saved)
	}
}

type developmentProjectSourceStub struct{}

func (developmentProjectSourceStub) ResolveRevision(context.Context, string, string) (string, error) {
	return "snapshot-revision", nil
}
func (developmentProjectSourceStub) WorkingTreeStatus(context.Context, string) (string, error) {
	return "clean", nil
}
func (developmentProjectSourceStub) RemoteOrigin(context.Context, string) (string, error) {
	return "https://example.invalid/server-echo-studio.git", nil
}
func (developmentProjectSourceStub) Files(context.Context, string, string) ([]domain.File, error) {
	return []domain.File{{Path: "services/server/settings.json", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 1}}, nil
}
func (developmentProjectSourceStub) RuntimeArtifacts(context.Context, string, string) ([]domain.ArtifactReference, error) {
	return nil, nil
}
func (developmentProjectSourceStub) ServiceSettings(context.Context, string, string) ([]domain.Service, error) {
	return []domain.Service{{ID: "server", Settings: json.RawMessage(`{"routes":[]}`)}}, nil
}
func (developmentProjectSourceStub) Links(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (developmentProjectSourceStub) ProjectID(context.Context, string, string) (string, error) {
	return "server-echo-studio", nil
}

type developmentSnapshotStub struct{ path string }

func (snapshot developmentSnapshotStub) RootPath() string { return snapshot.path }
func (developmentSnapshotStub) Close() error              { return nil }

type developmentSnapshotterStub struct{}

func (developmentSnapshotterStub) Capture(_ context.Context, projectPath string) (domain.ProjectSnapshot, error) {
	return developmentSnapshotStub{path: projectPath}, nil
}

type blockedDevelopmentCoreStub struct {
	domain.CoreClient
	planRaw    []byte
	planCalls  int
	applyCalls int
}

func (core *blockedDevelopmentCoreStub) Plan(context.Context, domain.ConfigBundleRequest) (domain.PlanResponse, error) {
	core.planCalls++
	return domain.PlanResponse{Raw: core.planRaw}, nil
}
func (core *blockedDevelopmentCoreStub) Apply(context.Context, domain.ConfigBundleRequest, string) (domain.ApplyResponse, error) {
	core.applyCalls++
	return domain.ApplyResponse{}, nil
}

type developmentSecretsStub struct{ prepared bool }

func (secrets *developmentSecretsStub) Prepare(context.Context, []domain.Service) error {
	secrets.prepared = true
	return nil
}
func (*developmentSecretsStub) Finalize(context.Context, []domain.Service) error { return nil }
