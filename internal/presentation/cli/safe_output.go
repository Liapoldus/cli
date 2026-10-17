package cli

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

// CLI JSON is also the boundary consumed by desktop renderers. These response
// models intentionally expose only the metadata needed by the UI, not the
// local registry's filesystem paths, credential references, or launch config.
type targetSummary struct {
	Name              string `json:"name"`
	Environment       string `json:"environment"`
	Transport         string `json:"transport"`
	Approval          string `json:"approval,omitempty"`
	RuntimeConfigured bool   `json:"runtimeConfigured"`
}

func summarizeTarget(target domain.Target) targetSummary {
	return targetSummary{
		Name: target.Name, Environment: target.Environment, Transport: target.Transport,
		Approval: target.Approval, RuntimeConfigured: target.RuntimeImage != "",
	}
}

type artifactSummary struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
	Trusted  bool   `json:"trusted"`
	Command  string `json:"command,omitempty"`
}

func summarizeArtifact(artifact domain.Artifact) artifactSummary {
	return artifactSummary{
		ID: artifact.ID, Version: artifact.Version, Platform: artifact.Platform,
		Digest: artifact.Digest, Trusted: artifact.Trusted, Command: artifact.Command,
	}
}

type runtimePlanSummary struct {
	Revision string                  `json:"revision"`
	Target   string                  `json:"target"`
	Services []runtimeServiceSummary `json:"services"`
}

type operationSummary struct {
	OperationID     string                           `json:"operationId"`
	Target          string                           `json:"target"`
	State           string                           `json:"state"`
	ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes,omitempty"`
}

type developmentApplySummary struct {
	Accepted        bool                             `json:"accepted"`
	Revision        string                           `json:"revision"`
	Digest          string                           `json:"digest"`
	Response        json.RawMessage                  `json:"response"`
	ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes,omitempty"`
}

func summarizeDevelopmentApply(bundle domain.Bundle, result application.DevelopmentApplyResult) developmentApplySummary {
	return developmentApplySummary{
		Accepted: true, Revision: bundle.Revision, Digest: bundle.Digest,
		Response: result.CoreResponse, ReplicaOutcomes: result.ReplicaOutcomes,
	}
}

type runtimeServiceSummary struct {
	Artifact        artifactSummary `json:"artifact"`
	Command         string          `json:"command"`
	ControlEndpoint string          `json:"controlEndpoint"`
	PeerEndpoint    string          `json:"peerEndpoint"`
}

func summarizeRuntimePlan(plan domain.RuntimePlan) runtimePlanSummary {
	services := make([]runtimeServiceSummary, 0, len(plan.Services))
	for _, service := range plan.Services {
		services = append(services, runtimeServiceSummary{
			Artifact: summarizeArtifact(service.Artifact), Command: service.Command,
			ControlEndpoint: service.ControlEndpoint, PeerEndpoint: service.PeerEndpoint,
		})
	}
	return runtimePlanSummary{Revision: plan.Revision, Target: plan.Target, Services: services}
}

type dockerInspectSummary struct {
	Name   string                `json:"name"`
	Image  string                `json:"image,omitempty"`
	State  string                `json:"state,omitempty"`
	Health string                `json:"health,omitempty"`
	Ports  []dockerPublishedPort `json:"ports,omitempty"`
}

type dockerPublishedPort struct {
	ContainerPort string `json:"containerPort"`
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort"`
}

// safeDockerInspectSummary projects the Docker response onto an explicit
// allowlist. In particular, Config.Env and Mounts.Source never cross into
// stdout/events, where Studio renders CLI payloads.
func safeDockerInspectSummary(body []byte, expectedName string) (dockerInspectSummary, error) {
	var records []struct {
		Name   string `json:"Name"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
			Health *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(body, &records); err != nil || len(records) != 1 {
		return dockerInspectSummary{}, errors.New("Docker inspect returned an invalid response")
	}
	record := records[0]
	name := strings.TrimPrefix(record.Name, "/")
	if expectedName == "" || name != expectedName {
		return dockerInspectSummary{}, errors.New("Docker inspect returned an unexpected container")
	}
	result := dockerInspectSummary{Name: name, Image: record.Config.Image, State: record.State.Status}
	if record.State.Health != nil {
		result.Health = record.State.Health.Status
	}
	for containerPort, bindings := range record.NetworkSettings.Ports {
		for _, binding := range bindings {
			result.Ports = append(result.Ports, dockerPublishedPort{
				ContainerPort: containerPort, HostIP: binding.HostIP, HostPort: binding.HostPort,
			})
		}
	}
	sort.Slice(result.Ports, func(i, j int) bool {
		if result.Ports[i].ContainerPort == result.Ports[j].ContainerPort {
			if result.Ports[i].HostIP == result.Ports[j].HostIP {
				return result.Ports[i].HostPort < result.Ports[j].HostPort
			}
			return result.Ports[i].HostIP < result.Ports[j].HostIP
		}
		return result.Ports[i].ContainerPort < result.Ports[j].ContainerPort
	})
	return result, nil
}
