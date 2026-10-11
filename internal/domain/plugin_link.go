package domain

import "encoding/json"

// PluginLinkPolicyRequest is the typed Core Management API intent derived
// from a canonical project link. It contains only generic placement, carrier
// and contract constraints; product payloads remain plugin-owned.
type PluginLinkPolicyRequest struct {
	CallerInstanceID string           `json:"callerInstanceId"`
	TargetInstanceID string           `json:"targetInstanceId"`
	Rules            []PluginLinkRule `json:"rules"`
}

type PluginLinkRule struct {
	PlacementRule     string                    `json:"placementRule"`
	Carrier           string                    `json:"carrier"`
	Weight            uint16                    `json:"weight"`
	RequiredContracts []PluginLinkContractRange `json:"requiredContracts"`
}

type PluginLinkContractRange struct {
	ContractID              string `json:"contractId"`
	MinimumVersion          string `json:"minimumVersion"`
	MaximumVersionExclusive string `json:"maximumVersionExclusive"`
}

func (policy PluginLinkPolicyRequest) Valid() bool {
	return policy.CallerInstanceID != "" && policy.TargetInstanceID != "" &&
		policy.CallerInstanceID != policy.TargetInstanceID && len(policy.Rules) > 0
}

func (policy PluginLinkPolicyRequest) Raw() (json.RawMessage, error) {
	body, err := json.Marshal(policy)
	return json.RawMessage(body), err
}
