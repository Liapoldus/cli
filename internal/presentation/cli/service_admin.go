package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

const maxServiceAdminRequestBytes = 1 << 20

var safeAdminIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type serviceAdminDescriptor struct {
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

func (runner Runner) serviceAdmin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" || (args[0] != "query" && args[0] != "invoke") {
		return fail(stderr, application.ExitArguments, "service admin query|invoke <service-id> is required")
	}
	options, err := parseOptions(args[2:])
	if err != nil {
		return fail(stderr, application.ExitArguments, err.Error())
	}
	if options.target != "local" {
		return fail(stderr, application.ExitArguments, "local --target is required for service admin actions")
	}
	if runner.CoreFactory == nil {
		return fail(stderr, application.ExitTarget, "Core client is not configured")
	}
	if options.surfaceDigest != "" && !validArtifactDigest(options.surfaceDigest) {
		return fail(stderr, application.ExitArguments, "--surface-digest is invalid")
	}
	connection, err := runner.credentials(ctx, options)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	client, err := runner.CoreFactory(connection)
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	reader, ok := client.(domain.ServiceAdminSurfaceReader)
	if !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support service admin-surface inspection")
	}
	surface, descriptor, err := readServiceAdminDescriptor(ctx, reader, args[1])
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if options.surfaceDigest != "" && surface.Digest != options.surfaceDigest {
		return fail(stderr, application.ExitValidation, "service admin surface changed since it was read")
	}
	if args[0] == "query" {
		if options.page == "" || options.section == "" || !safeAdminIdentifier.MatchString(options.page) || !safeAdminIdentifier.MatchString(options.section) {
			return fail(stderr, application.ExitArguments, "service admin query requires --page and --section")
		}
		resourceID, columns, found := descriptor.table(options.page, options.section)
		if !found {
			return fail(stderr, application.ExitValidation, "table is not declared by the live service admin surface")
		}
		request := domain.ServiceAdminSectionQuery{
			ServiceID: args[1], PageID: options.page, SectionID: options.section,
			ResourceID: resourceID, Cursor: options.cursor, SurfaceDigest: surface.Digest,
		}
		var response json.RawMessage
		if pager, supported := client.(domain.ServiceAdminQueryPager); supported {
			response, err = pager.QueryServiceAdminSection(ctx, request)
		} else if options.cursor == "" {
			response, err = client.ServiceAdminQuery(ctx, args[1], options.page, resourceID)
		} else {
			err = errors.New("core client does not support table pagination")
		}
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		filtered, err := sanitizeServiceAdminRows(response, columns)
		if err != nil {
			return fail(stderr, application.ExitTarget, err.Error())
		}
		return encode(stdout, map[string]any{"serviceId": args[1], "pageId": options.page, "sectionId": options.section, "data": filtered})
	}
	return runner.invokeServiceAdminAction(ctx, client, descriptor, surface, args[1], options, stdout, stderr)
}

func readServiceAdminDescriptor(ctx context.Context, reader domain.ServiceAdminSurfaceReader, serviceID string) (domain.ServiceAdminSurface, serviceAdminDescriptor, error) {
	if !safeAdminIdentifier.MatchString(serviceID) {
		return domain.ServiceAdminSurface{}, serviceAdminDescriptor{}, errors.New("service id is invalid")
	}
	surface, err := reader.ReadServiceAdminSurface(ctx, serviceID)
	if err != nil {
		return domain.ServiceAdminSurface{}, serviceAdminDescriptor{}, err
	}
	if !validServiceAdminSurface(surface.Descriptor) || !validServiceAdminSurfaceDigest(surface.Digest, surface.Descriptor) {
		return domain.ServiceAdminSurface{}, serviceAdminDescriptor{}, errors.New("core returned an invalid service admin-surface descriptor")
	}
	var descriptor serviceAdminDescriptor
	if json.Unmarshal(surface.Descriptor, &descriptor) != nil || descriptor.Service != serviceID || descriptor.Version != 1 {
		return domain.ServiceAdminSurface{}, serviceAdminDescriptor{}, errors.New("core returned an invalid service admin-surface descriptor")
	}
	return surface, descriptor, nil
}

func (descriptor serviceAdminDescriptor) table(pageID, sectionID string) (string, []string, bool) {
	for _, page := range descriptor.Pages {
		if page.ID != pageID {
			continue
		}
		for _, section := range page.Sections {
			if section.ID != sectionID || section.Kind != "table" || strings.TrimSpace(section.DataCapability) == "" ||
				!safeAdminIdentifier.MatchString(section.QueryResource) || len(section.Columns) == 0 || len(section.Columns) > 128 {
				continue
			}
			seen := make(map[string]bool, len(section.Columns))
			for _, column := range section.Columns {
				if !safeAdminIdentifier.MatchString(column) || sensitiveAdminValue(column) || seen[column] {
					return "", nil, false
				}
				seen[column] = true
			}
			return section.QueryResource, append([]string(nil), section.Columns...), true
		}
	}
	return "", nil, false
}

