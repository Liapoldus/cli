package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/Liapoldus/cli/internal/domain"
)

const maxAdminSurfaceJSONBytes = 1 << 20

var adminSurfaceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type adminSurfaceDescriptor struct {
	Service string `json:"service"`
	Version int    `json:"version"`
	Pages   []struct {
		ID       string `json:"id"`
		Sections []struct {
			ID             string   `json:"id"`
			Kind           string   `json:"kind"`
			DataCapability string   `json:"dataCapability"`
			QueryResource  string   `json:"queryResource"`
			Columns        []string `json:"columns"`
			Actions        []struct {
				ID           string          `json:"id"`
				Confirmation string          `json:"confirmation"`
				Dangerous    bool            `json:"dangerous"`
				IfMatchField string          `json:"ifMatchField"`
				InputSchema  json.RawMessage `json:"inputSchema"`
			} `json:"actions"`
		} `json:"sections"`
	} `json:"pages"`
}

func decodeAdminSurface(raw json.RawMessage) (adminSurfaceDescriptor, error) {
	var descriptor adminSurfaceDescriptor
	if len(raw) == 0 || len(raw) > maxAdminSurfaceJSONBytes || json.Unmarshal(raw, &descriptor) != nil ||
		descriptor.Version != 1 || !adminSurfaceIdentifier.MatchString(descriptor.Service) ||
		len(descriptor.Pages) == 0 || len(descriptor.Pages) > 128 {
		return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
	}
	seenPages := make(map[string]bool, len(descriptor.Pages))
	for _, page := range descriptor.Pages {
		if !adminSurfaceIdentifier.MatchString(page.ID) || seenPages[page.ID] || len(page.Sections) == 0 || len(page.Sections) > 128 {
			return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
		}
		seenPages[page.ID] = true
		seenSections := make(map[string]bool, len(page.Sections))
		for _, section := range page.Sections {
			if !adminSurfaceIdentifier.MatchString(section.ID) || seenSections[section.ID] || len(section.Actions) > 128 {
				return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
			}
			seenSections[section.ID] = true
			if section.Kind == "table" {
				if section.QueryResource != "" && !adminSurfaceIdentifier.MatchString(section.QueryResource) || strings.TrimSpace(section.DataCapability) == "" || len(section.Columns) == 0 || len(section.Columns) > 128 {
					return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
				}
				seenColumns := make(map[string]bool, len(section.Columns))
				for _, column := range section.Columns {
					if !adminSurfaceIdentifier.MatchString(column) || sensitiveAdminKey(column) || seenColumns[column] {
						return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
					}
					seenColumns[column] = true
				}
			} else if section.Kind != "detail" {
				return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
			}
			seenActions := make(map[string]bool, len(section.Actions))
			for _, action := range section.Actions {
				if !adminSurfaceIdentifier.MatchString(action.ID) || seenActions[action.ID] || len(action.InputSchema) > 65_536 {
					return adminSurfaceDescriptor{}, errors.New("service admin surface is invalid")
				}
				seenActions[action.ID] = true
			}
		}
	}
	return descriptor, nil
}

func (descriptor adminSurfaceDescriptor) table(pageID, sectionID string) (string, []string, bool) {
	for _, page := range descriptor.Pages {
		if page.ID != pageID {
			continue
		}
		for _, section := range page.Sections {
			if section.ID == sectionID && section.Kind == "table" && adminSurfaceIdentifier.MatchString(section.QueryResource) {
				return section.QueryResource, append([]string(nil), section.Columns...), true
			}
		}
	}
	return "", nil, false
}

func (descriptor adminSurfaceDescriptor) action(pageID, sectionID, actionID string) (string, json.RawMessage, bool, bool) {
	for _, page := range descriptor.Pages {
		if page.ID != pageID {
			continue
		}
		for _, section := range page.Sections {
			if section.ID != sectionID {
				continue
			}
			for _, action := range section.Actions {
				if action.ID == actionID {
					return action.IfMatchField, append(json.RawMessage(nil), action.InputSchema...), action.Dangerous || strings.TrimSpace(action.Confirmation) != "", true
				}
			}
		}
	}
	return "", nil, false, false
}

