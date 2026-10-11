package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/artifacts"
	"github.com/Liapoldus/cli/internal/infrastructure/coreapi"
	"github.com/Liapoldus/cli/internal/infrastructure/credentials"
	"github.com/Liapoldus/cli/internal/infrastructure/docker"
	"github.com/Liapoldus/cli/internal/infrastructure/git"
	"github.com/Liapoldus/cli/internal/infrastructure/localcore"
	"github.com/Liapoldus/cli/internal/infrastructure/targets"
	"github.com/Liapoldus/cli/internal/presentation/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := cli.New(git.Repository{}, targets.Default(), func(path string) domain.TargetRegistry {
		return targets.Registry{Path: path}
	}, credentials.KeyringProvider{}, os.Stdin, localcore.StateFiles{}, func(connection domain.CoreConnection) (domain.CoreClient, error) {
		return coreapi.New(connection)
	})
	runner.Artifacts = artifacts.Default()
	runner.Docker = docker.Default()
	os.Exit(runner.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
