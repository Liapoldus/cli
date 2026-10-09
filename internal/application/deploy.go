package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

const (
	ExitOK         = 0
	ExitArguments  = 2
	ExitRevision   = 3
	ExitValidation = 4
	ExitTarget     = 5
	ExitOperation  = 6
	ExitDegraded   = 7
	ExitConflict   = 8
)

// DeploymentService handles Core-facing plan and apply use cases.
type DeploymentService struct {
	Source domain.ProjectSource
	Core   domain.CoreClient
	Bundle BundleService
}

func (s DeploymentService) Plan(ctx context.Context, projectPath, revision, target string, expectedGeneration *int64) (domain.Bundle, []byte, error) {
	bundle, request, err := s.request(ctx, projectPath, revision, target, expectedGeneration, nil)
	if err != nil {
		return domain.Bundle{}, nil, err
	}
	result, err := s.Core.Plan(ctx, request)
	if err != nil {
		return domain.Bundle{}, nil, wrapCoreError(err)
	}
	return bundle, result.Raw, nil
}

func (s DeploymentService) Apply(ctx context.Context, projectPath, revision, target string, expectedGeneration *int64, approval *domain.ApprovalEvidence, confirm bool, wait bool, idempotencyKey string) (domain.Bundle, []byte, error) {
	if !confirm {
		return domain.Bundle{}, nil, errors.New("apply requires --confirm")
	}
	status, err := s.Source.WorkingTreeStatus(ctx, projectPath)
	if err != nil {
		return domain.Bundle{}, nil, fmt.Errorf("check working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return domain.Bundle{}, nil, domain.ErrDirtyWorkingTree
	}
	bundle, request, err := s.request(ctx, projectPath, revision, target, expectedGeneration, approval)
	if err != nil {
		return domain.Bundle{}, nil, err
	}
	if idempotencyKey == "" {
		idempotencyKey = "apply-" + strings.TrimPrefix(bundle.Digest, "sha256:")
	}
	result, err := s.Core.Apply(ctx, request, idempotencyKey)
	if err != nil {
		return domain.Bundle{}, nil, wrapCoreError(err)
	}
	if wait {
		if err := s.wait(ctx, result); err != nil {
			return domain.Bundle{}, nil, err
		}
	}
	return bundle, result.Raw, nil
}

func (s DeploymentService) request(ctx context.Context, projectPath, revision, target string, expectedGeneration *int64, approval *domain.ApprovalEvidence) (domain.Bundle, domain.ConfigBundleRequest, error) {
	if strings.TrimSpace(revision) == "" {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, errors.New("--revision is required")
	}
	if strings.TrimSpace(target) == "" {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, errors.New("--target is required")
	}
	bundle, err := s.Bundle.Build(ctx, projectPath, revision, target)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, err
	}
	repository, err := s.Source.RemoteOrigin(ctx, projectPath)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, fmt.Errorf("read Git remote: %w", err)
	}
	projectID, err := s.Source.ProjectID(ctx, projectPath, bundle.Revision)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, fmt.Errorf("read project identity: %w", err)
	}
	if approval != nil {
		if err := approval.Validate(repository, bundle.Revision, target); err != nil {
			return domain.Bundle{}, domain.ConfigBundleRequest{}, err
		}
	}
	return bundle, domain.ConfigBundleRequest{
		Project: domain.ProjectRef{ID: projectID, Repository: repository, Revision: bundle.Revision},
		Bundle:  domain.BundleRef{SchemaVersion: bundle.SchemaVersion, Digest: bundle.Digest, Services: bundle.Services, Links: bundle.Links},
		Target:  domain.TargetRef{Environment: target, ExpectedGeneration: expectedGeneration},
	}, nil
}

func wrapCoreError(err error) error {
	var httpError domain.CoreHTTPError
	var contractError domain.CoreContractError
	if errors.As(err, &httpError) || errors.As(err, &contractError) {
		return err
	}
	return TargetError{Err: err}
}

func (s DeploymentService) wait(ctx context.Context, response domain.ApplyResponse) error {
	for _, operationID := range response.OperationIDs {
		if _, err := (OperationService{Core: s.Core}).Watch(ctx, operationID, 250*time.Millisecond, 60*time.Second); err != nil {
			return err
		}
	}
	return nil
}