// QueryServiceAdminSection checks the live table binding and digest before it
// asks Core to query the service. Core remains the only Service SDK client.
func (client Client) QueryServiceAdminSection(ctx context.Context, request domain.ServiceAdminSectionQuery) (json.RawMessage, error) {
	if !adminSurfaceIdentifier.MatchString(request.ServiceID) || !adminSurfaceIdentifier.MatchString(request.PageID) ||
		!adminSurfaceIdentifier.MatchString(request.ResourceID) || !adminSurfaceIdentifier.MatchString(request.SectionID) ||
		request.Cursor != "" && (len(request.Cursor) > 1024 || strings.IndexFunc(request.Cursor, unicode.IsControl) >= 0) {
		return nil, errors.New("service, page, section, resource or cursor is invalid")
	}
	surface, err := client.ReadServiceAdminSurface(ctx, request.ServiceID)
	if err != nil {
		return nil, err
	}
	if surface.Digest != request.SurfaceDigest {
		return nil, domain.CoreContractError{Operation: "service admin query", Message: "surface changed during request"}
	}
	descriptor, err := decodeAdminSurface(surface.Descriptor)
	if err != nil || descriptor.Service != request.ServiceID {
		return nil, domain.CoreContractError{Operation: "service admin query", Message: "invalid live surface"}
	}
	resourceID, _, found := descriptor.table(request.PageID, request.SectionID)
	if !found || resourceID != request.ResourceID {
		return nil, domain.CoreContractError{Operation: "service admin query", Message: "query is not declared by the live surface"}
	}
	query := map[string]any{"resource": request.ResourceID, "limit": 50}
	if request.Cursor != "" {
		query["cursor"] = request.Cursor
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	path := "/api/services/" + url.PathEscape(request.ServiceID) + "/admin/pages/" + url.PathEscape(request.PageID) + "/query"
	coreRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	coreRequest.Header.Set("Accept", "application/json")
	coreRequest.Header.Set("Content-Type", "application/json")
	coreRequest.Header.Set("Authorization", "Bearer "+client.Token)
	coreRequest.Header.Set("X-Liapoldus-Admin-Surface-Digest", surface.Digest)
	responseBody, status, err := client.do(coreRequest)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service admin query", status, responseBody)
	}
	if !json.Valid(responseBody) || len(responseBody) > maxAdminSurfaceJSONBytes {
		return nil, domain.CoreContractError{Operation: "service admin query", Message: "invalid or oversized result"}
	}
	return json.RawMessage(responseBody), nil
}

// InvokeServiceAdminAction rechecks the exact descriptor digest and declared
// page/section/action immediately before forwarding the bounded JSON request.
func (client Client) InvokeServiceAdminAction(ctx context.Context, request domain.ServiceAdminActionInvocation) (json.RawMessage, error) {
	if !adminSurfaceIdentifier.MatchString(request.ServiceID) || !adminSurfaceIdentifier.MatchString(request.PageID) ||
		!adminSurfaceIdentifier.MatchString(request.SectionID) || !adminSurfaceIdentifier.MatchString(request.ActionID) ||
		request.SurfaceDigest == "" || len(request.Input) == 0 || len(request.Input) > maxAdminSurfaceJSONBytes ||
		!json.Valid(request.Input) || strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 ||
		strings.IndexFunc(request.IdempotencyKey, unicode.IsControl) >= 0 {
		return nil, errors.New("service admin action request is invalid")
	}
	if strings.TrimSpace(client.Token) == "" {
		return nil, errors.New("core token is required")
	}
	surface, err := client.ReadServiceAdminSurface(ctx, request.ServiceID)
	if err != nil {
		return nil, err
	}
	if surface.Digest != request.SurfaceDigest {
		return nil, domain.CoreContractError{Operation: "service admin action", Message: "surface changed during request"}
	}
	descriptor, err := decodeAdminSurface(surface.Descriptor)
	if err != nil || descriptor.Service != request.ServiceID {
		return nil, domain.CoreContractError{Operation: "service admin action", Message: "invalid live surface"}
	}
	ifMatchField, inputSchema, _, found := descriptor.action(request.PageID, request.SectionID, request.ActionID)
	if !found || !validAdminActionInput(request.Input, inputSchema, ifMatchField, request.IfMatch) {
		return nil, domain.CoreContractError{Operation: "service admin action", Message: "action is not declared or input is invalid"}
	}
	path := "/api/services/" + url.PathEscape(request.ServiceID) + "/admin/pages/" + url.PathEscape(request.PageID) + "/actions/" + url.PathEscape(request.ActionID)
	coreRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url(path), bytes.NewReader(request.Input))
	if err != nil {
		return nil, err
	}
	coreRequest.Header.Set("Accept", "application/json")
	coreRequest.Header.Set("Content-Type", "application/json")
	coreRequest.Header.Set("Authorization", "Bearer "+client.Token)
	coreRequest.Header.Set("X-Liapoldus-Admin-Surface-Digest", surface.Digest)
	coreRequest.Header.Set("Idempotency-Key", request.IdempotencyKey)
	coreRequest.Header.Set("If-Match", request.IfMatch)
	responseBody, status, err := client.do(coreRequest)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service admin action", status, responseBody)
	}
	if !json.Valid(responseBody) || len(responseBody) > maxAdminSurfaceJSONBytes {
		return nil, domain.CoreContractError{Operation: "service admin action", Message: "invalid or oversized result"}
	}
	return json.RawMessage(responseBody), nil
}

