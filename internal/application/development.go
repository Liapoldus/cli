package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

// DevelopmentDeploymentService applies the exact current Studio working copy
// to the local target through Core, without creating a Git commit.
type DevelopmentDeploymentService struct {
	Source            domain.ProjectSource
	Core              domain.CoreClient
	Snapshots         domain.ProjectSnapshotter
	Recovery          domain.ApplyRecoveryStore
	RecoveryTargetKey string
	OnOperationState  func(operationID, state string)
}

type DevelopmentApplyResult struct {
	CoreResponse    json.RawMessage
	ReplicaOutcomes []domain.OperationReplicaOutcome
}

func (s DevelopmentDeploymentService) Plan(ctx context.Context, projectPath, target string, expectedGeneration *int64) (domain.Bundle, []byte, error) {
	if err := s.validate(target); err != nil {
		return domain.Bundle{}, nil, err
	}
	snapshot, err := s.capture(ctx, projectPath)
	if err != nil {
		return domain.Bundle{}, nil, err
	}
	defer snapshot.Close()
	bundle, request, err := s.request(ctx, snapshot, projectPath, expectedGeneration)
	if err != nil {
		return domain.Bundle{}, nil, err
	}
	result, err := s.Core.Plan(ctx, request)
	if err != nil {
		return domain.Bundle{}, nil, wrapCoreError(err)
	}
	return bundle, result.Raw, nil
}

func (s DevelopmentDeploymentService) Apply(ctx context.Context, projectPath, target string, expectedGeneration *int64, confirm, wait, retryFailed bool, idempotencyKey string) (domain.Bundle, DevelopmentApplyResult, error) {
	if !confirm {
		return domain.Bundle{}, DevelopmentApplyResult{}, errors.New("development apply requires --confirm")
	}
	if err := s.validate(target); err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, err
	}
	if s.Recovery == nil || s.RecoveryTargetKey == "" {
		return domain.Bundle{}, DevelopmentApplyResult{}, errors.New("development apply recovery queue is not configured")
	}
	release, err := s.Recovery.Acquire(ctx, s.RecoveryTargetKey)
	if err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, err
	}
	defer func() { _ = release() }()
	retryingFailed, err := s.recoverPending(ctx, retryFailed)
	if err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, err
	}
	snapshot, err := s.capture(ctx, projectPath)
	if err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, err
	}
	defer snapshot.Close()
	bundle, request, err := s.request(ctx, snapshot, projectPath, expectedGeneration)
	if err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, err
	}
	if idempotencyKey == "" {
		// A content-only key is not sufficient: after a successful apply, the
		// same working-tree bundle is a new desired operation against newer
		// service configuration revisions. Include Core's current revisions so
		// an unchanged retry is idempotent, while a later apply is not mistaken
		// for a conflicting replay.
		planResult, planErr := s.Core.Plan(ctx, request)
		if planErr != nil {
			return domain.Bundle{}, DevelopmentApplyResult{}, wrapCoreError(planErr)
		}
		idempotencyKey, err = developmentIdempotencyKey(request, bundle, planResult.Raw)
		if err != nil {
			return domain.Bundle{}, DevelopmentApplyResult{}, fmt.Errorf("derive development apply idempotency key: %w", err)
		}
	}
	if retryingFailed {
		idempotencyKey, err = freshRetryIdempotencyKey(idempotencyKey)
		if err != nil {
			return domain.Bundle{}, DevelopmentApplyResult{}, fmt.Errorf("derive explicit retry idempotency key: %w", err)
		}
	}
	intent := domain.ApplyIntent{Scope: "config-bundle/" + request.Project.Revision, Keys: make([]string, 0, len(request.Bundle.Services))}
	for _, service := range request.Bundle.Services {
		intent.Keys = append(intent.Keys, idempotencyKey+":"+service.ID)
	}
	if len(intent.Keys) > 0 {
		if err := s.Recovery.Save(s.RecoveryTargetKey, intent); err != nil {
			return domain.Bundle{}, DevelopmentApplyResult{}, fmt.Errorf("persist apply recovery intent before Core submission: %w", err)
		}
	}
	result, err := s.Core.Apply(ctx, request, idempotencyKey)
	if err != nil {
		return domain.Bundle{}, DevelopmentApplyResult{}, wrapCoreError(err)
	}
	applyResult := DevelopmentApplyResult{CoreResponse: result.Raw}
	if len(result.OperationIDs) == 0 {
		if err := s.Recovery.Clear(s.RecoveryTargetKey); err != nil {
			return domain.Bundle{}, DevelopmentApplyResult{}, fmt.Errorf("clear empty apply recovery intent: %w", err)
		}
	}
	if wait {
		applyResult.ReplicaOutcomes, err = s.wait(ctx, result)
		if err != nil {
			return bundle, applyResult, err
		}
		if len(result.OperationIDs) > 0 {
			if err := s.Recovery.Clear(s.RecoveryTargetKey); err != nil {
				return bundle, applyResult, fmt.Errorf("clear completed apply recovery intent: %w", err)
			}
		}
	}
	return bundle, applyResult, nil
}