func (runner Runner) invokeServiceAdminAction(ctx context.Context, client domain.CoreClient, descriptor serviceAdminDescriptor, surface domain.ServiceAdminSurface, serviceID string, options commandOptions, stdout, stderr io.Writer) int {
	if options.page == "" || options.section == "" || options.action == "" || !safeAdminIdentifier.MatchString(options.page) ||
		!safeAdminIdentifier.MatchString(options.section) || !safeAdminIdentifier.MatchString(options.action) ||
		!options.projectExplicit || options.requestFile == "" || !options.confirm {
		return fail(stderr, application.ExitArguments, "service admin invoke requires --page, --section, --action, --project, --request-file and --confirm")
	}
	if len(options.idempotencyKey) > 128 || options.idempotencyKey != "" && strings.IndexFunc(options.idempotencyKey, unicode.IsControl) >= 0 {
		return fail(stderr, application.ExitArguments, "--idempotency-key is invalid")
	}
	inputPath, info, err := resolveProjectFile(options.project, options.requestFile)
	if err != nil || info.IsDir() || info.Size() < 2 || info.Size() > maxServiceAdminRequestBytes {
		return fail(stderr, application.ExitValidation, "service admin action input file is invalid")
	}
	input, err := os.ReadFile(inputPath)
	if err != nil || int64(len(input)) > maxServiceAdminRequestBytes || !safeAdminActionJSON(input) {
		return fail(stderr, application.ExitValidation, "service admin action input is invalid")
	}
	ifMatchField, declared, requiredConfirmation, found := descriptor.action(options.page, options.section, options.action)
	if !found {
		return fail(stderr, application.ExitValidation, "action is not declared by the live service admin surface")
	}
	if options.ifMatch == "" {
		options.ifMatch = "*"
	}
	if ifMatchField != "" {
		var values map[string]any
		if json.Unmarshal(input, &values) != nil || values[ifMatchField] != options.ifMatch {
			return fail(stderr, application.ExitValidation, "action compare-and-swap value does not match its declared input")
		}
	}
	if requiredConfirmation && !options.confirm {
		return fail(stderr, application.ExitArguments, "service admin action requires explicit confirmation")
	}
	if !serviceAdminInputMatchesSchema(input, declared) {
		return fail(stderr, application.ExitValidation, "service admin action input does not match the live action schema")
	}
	if _, ok := client.(domain.ServiceAdminActionInvoker); !ok {
		return fail(stderr, application.ExitTarget, "Core client does not support service admin actions")
	}
	if options.idempotencyKey == "" {
		var key [16]byte
		if _, err := rand.Read(key[:]); err != nil {
			return fail(stderr, application.ExitTarget, "could not create service action key")
		}
		options.idempotencyKey = "cli-admin-" + hex.EncodeToString(key[:])
	}
	result, err := client.(domain.ServiceAdminActionInvoker).InvokeServiceAdminAction(ctx, domain.ServiceAdminActionInvocation{
		ServiceID: serviceID, PageID: options.page, SectionID: options.section, ActionID: options.action,
		Input: input, IfMatch: options.ifMatch, IdempotencyKey: options.idempotencyKey, SurfaceDigest: surface.Digest,
	})
	if err != nil {
		return fail(stderr, application.ExitTarget, err.Error())
	}
	if len(result) == 0 || len(result) > maxServiceAdminRequestBytes || !json.Valid(result) {
		return fail(stderr, application.ExitTarget, "Core returned an invalid service action result")
	}
	// The plugin only needs to know that the declared action completed. Results
	// remain inside the CLI so unexpected service fields cannot reach the iframe.
	return encode(stdout, map[string]any{"completed": true})
}

func (descriptor serviceAdminDescriptor) action(pageID, sectionID, actionID string) (string, json.RawMessage, bool, bool) {
	for _, page := range descriptor.Pages {
		if page.ID != pageID {
			continue
		}
		for _, section := range page.Sections {
			if section.ID != sectionID {
				continue
			}
			for _, action := range section.Actions {
				if action.ID == actionID && len(action.InputSchema) > 0 && len(action.InputSchema) <= 65_536 {
					return action.IfMatchField, append(json.RawMessage(nil), action.InputSchema...), action.Dangerous || strings.TrimSpace(action.Confirmation) != "", true
				}
			}
		}
	}
	return "", nil, false, false
}