func validAdminActionInput(raw, schemaRaw json.RawMessage, ifMatchField, ifMatch string) bool {
	if len(schemaRaw) == 0 || len(schemaRaw) > 65_536 || !json.Valid(schemaRaw) {
		return false
	}
	var schema struct {
		Type                 string   `json:"type"`
		AdditionalProperties bool     `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Type      string   `json:"type"`
			MinLength *int     `json:"minLength"`
			MaxLength *int     `json:"maxLength"`
			Minimum   *float64 `json:"minimum"`
			Maximum   *float64 `json:"maximum"`
			Enum      []any    `json:"enum"`
		} `json:"properties"`
	}
	var input map[string]any
	schemaDecoder := json.NewDecoder(bytes.NewReader(schemaRaw))
	schemaDecoder.DisallowUnknownFields()
	inputDecoder := json.NewDecoder(bytes.NewReader(raw))
	inputDecoder.UseNumber()
	if schemaDecoder.Decode(&schema) != nil || inputDecoder.Decode(&input) != nil || schema.Type != "object" || schema.AdditionalProperties || input == nil || len(input) > 128 || len(schema.Properties) > 128 {
		return false
	}
	if ifMatchField != "" {
		if !containsString(schema.Required, ifMatchField) || strings.TrimSpace(ifMatch) == "" || input[ifMatchField] != ifMatch {
			return false
		}
	} else if strings.TrimSpace(ifMatch) == "" {
		return false
	}
	for _, required := range schema.Required {
		if _, exists := input[required]; !exists {
			return false
		}
	}
	for key, value := range input {
		field, exists := schema.Properties[key]
		if !exists || sensitiveAdminKey(key) || field.Type == "" {
			return false
		}
		if !validAdminScalar(value, field.Type, field.MinLength, field.MaxLength, field.Minimum, field.Maximum, field.Enum) {
			return false
		}
	}
	return true
}

func validAdminScalar(value any, fieldType string, minLength, maxLength *int, minimum, maximum *float64, choices []any) bool {
	var actual any
	switch fieldType {
	case "string":
		text, ok := value.(string)
		if !ok || strings.IndexFunc(text, unicode.IsControl) >= 0 || minLength != nil && len([]rune(text)) < *minLength || maxLength != nil && len([]rune(text)) > *maxLength {
			return false
		}
		actual = text
	case "integer", "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := number.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || fieldType == "integer" && math.Trunc(parsed) != parsed || minimum != nil && parsed < *minimum || maximum != nil && parsed > *maximum {
			return false
		}
		actual = parsed
	case "boolean":
		if _, ok := value.(bool); !ok {
			return false
		}
		actual = value
	default:
		return false
	}
	if len(choices) > 0 {
		for _, choice := range choices {
			if actual == choice {
				return true
			}
			if number, ok := actual.(float64); ok {
				if enumNumber, ok := choice.(float64); ok && number == enumNumber {
					return true
				}
			}
		}
		return false
	}
	return true
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func sensitiveAdminKey(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"secret", "password", "passwd", "token", "privatekey", "private_key", "credential", "authorization", "authkey"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
