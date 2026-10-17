package application

import (
	"context"
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
}

func (store *recoveryStoreStub) Acquire(context.Context, string) (func() error, error) {
	return func() error { return nil }, nil
}
func (store *recoveryStoreStub) Load(string) (domain.ApplyIntent, bool, error) {
	return store.intent, store.found, nil
}
func (store *recoveryStoreStub) Save(string, domain.ApplyIntent) error { return nil }
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
	retrying, err := service.recoverPending(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "--retry-failed") || journal.cleared {
		t.Fatalf("failed operation must pause without clearing its journal: err=%v cleared=%v", err, journal.cleared)
	}
	retrying, err = service.recoverPending(context.Background(), true)
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
