package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Liapoldus/cli/internal/domain"
)

// BundleService builds deterministic bundles from one immutable Git revision.
type BundleService struct {
	Source domain.ProjectSource
}

func (s BundleService) Build(ctx context.Context, projectPath, revision, target string) (domain.Bundle, error) {
	if s.Source == nil {
		return domain.Bundle{}, errors.New("project source is required")
	}
	resolved, err := s.Source.ResolveRevision(ctx, projectPath, revision)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("resolve revision: %w", err)
	}
	files, err := s.Source.Files(ctx, projectPath, resolved)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("read project files: %w", err)
	}
	services, err := s.Source.ServiceSettings(ctx, projectPath, resolved)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("read service settings: %w", err)
	}
	links, err := s.Source.Links(ctx, projectPath, resolved)
	if err != nil {
		return domain.Bundle{}, fmt.Errorf("read service links: %w", err)
	}
	bundle := domain.Bundle{
		SchemaVersion: "core-config/v2",
		ProjectPath:   projectPath,
		Revision:      resolved,
		Target:        target,
		Files:         files,
		Services:      services,
		Links:         links,
	}
	bundle.Digest = domain.BundleDigest(files)
	if err := bundle.Validate(); err != nil {
		return domain.Bundle{}, fmt.Errorf("validate bundle: %w", err)
	}
	return bundle, nil
}
