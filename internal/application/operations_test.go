package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

type operationCoreStub struct {
	states   []string
	err      error
	index    int
	response domain.OperationResponse
}

func (s *operationCoreStub) Health(context.Context) (int, error) { return 200, nil }
func (s *operationCoreStub) Plan(context.Context, domain.ConfigBundleRequest) (domain.PlanResponse, error) {
	return domain.PlanResponse{}, nil
}
func (s *operationCoreStub) Apply(context.Context, domain.ConfigBundleRequest, string) (domain.ApplyResponse, error) {
	return domain.ApplyResponse{}, nil
}
func (s *operationCoreStub) Operation(context.Context, string) (domain.OperationResponse, error) {
	if s.err != nil {
		return domain.OperationResponse{}, s.err
	}
	if s.response.State != "" {
		return s.response, nil
	}
	if len(s.states) == 0 {
		return domain.OperationResponse{State: "pending"}, nil
	}
	index := s.index
	if index >= len(s.states) {
		index = len(s.states) - 1
	}
	s.index++
	return domain.OperationResponse{State: s.states[index]}, nil
}

func TestDevelopmentWaitPreservesDegradedReplicaOutcomes(t *testing.T) {
	want := []domain.OperationReplicaOutcome{{
		ReplicaID: "server-1", DesiredGeneration: 7, State: "failed", FailureCode: "replica_unavailable",
	}}
	core := &operationCoreStub{response: domain.OperationResponse{
		ID: "op-1", State: "degraded", ReplicaOutcomes: want,
	}}
	service := DevelopmentDeploymentService{Core: core}
	got, err := service.wait(context.Background(), domain.ApplyResponse{OperationIDs: []string{"op-1"}})
	var stateError OperationStateError
	if !errors.As(err, &stateError) || stateError.State != "degraded" {
		t.Fatalf("expected degraded operation error, got %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partial replica outcomes lost: got=%+v want=%+v", got, want)
	}
}
func (s *operationCoreStub) ResolveOperations(context.Context, string, []string) ([]domain.OperationReference, error) {
	return nil, nil
}
func (s *operationCoreStub) ServiceMetadata(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (s *operationCoreStub) ServiceAdminQuery(context.Context, string, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[]}`), nil
}
func (s *operationCoreStub) RegisterService(context.Context, domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (s *operationCoreStub) EnsureServiceLink(context.Context, domain.ServiceLinkPolicyRequest, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func TestOperationWatchSuccessfulStates(t *testing.T) {
	for _, state := range []string{"succeeded", "completed"} {
		t.Run(state, func(t *testing.T) {
			service := OperationService{Core: &operationCoreStub{states: []string{state}}}
			result, err := service.Watch(context.Background(), "op-1", time.Millisecond, time.Second)
			if err != nil || result.State != state {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOperationWatchObservedReportsOnlyStateTransitions(t *testing.T) {
	service := OperationService{Core: &operationCoreStub{states: []string{"pending", "pending", "running", "running", "succeeded"}}}
	var observed []string
	result, err := service.WatchObserved(context.Background(), "op-1", time.Millisecond, time.Second, func(_, state string) {
		observed = append(observed, state)
	})
	if err != nil || result.State != "succeeded" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"pending", "running", "succeeded"}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed states=%v want=%v", observed, want)
	}
}

func TestOperationWatchObservedRejectsInvalidTiming(t *testing.T) {
	service := OperationService{Core: &operationCoreStub{states: []string{"succeeded"}}}
	if _, err := service.WatchObserved(context.Background(), "op-1", 0, time.Second, nil); err == nil {
		t.Fatal("accepted zero polling interval")
	}
}

func TestOperationWatchFailedAndDegradedStates(t *testing.T) {
	for _, state := range []string{"failed", "degraded"} {
		t.Run(state, func(t *testing.T) {
			service := OperationService{Core: &operationCoreStub{states: []string{state}}}
			_, err := service.Watch(context.Background(), "op-1", time.Millisecond, time.Second)
			var stateError OperationStateError
			if !errors.As(err, &stateError) || stateError.State != state {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOperationWatchTimeoutAndCancellation(t *testing.T) {
	service := OperationService{Core: &operationCoreStub{states: []string{"pending"}}}
	_, err := service.Watch(context.Background(), "op-1", time.Millisecond, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout err=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Watch(ctx, "op-1", time.Millisecond, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestOperationGetWrapsTransportFailure(t *testing.T) {
	service := OperationService{Core: &operationCoreStub{err: errors.New("connection refused")}}
	_, err := service.Get(context.Background(), "op-1")
	var targetError TargetError
	if !errors.As(err, &targetError) || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err=%v", err)
	}
}
