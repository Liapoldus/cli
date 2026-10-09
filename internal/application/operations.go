package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

// OperationService provides read-only operation observation for operators and CI.
type OperationService struct {
	Core domain.CoreClient
}

type OperationStateError struct {
	OperationID string
	State       string
}

type TargetError struct {
	Err error
}

func (e TargetError) Error() string { return fmt.Sprintf("target request failed: %v", e.Err) }
func (e TargetError) Unwrap() error { return e.Err }

func (e OperationStateError) Error() string {
	return fmt.Sprintf("operation %s %s", e.OperationID, e.State)
}

func (s OperationService) Get(ctx context.Context, operationID string) (domain.OperationResponse, error) {
	if operationID == "" {
		return domain.OperationResponse{}, errors.New("operation ID is required")
	}
	response, err := s.Core.Operation(ctx, operationID)
	if err != nil {
		var httpError domain.CoreHTTPError
		var contractError domain.CoreContractError
		if errors.As(err, &httpError) || errors.As(err, &contractError) {
			return domain.OperationResponse{}, err
		}
		return domain.OperationResponse{}, TargetError{Err: err}
	}
	return response, nil
}

func (s OperationService) Watch(ctx context.Context, operationID string, interval, timeout time.Duration) (domain.OperationResponse, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		response, err := s.Get(ctx, operationID)
		if err != nil {
			return domain.OperationResponse{}, err
		}
		switch response.State {
		case "succeeded", "completed":
			return response, nil
		case "failed", "degraded":
			return response, OperationStateError{OperationID: operationID, State: response.State}
		}
		select {
		case <-ctx.Done():
			return domain.OperationResponse{}, ctx.Err()
		case <-deadline.C:
			return domain.OperationResponse{}, fmt.Errorf("operation %s timed out", operationID)
		case <-ticker.C:
		}
	}
}
