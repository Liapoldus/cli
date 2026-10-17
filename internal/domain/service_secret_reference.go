package domain

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ServiceSecretReference is safe metadata returned by Core. It never contains
// a value, storage path, grant token or provider key.
type ServiceSecretReference struct {
	Reference string `json:"reference"`
	ServiceID string `json:"serviceId"`
	Purpose   string `json:"purpose"`
	Label     string `json:"label"`
	State     string `json:"state"`
	CreatedAt string `json:"createdAt"`
}

func (reference ServiceSecretReference) Validate() bool {
	if len(reference.Reference) != len("ssr:v1:")+32 || !strings.HasPrefix(reference.Reference, "ssr:v1:") ||
		!validNeutralID(reference.ServiceID) || !validSecretPurpose(reference.Purpose) ||
		strings.TrimSpace(reference.Label) == "" || strings.TrimSpace(reference.Label) != reference.Label ||
		utf8.RuneCountInString(reference.Label) > 128 || reference.CreatedAt == "" ||
		(reference.State != "active" && reference.State != "revoked") {
		return false
	}
	for _, character := range reference.Reference[len("ssr:v1:"):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	for _, character := range reference.Label {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSecretPurpose(value string) bool {
	if value == "" || len(value) > 192 || strings.TrimSpace(value) != value {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if part == "" || part[0] < 'a' || part[0] > 'z' {
			return false
		}
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
				return false
			}
		}
	}
	return true
}

func validNeutralID(value string) bool {
	if value == "" || len(value) > 128 || !((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= '0' && value[0] <= '9')) {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// ServiceSecretReferenceClient exposes the Core-owned, service-scoped metadata
// catalog. Secret bytes are stored separately in the local OS vault.
type ServiceSecretReferenceClient interface {
	ListServiceSecretReferences(ctx context.Context, serviceID, purpose string) ([]ServiceSecretReference, error)
	CreateServiceSecretReference(ctx context.Context, serviceID string, reference ServiceSecretReference) (ServiceSecretReference, error)
	RevokeServiceSecretReference(ctx context.Context, serviceID, reference string) error
}
