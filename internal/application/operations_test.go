package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

type operationCoreStub struct {
	states []string
	err    error
	index  int
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
