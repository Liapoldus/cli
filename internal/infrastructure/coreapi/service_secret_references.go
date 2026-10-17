package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Liapoldus/cli/internal/domain"
)

func (c Client) ListServiceSecretReferences(ctx context.Context, serviceID, purpose string) ([]domain.ServiceSecretReference, error) {
	if !validSecretServiceRequest(c.Token, serviceID) || len(purpose) > 192 || strings.ContainsAny(purpose, "\x00\r\n") {
		return nil, domain.CoreContractError{Operation: "service secret list", Message: "invalid request"}
	}
	path := "/api/services/" + url.PathEscape(serviceID) + "/secrets"
	if purpose != "" {
		query := url.Values{"purpose": []string{purpose}}
		path += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, err := c.do(request)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service secret list", status, body)
	}
	var envelope struct {
		Items []domain.ServiceSecretReference `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || len(envelope.Items) > 4096 {
		return nil, domain.CoreContractError{Operation: "service secret list", Message: "invalid response"}
	}
	for _, item := range envelope.Items {
		if !item.Validate() || item.ServiceID != serviceID || purpose != "" && item.Purpose != purpose {
			return nil, domain.CoreContractError{Operation: "service secret list", Message: "invalid metadata"}
		}
	}
	return envelope.Items, nil
}

func (c Client) CreateServiceSecretReference(ctx context.Context, serviceID string, reference domain.ServiceSecretReference) (domain.ServiceSecretReference, error) {
	if !validSecretServiceRequest(c.Token, serviceID) || !validOpaqueSecretReference(reference.Reference) ||
		!validSecretPurposeValue(reference.Purpose) || strings.TrimSpace(reference.Label) == "" || strings.TrimSpace(reference.Label) != reference.Label || len([]rune(reference.Label)) > 128 || strings.ContainsAny(reference.Label, "\x00\r\n") {
		return domain.ServiceSecretReference{}, domain.CoreContractError{Operation: "service secret create", Message: "invalid request"}
	}
	requestBody := struct {
		Reference string `json:"reference"`
		Purpose   string `json:"purpose"`
		Label     string `json:"label"`
	}{Reference: reference.Reference, Purpose: reference.Purpose, Label: reference.Label}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return domain.ServiceSecretReference{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/api/services/"+url.PathEscape(serviceID)+"/secrets"), bytes.NewReader(encoded))
	if err != nil {
		return domain.ServiceSecretReference{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, err := c.do(request)
	if err != nil {
		return domain.ServiceSecretReference{}, err
	}
	if status < 200 || status >= 300 {
		return domain.ServiceSecretReference{}, domain.NewCoreHTTPError("service secret create", status, body)
	}
	var created domain.ServiceSecretReference
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&created) != nil || decoder.Decode(new(any)) != io.EOF || !created.Validate() || created.ServiceID != serviceID ||
		created.Reference != reference.Reference || created.Purpose != reference.Purpose || created.Label != reference.Label || created.State != "active" {
		return domain.ServiceSecretReference{}, domain.CoreContractError{Operation: "service secret create", Message: "invalid response"}
	}
	return created, nil
}

func (c Client) RevokeServiceSecretReference(ctx context.Context, serviceID, reference string) error {
	if !validSecretServiceRequest(c.Token, serviceID) || !validOpaqueSecretReference(reference) {
		return domain.CoreContractError{Operation: "service secret revoke", Message: "invalid request"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url("/api/services/"+url.PathEscape(serviceID)+"/secrets/"+url.PathEscape(reference)), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, err := c.do(request)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return domain.NewCoreHTTPError("service secret revoke", status, body)
	}
	if status != http.StatusNoContent || len(body) != 0 {
		return domain.CoreContractError{Operation: "service secret revoke", Message: "invalid response"}
	}
	return nil
}

func validSecretServiceRequest(token, serviceID string) bool {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(serviceID) != serviceID || serviceID == "" || len(serviceID) > 128 {
		return false
	}
	for _, character := range serviceID {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

func validOpaqueSecretReference(value string) bool {
	const prefix = "ssr:v1:"
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validSecretPurposeValue(value string) bool {
	if value == "" || len(value) > 192 || strings.TrimSpace(value) != value {
		return false
	}
	for _, part := range strings.Split(value, ".") {
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

var _ domain.ServiceSecretReferenceClient = Client{}
