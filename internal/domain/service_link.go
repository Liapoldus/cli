package domain

import "encoding/json"

// ServiceLinkPolicyRequest is the typed Core Management API intent derived
// from a canonical project link. It contains only generic placement, carrier
// and contract constraints; product payloads remain service-owned.
type ServiceLinkPolicyRequest struct {
	CallerInstanceID string            `json:"callerInstanceId"`
	TargetInstanceID string            `json:"targetInstanceId"`
	Rules            []ServiceLinkRule `json:"rules"`
}

type ServiceLinkRule struct {
	PlacementRule     string                     `json:"placementRule"`
	Carrier           string                     `json:"carrier"`
	Weight            uint16                     `json:"weight"`
	RequiredContracts []ServiceLinkContractRange `json:"requiredContracts"`
}

type ServiceLinkContractRange struct {
	ContractID              string `json:"contractId"`
	MinimumVersion          string `json:"minimumVersion"`
	MaximumVersionExclusive string `json:"maximumVersionExclusive"`
}

func (policy ServiceLinkPolicyRequest) Valid() bool {
	return policy.CallerInstanceID != "" && policy.TargetInstanceID != "" &&
		policy.CallerInstanceID != policy.TargetInstanceID && len(policy.Rules) > 0
}

func (policy ServiceLinkPolicyRequest) Raw() (json.RawMessage, error) {
	body, err := json.Marshal(policy)
	return json.RawMessage(body), err
}
