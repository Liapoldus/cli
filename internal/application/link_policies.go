package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

// LinkPolicies converts canonical project links into generic Core policy
// intents. Core receives no product method names or payload schemas; those
// remain owned by the plugin manifests and pluginprotocol data plane.
func LinkPolicies(rawLinks []json.RawMessage) ([]domain.PluginLinkPolicyRequest, error) {
	policies := make([]domain.PluginLinkPolicyRequest, 0, len(rawLinks))
	for index, raw := range rawLinks {
		var link struct {
			Caller    string `json:"caller"`
			Target    string `json:"target"`
			Transport string `json:"transport"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&link); err != nil {
			return nil, fmt.Errorf("link %d: invalid JSON: %w", index, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil {
			return nil, fmt.Errorf("link %d: trailing JSON", index)
		} else if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("link %d: invalid trailing JSON: %w", index, err)
		}
		if strings.TrimSpace(link.Caller) == "" || strings.TrimSpace(link.Target) == "" || link.Caller == link.Target {
			return nil, fmt.Errorf("link %d: caller and target are required and must differ", index)
		}
		policy := domain.PluginLinkPolicyRequest{
			CallerInstanceID: link.Caller,
			TargetInstanceID: link.Target,
			Rules: []domain.PluginLinkRule{{
				PlacementRule:     "remote",
				Carrier:           "tcp",
				Weight:            100,
				RequiredContracts: []domain.PluginLinkContractRange{},
			}},
		}
		if link.Transport == "unix" {
			policy.Rules[0].PlacementRule = "same-placement"
			policy.Rules[0].Carrier = "unix"
		}
		if !policy.Valid() {
			return nil, fmt.Errorf("link %d: invalid policy", index)
		}
		policies = append(policies, policy)
	}
	return policies, nil
}