func (s DevelopmentDeploymentService) recoverPending(ctx context.Context, retryFailed bool) (bool, error) {
	intent, found, err := s.Recovery.Load(s.RecoveryTargetKey)
	if err != nil || !found {
		if retryFailed && err == nil {
			return false, errors.New("--retry-failed was requested, but no previous failed apply is recorded")
		}
		return false, err
	}
	operations, err := s.Core.ResolveOperations(ctx, intent.Scope, intent.Keys)
	if err != nil {
		return false, fmt.Errorf("resolve previously accepted Core apply before retry: %w", err)
	}
	failedFound := false
	for _, operation := range operations {
		switch operation.State {
		case "pending", "running":
			if _, err := (OperationService{Core: s.Core}).WatchObserved(ctx, operation.ID, 250*time.Millisecond, 60*time.Second, s.OnOperationState); err != nil {
				return false, fmt.Errorf("recover previous Core apply operation %s before retry: %w", operation.ID, err)
			}
		case "succeeded", "completed":
			continue
		case "failed":
			if !retryFailed {
				return false, fmt.Errorf("previous Core apply operation %s failed; inspect its report, then use --retry-failed only to explicitly retry", operation.ID)
			}
			failedFound = true
		case "degraded":
			return false, fmt.Errorf("previous Core apply operation %s is degraded; reconcile the target before retry", operation.ID)
		default:
			return false, fmt.Errorf("previous Core apply operation %s has unsupported state %q; reconcile the target before retry", operation.ID, operation.State)
		}
	}
	if retryFailed && !failedFound {
		return false, errors.New("--retry-failed was requested, but the recovery journal contains no failed operation")
	}
	if err := s.Recovery.Clear(s.RecoveryTargetKey); err != nil {
		return false, fmt.Errorf("clear recovered Core apply journal: %w", err)
	}
	return failedFound, nil
}

func freshRetryIdempotencyKey(previous string) (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("liapoldus-dev-retry-v1\x00" + previous + "\x00" + hex.EncodeToString(nonce[:])))
	return "dev-retry-" + hex.EncodeToString(digest[:]), nil
}

