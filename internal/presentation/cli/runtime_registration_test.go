package cli

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

type runtimeRegistrationCoreStub struct {
	register      func(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error)
	metadataCalls int
}

func (stub *runtimeRegistrationCoreStub) Health(context.Context) (int, error) { return 200, nil }
func (stub *runtimeRegistrationCoreStub) Plan(context.Context, domain.ConfigBundleRequest) (domain.PlanResponse, error) {
	return domain.PlanResponse{}, nil
}
func (stub *runtimeRegistrationCoreStub) Apply(context.Context, domain.ConfigBundleRequest, string) (domain.ApplyResponse, error) {
	return domain.ApplyResponse{}, nil
}
func (stub *runtimeRegistrationCoreStub) Operation(context.Context, string) (domain.OperationResponse, error) {
	return domain.OperationResponse{}, nil
}
func (stub *runtimeRegistrationCoreStub) ResolveOperations(context.Context, string, []string) ([]domain.OperationReference, error) {
	return nil, nil
}
func (stub *runtimeRegistrationCoreStub) ServiceMetadata(context.Context, string, string) (json.RawMessage, error) {
	stub.metadataCalls++
	return nil, errors.New("runtime registration must not pre-read service metadata")
}
func (stub *runtimeRegistrationCoreStub) ServiceAdminQuery(context.Context, string, string, string) (json.RawMessage, error) {
	return nil, errors.New("unexpected service admin query")
}
func (stub *runtimeRegistrationCoreStub) RegisterService(ctx context.Context, metadata domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
	return stub.register(ctx, metadata)
}
func (stub *runtimeRegistrationCoreStub) EnsureServiceLink(context.Context, domain.ServiceLinkPolicyRequest, string) (json.RawMessage, error) {
	return nil, errors.New("unexpected service link")
}

func TestRegisterRuntimeServiceRetriesCoreUntilSDKRegistrationIsVisible(t *testing.T) {
	attempts := 0
	core := &runtimeRegistrationCoreStub{}
	core.register = func(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
		attempts++
		if attempts < 3 {
			return nil, domain.CoreHTTPError{Operation: "service register", Status: 503, Code: "service_registration_failed"}
		}
		return json.RawMessage(`{"accepted":true}`), nil
	}

	result, err := registerRuntimeService(context.Background(), core, domain.ServiceRegistrationMetadata{InstanceID: "echo"}, time.Second)
	if err != nil {
		t.Fatalf("registerRuntimeService() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("registration attempts = %d, want 3", attempts)
	}
	if string(result) != `{"accepted":true}` {
		t.Fatalf("registration result = %s", result)
	}
	if core.metadataCalls != 0 {
		t.Fatalf("CLI pre-read Core service metadata %d times", core.metadataCalls)
	}
}

func TestRegisterRuntimeServiceDoesNotRetryNonTransientCoreFailure(t *testing.T) {
	wantErr := domain.CoreHTTPError{Operation: "service register", Status: 400, Code: "invalid_request"}
	attempts := 0
	core := &runtimeRegistrationCoreStub{register: func(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
		attempts++
		return nil, wantErr
	}}

	_, err := registerRuntimeService(context.Background(), core, domain.ServiceRegistrationMetadata{InstanceID: "echo"}, time.Second)
	if !errors.As(err, new(domain.CoreHTTPError)) {
		t.Fatalf("registerRuntimeService() error = %v, want Core HTTP error", err)
	}
	if attempts != 1 {
		t.Fatalf("registration attempts = %d, want 1", attempts)
	}
}

func TestRegisterRuntimeServiceAllowsCoreSDKMetadataReadDeadline(t *testing.T) {
	var remaining time.Duration
	core := &runtimeRegistrationCoreStub{register: func(ctx context.Context, _ domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("registration context has no deadline")
		}
		remaining = time.Until(deadline)
		return json.RawMessage(`{"accepted":true}`), nil
	}}

	if _, err := registerRuntimeService(context.Background(), core, domain.ServiceRegistrationMetadata{InstanceID: "echo"}, 60*time.Second); err != nil {
		t.Fatalf("registerRuntimeService() error = %v", err)
	}
	if remaining < 20*time.Second || remaining > 25*time.Second {
		t.Fatalf("registration attempt deadline = %s, want about 25 seconds", remaining)
	}
}