func serviceAdminInputMatchesSchema(raw, schemaRaw json.RawMessage) bool {
	var schema struct {
		Type                 string   `json:"type"`
		AdditionalProperties *bool    `json:"additionalProperties"`
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
	var values map[string]any
	schemaDecoder := json.NewDecoder(strings.NewReader(string(schemaRaw)))
	schemaDecoder.DisallowUnknownFields()
	valueDecoder := json.NewDecoder(strings.NewReader(string(raw)))
	valueDecoder.UseNumber()
	if schemaDecoder.Decode(&schema) != nil || valueDecoder.Decode(&values) != nil || schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties || values == nil || len(values) > 128 || len(schema.Properties) > 128 {
		return false
	}
	for _, key := range schema.Required {
		if _, found := values[key]; !found {
			return false
		}
	}
	for key, value := range values {
		field, found := schema.Properties[key]
		if !found || sensitiveAdminValue(key) || !serviceAdminScalarMatches(value, field.Type, field.MinLength, field.MaxLength, field.Minimum, field.Maximum, field.Enum) {
			return false
		}
	}
	return true
}

func serviceAdminScalarMatches(value any, kind string, minLength, maxLength *int, minimum, maximum *float64, choices []any) bool {
	var comparable any
	switch kind {
	case "string":
		text, ok := value.(string)
		if !ok || strings.IndexFunc(text, unicode.IsControl) >= 0 || minLength != nil && len([]rune(text)) < *minLength || maxLength != nil && len([]rune(text)) > *maxLength {
			return false
		}
		comparable = text
	case "integer", "number":
		number, ok := value.(json.Number)
		parsed, err := number.Float64()
		if !ok || err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || kind == "integer" && math.Trunc(parsed) != parsed || minimum != nil && parsed < *minimum || maximum != nil && parsed > *maximum {
			return false
		}
		comparable = parsed
	case "boolean":
		if _, ok := value.(bool); !ok {
			return false
		}
		comparable = value
	default:
		return false
	}
	if len(choices) == 0 {
		return true
	}
	for _, choice := range choices {
		if comparable == choice {
			return true
		}
		if number, ok := comparable.(float64); ok {
			if enumNumber, ok := choice.(float64); ok && number == enumNumber {
				return true
			}
		}
	}
	return false
}

func safeAdminActionJSON(raw []byte) bool {
	if len(raw) < 2 || len(raw) > maxServiceAdminRequestBytes {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var values map[string]any
	if decoder.Decode(&values) != nil || values == nil || len(values) > 128 {
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return false
	}
	return safeAdminActionValue(values, 0)
}

func safeAdminActionValue(value any, depth int) bool {
	if depth > 16 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 128 {
			return false
		}
		for key, child := range typed {
			if sensitiveAdminValue(key) || key == "__proto__" || key == "constructor" || key == "prototype" || !safeAdminActionValue(child, depth+1) {
				return false
			}
		}
	case []any:
		return false
	case string:
		return strings.IndexFunc(typed, unicode.IsControl) < 0 && len(typed) <= 4096
	case json.Number:
		number, err := typed.Float64()
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	case bool, nil:
	default:
		return false
	}
	return true
}

func sensitiveAdminValue(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"secret", "password", "passwd", "token", "privatekey", "private_key", "credential", "authorization", "authkey"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sanitizeServiceAdminRows(raw json.RawMessage, columns []string) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxServiceAdminRequestBytes || len(columns) == 0 || len(columns) > 128 {
		return nil, errors.New("service admin query result is invalid")
	}
	var response struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"nextCursor"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&response) != nil || response.Items == nil || len(response.Items) > 100 {
		return nil, errors.New("service admin query result is invalid")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("service admin query result is invalid")
	}
	allowed := make(map[string]bool, len(columns))
	for _, column := range columns {
		if !safeAdminIdentifier.MatchString(column) || sensitiveAdminValue(column) || allowed[column] {
			return nil, errors.New("service admin table columns are invalid")
		}
		allowed[column] = true
	}
	rows := make([]map[string]any, len(response.Items))
	for index, item := range response.Items {
		if item == nil || len(item) > 256 {
			return nil, errors.New("service admin query row is invalid")
		}
		row := make(map[string]any, len(allowed))
		for key, value := range item {
			if !allowed[key] {
				continue
			}
			safe, valid := safeAdminCell(value)
			if !valid {
				return nil, errors.New("service admin query cell is invalid")
			}
			row[key] = safe
		}
		rows[index] = row
	}
	if response.NextCursor != nil && (len(*response.NextCursor) > 1024 || strings.IndexFunc(*response.NextCursor, unicode.IsControl) >= 0) {
		return nil, errors.New("service admin pagination cursor is invalid")
	}
	result, err := json.Marshal(map[string]any{"items": rows, "nextCursor": response.NextCursor})
	if err != nil || len(result) > maxServiceAdminRequestBytes {
		return nil, errors.New("service admin query result is invalid")
	}
	return result, nil
}

func safeAdminCell(value any) (any, bool) {
	switch typed := value.(type) {
	case nil, bool:
		return typed, true
	case string:
		return typed, len(typed) <= 4096 && strings.IndexFunc(typed, unicode.IsControl) < 0
	case json.Number:
		number, err := typed.Float64()
		return typed, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return nil, false
	}
}