func developmentIdempotencyKey(request domain.ConfigBundleRequest, bundle domain.Bundle, planRaw []byte) (string, error) {
	var response struct {
		Plan struct {
			Valid    bool   `json:"valid"`
			Digest   string `json:"digest"`
			Services []struct {
				ID              string `json:"id"`
				CurrentRevision int64  `json:"currentRevision"`
			} `json:"services"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(planRaw, &response); err != nil {
		return "", fmt.Errorf("decode Core plan: %w", err)
	}
	if !response.Plan.Valid || response.Plan.Digest != bundle.Digest {
		return "", errors.New("Core plan does not match the requested bundle")
	}
	if len(response.Plan.Services) != len(request.Bundle.Services) {
		return "", errors.New("Core plan service revisions do not match the requested bundle")
	}
	services := append([]struct {
		ID              string `json:"id"`
		CurrentRevision int64  `json:"currentRevision"`
	}(nil), response.Plan.Services...)
	sort.Slice(services, func(left, right int) bool { return services[left].ID < services[right].ID })
	expectedIDs := make([]string, 0, len(request.Bundle.Services))
	for _, service := range request.Bundle.Services {
		expectedIDs = append(expectedIDs, service.ID)
	}
	sort.Strings(expectedIDs)

	identity := request.Project.ID + "\x00" + request.Project.Revision + "\x00" + request.Target.Environment + "\x00" + bundle.Digest
	if request.Target.ExpectedGeneration != nil {
		identity += fmt.Sprintf("\x00%d", *request.Target.ExpectedGeneration)
	}
	for index, service := range services {
		if service.ID == "" || service.ID != expectedIDs[index] || (index > 0 && services[index-1].ID == service.ID) {
			return "", errors.New("Core plan service revisions do not match the requested bundle")
		}
		identity += fmt.Sprintf("\x00%s\x00%d", service.ID, service.CurrentRevision)
	}
	keyDigest := sha256.Sum256([]byte(identity))
	return "dev-apply-" + hex.EncodeToString(keyDigest[:]), nil
}

func (s DevelopmentDeploymentService) validate(target string) error {
	if target != "local" {
		return errors.New("development working-tree apply is available only for --target local")
	}
	if s.Source == nil || s.Core == nil || s.Snapshots == nil {
		return errors.New("development apply requires project source, Core client and snapshotter")
	}
	return nil
}

func (s DevelopmentDeploymentService) capture(ctx context.Context, projectPath string) (domain.ProjectSnapshot, error) {
	snapshot, err := s.Snapshots.Capture(ctx, projectPath)
	if err != nil {
		return nil, fmt.Errorf("capture immutable development snapshot: %w", err)
	}
	if snapshot == nil || strings.TrimSpace(snapshot.RootPath()) == "" {
		return nil, errors.New("snapshotter returned an invalid project snapshot")
	}
	return snapshot, nil
}

func (s DevelopmentDeploymentService) request(ctx context.Context, snapshot domain.ProjectSnapshot, sourceProjectPath string, expectedGeneration *int64) (domain.Bundle, domain.ConfigBundleRequest, error) {
	const target = "local"
	const sourceRevision = domain.WorkingTreeRevision
	bundle, err := (BundleService{Source: s.Source}).Build(ctx, snapshot.RootPath(), sourceRevision, target)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, err
	}
	// The revision is a stable content identity, not HEAD or a mutable path.
	bundle.Revision = "working-tree:" + strings.TrimPrefix(bundle.Digest, "sha256:")
	repository, err := s.Source.RemoteOrigin(ctx, sourceProjectPath)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, fmt.Errorf("read Git remote: %w", err)
	}
	projectID, err := s.Source.ProjectID(ctx, snapshot.RootPath(), sourceRevision)
	if err != nil {
		return domain.Bundle{}, domain.ConfigBundleRequest{}, fmt.Errorf("read project identity from snapshot: %w", err)
	}
	return bundle, domain.ConfigBundleRequest{
		Project: domain.ProjectRef{ID: projectID, Repository: repository, Revision: bundle.Revision},
		Bundle:  domain.BundleRef{SchemaVersion: bundle.SchemaVersion, Digest: bundle.Digest, Services: bundle.Services, Links: bundle.Links},
		Target:  domain.TargetRef{Environment: target, ExpectedGeneration: expectedGeneration},
	}, nil
}

func (s DevelopmentDeploymentService) wait(ctx context.Context, response domain.ApplyResponse) ([]domain.OperationReplicaOutcome, error) {
	outcomes := make([]domain.OperationReplicaOutcome, 0)
	for _, operationID := range response.OperationIDs {
		operation, err := (OperationService{Core: s.Core}).WatchObserved(ctx, operationID, 250*time.Millisecond, 60*time.Second, s.OnOperationState)
		if len(operation.ReplicaOutcomes) > 128-len(outcomes) {
			return outcomes, errors.New("Core returned too many replica outcomes for one apply result")
		}
		outcomes = append(outcomes, operation.ReplicaOutcomes...)
		if err != nil {
			return outcomes, err
		}
	}
	return outcomes, nil
}
