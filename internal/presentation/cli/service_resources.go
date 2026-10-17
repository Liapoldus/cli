package cli

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
)

const maximumServiceResourceItems = 50

var errInvalidServiceResource = errors.New("service resource catalog is invalid or unavailable")

type serviceResourceQuerySpec struct {
	pageID       string
	resourceID   string
	valuePointer string
	labelPointer string
}

type serviceResourceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func serviceResourceQuery(manifest json.RawMessage, resourceID string) (serviceResourceQuerySpec, error) {
	if len(manifest) == 0 || len(manifest) > 262_144 || !safeServiceResourceID(resourceID) {
		return serviceResourceQuerySpec{}, errInvalidServiceResource
	}
	var document struct {
		Resources []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			Provider      string `json:"provider"`
			PageID        string `json:"pageId"`
			QueryResource string `json:"queryResource"`
			ValuePointer  string `json:"valuePointer"`
			LabelPointer  string `json:"labelPointer"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(manifest, &document); err != nil || len(document.Resources) > 256 {
		return serviceResourceQuerySpec{}, errInvalidServiceResource
	}
	var selected *serviceResourceQuerySpec
	seen := make(map[string]bool, len(document.Resources))
	for _, resource := range document.Resources {
		if !safeServiceResourceID(resource.ID) || !safeServiceResourceKind(resource.Kind) || seen[resource.ID] {
			return serviceResourceQuerySpec{}, errInvalidServiceResource
		}
		seen[resource.ID] = true
		if resource.ID != resourceID {
			continue
		}
		if !safeServiceResourceKind(resource.Kind) || resource.Provider != "service-admin" || !safeServiceResourceID(resource.PageID) || !safeServiceResourceID(resource.QueryResource) ||
			!safeResourcePointer(resource.ValuePointer) || !safeResourcePointer(resource.LabelPointer) {
			return serviceResourceQuerySpec{}, errInvalidServiceResource
		}
		if selected != nil {
			return serviceResourceQuerySpec{}, errInvalidServiceResource
		}
		selected = &serviceResourceQuerySpec{
			pageID: resource.PageID, resourceID: resource.QueryResource,
			valuePointer: resource.ValuePointer, labelPointer: resource.LabelPointer,
		}
	}
	if selected == nil {
		return serviceResourceQuerySpec{}, errInvalidServiceResource
	}
	return *selected, nil
}

func sanitizeServiceResourceItems(response json.RawMessage, valuePointer, labelPointer string) ([]serviceResourceOption, error) {
	if len(response) == 0 || len(response) > 1_048_576 || !safeResourcePointer(valuePointer) || !safeResourcePointer(labelPointer) {
		return nil, errInvalidServiceResource
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(response)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, errInvalidServiceResource
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errInvalidServiceResource
	}
	items, ok := document["items"].([]any)
	if !ok || len(items) > maximumServiceResourceItems {
		return nil, errInvalidServiceResource
	}
	values := make(map[string]bool, len(items))
	result := make([]serviceResourceOption, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errInvalidServiceResource
		}
		value, valueOK := valueAtResourcePointer(item, valuePointer)
		label, labelOK := valueAtResourcePointer(item, labelPointer)
		valueText, validValue := safeResourceText(value)
		labelText, validLabel := safeResourceText(label)
		if !valueOK || !labelOK || !validValue || !validLabel {
			continue
		}
		if values[valueText] {
			continue
		}
		values[valueText] = true
		result = append(result, serviceResourceOption{Value: valueText, Label: labelText})
	}
	return result, nil
}

func valueAtResourcePointer(value any, pointer string) (any, bool) {
	segments, ok := resourcePointerSegments(pointer)
	if !ok {
		return nil, false
	}
	current := value
	for _, segment := range segments {
		switch container := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = container[segment]
			if !ok {
				return nil, false
			}
		case []any:
			if segment == "" || (len(segment) > 1 && segment[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(container) {
				return nil, false
			}
			current = container[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func resourcePointerSegments(pointer string) ([]string, bool) {
	if !strings.HasPrefix(pointer, "/") || len(pointer) > 1024 || strings.ContainsRune(pointer, '\x00') {
		return nil, false
	}
	parts := strings.Split(pointer[1:], "/")
	segments := make([]string, len(parts))
	for index, part := range parts {
		if part == "" || strings.Contains(part, "*") {
			return nil, false
		}
		for offset := 0; offset < len(part); offset++ {
			if part[offset] == '~' {
				if offset+1 >= len(part) || part[offset+1] != '0' && part[offset+1] != '1' {
					return nil, false
				}
				offset++
			}
		}
		segments[index] = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		if !safeResourceFieldName(segments[index]) {
			return nil, false
		}
	}
	return segments, true
}

func safeResourcePointer(pointer string) bool {
	_, ok := resourcePointerSegments(pointer)
	return ok
}

func safeResourceFieldName(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"secret", "password", "token", "privatekey", "private_key", "credential", "certificatekey"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return value != "__proto__" && value != "constructor" && value != "prototype"
}

func safeServiceResourceID(value string) bool {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func safeServiceResourceKind(value string) bool {
	switch value {
	case "file", "certificate", "site", "artifact":
		return true
	default:
		return false
	}
}

func safeResourceText(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len(text) > 512 || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return "", false
	}
	return text, true
}
