package coreapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Liapoldus/cli/internal/domain"
)

// Client implements the Core Management API port.
type Client struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

func (c Client) Plan(ctx context.Context, request domain.ConfigBundleRequest) (domain.PlanResponse, error) {
	body, status, err := c.post(ctx, "/api/config-bundles/plan", request, "plan-"+strings.TrimPrefix(request.Bundle.Digest, "sha256:"))
	if err != nil {
		return domain.PlanResponse{}, err
	}
	if status < 200 || status >= 300 {
		return domain.PlanResponse{}, domain.NewCoreHTTPError("plan", status, body)
	}
	if !json.Valid(body) {
		return domain.PlanResponse{}, domain.CoreContractError{Operation: "plan", Message: "invalid JSON"}
	}
	var envelope struct {
		Plan      json.RawMessage `json:"plan"`
		RequestID string          `json:"requestId"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Plan) == 0 || envelope.RequestID == "" {
		return domain.PlanResponse{}, domain.CoreContractError{Operation: "plan", Message: "missing required fields"}
	}
	return domain.PlanResponse{Raw: json.RawMessage(body)}, nil
}

func (c Client) Health(ctx context.Context) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/healthz"), nil)
	if err != nil {
		return 0, err
	}
	_, status, err := c.do(request)
	return status, err
}

// ReadCoreSettings fetches the operator-owned settings snapshot. The raw JSON
// is confined to the CLI process because it can contain container paths; the
// presentation command must project it before writing to Studio.
func (c Client) ReadCoreSettings(ctx context.Context) (domain.CoreSettingsSnapshot, error) {
	if strings.TrimSpace(c.Token) == "" {
		return domain.CoreSettingsSnapshot{}, errors.New("core token is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/v2/settings"), nil)
	if err != nil {
		return domain.CoreSettingsSnapshot{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, headers, err := c.doWithHeaders(request)
	if err != nil {
		return domain.CoreSettingsSnapshot{}, err
	}
	if status < 200 || status >= 300 {
		return domain.CoreSettingsSnapshot{}, domain.NewCoreHTTPError("settings", status, body)
	}
	return decodeCoreSettingsSnapshot(body, headers)
}

// UpdateCoreSettings sends the exact already-validated document using Core's
// ETag compare-and-swap contract. It deliberately performs no restart: local
// runtime orchestration is a separate CLI operation with explicit scope.
func (c Client) UpdateCoreSettings(ctx context.Context, ifMatch string, document json.RawMessage) (domain.CoreSettingsSnapshot, error) {
	if strings.TrimSpace(c.Token) == "" {
		return domain.CoreSettingsSnapshot{}, errors.New("core token is required")
	}
	if !validCoreSettingsDocument(document) || !validCoreSettingsETag(ifMatch) {
		return domain.CoreSettingsSnapshot{}, domain.CoreContractError{Operation: "settings", Message: "invalid update request"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url("/api/v2/settings"), bytes.NewReader(document))
	if err != nil {
		return domain.CoreSettingsSnapshot{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("If-Match", ifMatch)
	body, status, headers, err := c.doWithHeaders(request)
	if err != nil {
		return domain.CoreSettingsSnapshot{}, err
	}
	if status < 200 || status >= 300 {
		return domain.CoreSettingsSnapshot{}, domain.NewCoreHTTPError("settings", status, body)
	}
	return decodeCoreSettingsSnapshot(body, headers)
}

func decodeCoreSettingsSnapshot(body []byte, headers http.Header) (domain.CoreSettingsSnapshot, error) {
	var envelope struct {
		DesiredRevision   int64           `json:"desiredRevision"`
		EffectiveRevision int64           `json:"effectiveRevision"`
		Pending           bool            `json:"pending"`
		RestartRequired   bool            `json:"restartRequired"`
		Settings          json.RawMessage `json:"settings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(new(any)) != io.EOF ||
		envelope.DesiredRevision < 1 || envelope.EffectiveRevision < 0 || envelope.EffectiveRevision > envelope.DesiredRevision ||
		envelope.Pending != (envelope.DesiredRevision != envelope.EffectiveRevision) || envelope.RestartRequired != envelope.Pending ||
		!validCoreSettingsDocument(envelope.Settings) {
		return domain.CoreSettingsSnapshot{}, domain.CoreContractError{Operation: "settings", Message: "invalid response contract"}
	}
	etag := headers.Get("ETag")
	if etag != `"core-settings-`+strconv.FormatInt(envelope.DesiredRevision, 10)+`"` {
		return domain.CoreSettingsSnapshot{}, domain.CoreContractError{Operation: "settings", Message: "invalid settings revision"}
	}
	return domain.CoreSettingsSnapshot{
		DesiredRevision: envelope.DesiredRevision, EffectiveRevision: envelope.EffectiveRevision,
		Pending: envelope.Pending, RestartRequired: envelope.RestartRequired, ETag: etag,
		Settings: append(json.RawMessage(nil), envelope.Settings...),
	}, nil
}

func validCoreSettingsETag(value string) bool {
	if !strings.HasPrefix(value, `"core-settings-`) || !strings.HasSuffix(value, `"`) {
		return false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, `"core-settings-`), `"`)
	parsed, err := strconv.ParseInt(revision, 10, 64)
	return err == nil && parsed > 0 && value == `"core-settings-`+strconv.FormatInt(parsed, 10)+`"`
}

func validCoreSettingsDocument(document []byte) bool {
	trimmed := bytes.TrimSpace(document)
	return len(trimmed) > 0 && len(trimmed) <= 262144 && json.Valid(trimmed) && trimmed[0] == '{'
}

func (c Client) Apply(ctx context.Context, request domain.ConfigBundleRequest, idempotencyKey string) (domain.ApplyResponse, error) {
	body, status, err := c.post(ctx, "/api/config-bundles/apply", request, idempotencyKey)
	if err != nil {
		return domain.ApplyResponse{}, err
	}
	if status < 200 || status >= 300 {
		return domain.ApplyResponse{}, domain.NewCoreHTTPError("apply", status, body)
	}
	var envelope struct {
		Operations []string `json:"operations"`
		RequestID  string   `json:"requestId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "invalid response contract"}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "invalid response contract"}
	}
	if !safeOperationID(envelope.RequestID) {
		return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "invalid required fields"}
	}
	for _, operationID := range envelope.Operations {
		if !safeOperationID(operationID) {
			return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "invalid operation ID"}
		}
	}
	safeResponse, err := json.Marshal(envelope)
	if err != nil {
		return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "invalid response contract"}
	}
	return domain.ApplyResponse{OperationIDs: envelope.Operations, Raw: safeResponse}, nil
}

func (c Client) Operation(ctx context.Context, operationID string) (domain.OperationResponse, error) {
	if !safeOperationID(strings.TrimSpace(operationID)) || strings.TrimSpace(operationID) != operationID {
		return domain.OperationResponse{}, errors.New("operation ID is invalid")
	}
	if strings.TrimSpace(c.Token) == "" {
		return domain.OperationResponse{}, errors.New("core token is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/operations/"+url.PathEscape(operationID)), nil)
	if err != nil {
		return domain.OperationResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, err := c.do(request)
	if err != nil {
		return domain.OperationResponse{}, err
	}
	if status < 200 || status >= 300 {
		return domain.OperationResponse{}, domain.NewCoreHTTPError("operation", status, body)
	}
	var envelope struct {
		ID              string                           `json:"id"`
		Kind            string                           `json:"kind"`
		State           string                           `json:"state"`
		ResourceID      string                           `json:"resourceId,omitempty"`
		CreatedAt       time.Time                        `json:"createdAt"`
		UpdatedAt       *time.Time                       `json:"updatedAt,omitempty"`
		RequestID       string                           `json:"requestId"`
		ErrorCode       string                           `json:"errorCode,omitempty"`
		ReplicaOutcomes []domain.OperationReplicaOutcome `json:"replicaOutcomes,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid response contract"}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid response contract"}
	}
	if !safeOperationID(envelope.ID) || envelope.ID != operationID || !safeOperationID(envelope.Kind) || !safeOperationID(envelope.RequestID) || envelope.CreatedAt.IsZero() {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid required fields"}
	}
	switch envelope.State {
	case "pending", "running", "succeeded", "failed", "degraded":
	default:
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid operation state"}
	}
	if envelope.ErrorCode != "" && !safeOperationErrorCode.MatchString(envelope.ErrorCode) {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid error code"}
	}
	if envelope.ErrorCode != "" && envelope.State != "failed" && envelope.State != "degraded" {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "unexpected operation error code"}
	}
	if len(envelope.ReplicaOutcomes) > 128 {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "too many replica outcomes"}
	}
	seenReplicas := make(map[string]struct{}, len(envelope.ReplicaOutcomes))
	for _, outcome := range envelope.ReplicaOutcomes {
		if !safeOperationID(outcome.ReplicaID) || outcome.DesiredGeneration < 1 || (outcome.State != "pending" && outcome.State != "applied" && outcome.State != "failed") || (outcome.FailureCode != "" && !safeOperationErrorCode.MatchString(outcome.FailureCode)) {
			return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "invalid replica outcome"}
		}
		if outcome.FailureCode != "" && outcome.State != "failed" {
			return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "unexpected replica failure code"}
		}
		if _, exists := seenReplicas[outcome.ReplicaID]; exists {
			return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "duplicate replica outcome"}
		}
		seenReplicas[outcome.ReplicaID] = struct{}{}
	}
	return domain.OperationResponse{ID: envelope.ID, State: envelope.State, ReplicaOutcomes: envelope.ReplicaOutcomes}, nil
}

var safeOperationErrorCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
var safeOperationIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func safeOperationID(value string) bool { return safeOperationIdentity.MatchString(value) }

func (c Client) ResolveOperations(ctx context.Context, scope string, keys []string) ([]domain.OperationReference, error) {
	if strings.TrimSpace(scope) != scope || scope == "" || len(scope) > 1024 || len(keys) == 0 || len(keys) > 64 {
		return nil, errors.New("operation recovery scope and bounded keys are required")
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 512 {
			return nil, errors.New("operation recovery key is invalid")
		}
		if _, exists := seen[key]; exists {
			return nil, errors.New("operation recovery keys must be unique")
		}
		seen[key] = struct{}{}
	}
	body, status, err := c.post(ctx, "/api/operations/resolve", map[string]any{"scope": scope, "keys": keys}, "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("operation resolve", status, body)
	}
	var envelope struct {
		Items []struct {
			ID         string `json:"id"`
			State      string `json:"state"`
			ResourceID string `json:"resourceId"`
			ErrorCode  string `json:"errorCode"`
		} `json:"items"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || strings.TrimSpace(envelope.RequestID) == "" {
		return nil, domain.CoreContractError{Operation: "operation resolve", Message: "missing required fields"}
	}
	result := make([]domain.OperationReference, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		if item.ID == "" || item.State == "" || item.ResourceID == "" {
			return nil, domain.CoreContractError{Operation: "operation resolve", Message: "missing operation metadata"}
		}
		result = append(result, domain.OperationReference{ID: item.ID, State: item.State, ResourceID: item.ResourceID, ErrorCode: item.ErrorCode})
	}
	return result, nil
}

// ServiceMetadata is a Core-backed read. The CLI deliberately knows only the
// Management API resource; it has no Service SDK REST client or service URL
// routing logic.
func (c Client) ServiceMetadata(ctx context.Context, serviceID, view string) (json.RawMessage, error) {
	if strings.TrimSpace(serviceID) == "" || (view != "manifest" && view != "schema" && view != "status") {
		return nil, errors.New("service id and supported metadata view are required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/services/"+url.PathEscape(serviceID)+"/"+view), nil)
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
		return nil, domain.NewCoreHTTPError("service "+view, status, body)
	}
	if !json.Valid(body) {
		return nil, domain.CoreContractError{Operation: "service " + view, Message: "invalid JSON"}
	}
	return json.RawMessage(body), nil
}

// ServiceAdminQuery is a Core-backed service resource read. The CLI obtains
// the current surface digest from Core and sends a typed resource query back
// through Core Management API; it never resolves or calls a service endpoint.
func (c Client) ServiceAdminQuery(ctx context.Context, serviceID, pageID, resourceID string) (json.RawMessage, error) {
	if !validManagementID(serviceID) || !validManagementID(pageID) || !validManagementID(resourceID) {
		return nil, errors.New("service, page and resource identifiers are required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	listRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/services/admin-surfaces"), nil)
	if err != nil {
		return nil, err
	}
	listRequest.Header.Set("Accept", "application/json")
	listRequest.Header.Set("Authorization", "Bearer "+c.Token)
	listBody, status, err := c.do(listRequest)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service resource catalog", status, listBody)
	}
	var surfaces struct {
		Items []struct {
			InstanceID string `json:"instanceId"`
			SHA256     string `json:"sha256"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listBody, &surfaces); err != nil || len(surfaces.Items) > 4096 {
		return nil, domain.CoreContractError{Operation: "service resource catalog", Message: "invalid surface list"}
	}
	digest := ""
	for _, surface := range surfaces.Items {
		if surface.InstanceID != serviceID {
			continue
		}
		if digest != "" || !validSHA256Hex(surface.SHA256) {
			return nil, domain.CoreContractError{Operation: "service resource catalog", Message: "missing or ambiguous service surface"}
		}
		digest = surface.SHA256
	}
	if digest == "" {
		return nil, domain.CoreContractError{Operation: "service resource catalog", Message: "service surface is unavailable"}
	}
	body, err := json.Marshal(map[string]any{"resource": resourceID, "limit": 50})
	if err != nil {
		return nil, err
	}
	path := "/api/services/" + url.PathEscape(serviceID) + "/admin/pages/" + url.PathEscape(pageID) + "/query"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("X-Liapoldus-Admin-Surface-Digest", digest)
	responseBody, status, err := c.do(request)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service resource query", status, responseBody)
	}
	if !json.Valid(responseBody) || len(responseBody) > 1_048_576 {
		return nil, domain.CoreContractError{Operation: "service resource query", Message: "invalid or oversized JSON"}
	}
	return json.RawMessage(responseBody), nil
}

// PublishServiceArtifact sends a bounded artifact action through Core's
// Management API. The live declarative surface is read from Core first; no
// service endpoint is resolved or contacted by this client.
func (c Client) PublishServiceArtifact(ctx context.Context, publication domain.ServiceArtifactPublication) (json.RawMessage, error) {
	if !validManagementID(publication.ServiceID) || !validManagementID(publication.PageID) || !validManagementID(publication.ActionID) {
		return nil, errors.New("service, page and action identifiers are required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	if publication.Artifact == nil || !validManagementHeader(publication.IdempotencyKey, 200) || (publication.IfMatch != "" && !validManagementHeader(publication.IfMatch, 512)) {
		return nil, errors.New("artifact and idempotency key are required")
	}
	contentType, _, err := mime.ParseMediaType(publication.ContentType)
	if err != nil || contentType == "" {
		return nil, errors.New("valid artifact content type is required")
	}
	if len(publication.Metadata) == 0 || !json.Valid(publication.Metadata) || len(publication.Metadata) > 1<<20 {
		return nil, errors.New("artifact metadata must be a bounded JSON value")
	}
	var metadataObject map[string]json.RawMessage
	if err := json.Unmarshal(publication.Metadata, &metadataObject); err != nil || metadataObject == nil {
		return nil, errors.New("artifact metadata must be a JSON object")
	}

	surface, err := c.serviceAdminSurface(ctx, publication.ServiceID)
	if err != nil {
		return nil, err
	}
	action, ok := findArtifactAction(surface.Descriptor, publication.PageID, publication.ActionID)
	if !ok || action.ArtifactInput.MaxBytes <= 0 || action.ArtifactInput.MaxBytes > 1<<30 || action.ArtifactInput.MaxMetadataBytes <= 0 || action.ArtifactInput.MaxMetadataBytes > 1<<20 || action.ArtifactInput.MaxMultipartOverheadBytes <= 0 || action.ArtifactInput.MaxMultipartOverheadBytes > 1<<20 {
		return nil, domain.CoreContractError{Operation: "service artifact action", Message: "undeclared or invalid artifact limits"}
	}
	if int64(len(publication.Metadata)) > action.ArtifactInput.MaxMetadataBytes {
		return nil, errors.New("artifact metadata exceeds the declared action limit")
	}
	mediaAllowed := false
	for _, declared := range action.ArtifactInput.MediaTypes {
		declaredType, _, parseErr := mime.ParseMediaType(declared)
		if parseErr == nil && declaredType == contentType {
			mediaAllowed = true
			break
		}
	}
	if !mediaAllowed {
		return nil, errors.New("artifact content type is not declared by the service action")
	}

	pipeReader, pipeWriter := io.Pipe()
	requestLimit := action.ArtifactInput.MaxBytes + int64(len(publication.Metadata)) + action.ArtifactInput.MaxMultipartOverheadBytes
	multipartWriter := multipart.NewWriter(&limitedWriter{writer: pipeWriter, remaining: requestLimit})
	uploadResult := make(chan error, 1)
	go func() {
		writeErr := writeServiceArtifactMultipart(multipartWriter, publication, contentType, action.ArtifactInput.MaxBytes)
		if writeErr != nil {
			_ = pipeWriter.CloseWithError(writeErr)
		} else {
			writeErr = pipeWriter.Close()
		}
		uploadResult <- writeErr
	}()

	path := "/api/services/" + url.PathEscape(publication.ServiceID) + "/admin/pages/" + url.PathEscape(publication.PageID) + "/actions/" + url.PathEscape(publication.ActionID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), pipeReader)
	if err != nil {
		_ = pipeReader.CloseWithError(err)
		<-uploadResult
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("X-Liapoldus-Admin-Surface-Digest", surface.SHA256)
	request.Header.Set("Idempotency-Key", publication.IdempotencyKey)
	if publication.IfMatch != "" {
		request.Header.Set("If-Match", publication.IfMatch)
	}
	responseBody, status, requestErr := c.do(request)
	if requestErr != nil {
		_ = pipeReader.CloseWithError(requestErr)
	}
	uploadErr := <-uploadResult
	if requestErr != nil {
		return nil, requestErr
	}
	if uploadErr != nil {
		return nil, uploadErr
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service artifact action", status, responseBody)
	}
	if !json.Valid(responseBody) || len(responseBody) > 1<<20 {
		return nil, domain.CoreContractError{Operation: "service artifact action", Message: "invalid or oversized JSON"}
	}
	return json.RawMessage(responseBody), nil
}

type serviceAdminSurface struct {
	InstanceID string          `json:"instanceId"`
	SHA256     string          `json:"sha256"`
	Descriptor json.RawMessage `json:"descriptor"`
}

// ReadServiceAdminSurface returns the live service-owned descriptor only from
// Core Management API. The descriptor is bounded before it crosses the CLI
// boundary and remains opaque to the CLI beyond its JSON validity and digest.
func (c Client) ReadServiceAdminSurface(ctx context.Context, serviceID string) (domain.ServiceAdminSurface, error) {
	if !validManagementID(serviceID) {
		return domain.ServiceAdminSurface{}, errors.New("service id is invalid")
	}
	surface, err := c.serviceAdminSurface(ctx, serviceID)
	if err != nil {
		return domain.ServiceAdminSurface{}, err
	}
	if len(surface.Descriptor) == 0 || len(surface.Descriptor) > 1<<20 || !json.Valid(surface.Descriptor) {
		return domain.ServiceAdminSurface{}, domain.CoreContractError{Operation: "service admin surface", Message: "invalid or oversized descriptor"}
	}
	// Core returns the Service SDK's canonical digest, already prefixed with
	// "sha256:". Normalize at this boundary so the CLI JSON contract never
	// emits sha256:sha256:<hex> (which Studio correctly rejects).
	digest := strings.TrimPrefix(surface.SHA256, "sha256:")
	return domain.ServiceAdminSurface{Digest: "sha256:" + digest, Descriptor: append(json.RawMessage(nil), surface.Descriptor...)}, nil
}

func (c Client) serviceAdminSurface(ctx context.Context, serviceID string) (serviceAdminSurface, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/services/admin-surfaces"), nil)
	if err != nil {
		return serviceAdminSurface{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	body, status, err := c.do(request)
	if err != nil {
		return serviceAdminSurface{}, err
	}
	if status < 200 || status >= 300 {
		return serviceAdminSurface{}, domain.NewCoreHTTPError("service admin surface", status, body)
	}
	var list struct {
		Items []serviceAdminSurface `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil || len(list.Items) > 4096 {
		return serviceAdminSurface{}, domain.CoreContractError{Operation: "service admin surface", Message: "invalid surface list"}
	}
	var found *serviceAdminSurface
	for index := range list.Items {
		item := &list.Items[index]
		if item.InstanceID != serviceID {
			continue
		}
		if found != nil || !validSurfaceSHA256(item.SHA256) || len(item.Descriptor) == 0 || !json.Valid(item.Descriptor) {
			return serviceAdminSurface{}, domain.CoreContractError{Operation: "service admin surface", Message: "missing or ambiguous live surface"}
		}
		found = item
	}
	if found == nil {
		return serviceAdminSurface{}, domain.CoreContractError{Operation: "service admin surface", Message: "service surface is unavailable"}
	}
	return *found, nil
}

type artifactActionDescriptor struct {
	ID            string `json:"id"`
	ArtifactInput *struct {
		MediaTypes                []string `json:"mediaTypes"`
		MaxBytes                  int64    `json:"maxBytes"`
		MaxMetadataBytes          int64    `json:"maxMetadataBytes"`
		MaxMultipartOverheadBytes int64    `json:"maxMultipartOverheadBytes"`
	} `json:"artifactInput"`
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *limitedWriter) Write(buffer []byte) (int, error) {
	if int64(len(buffer)) > writer.remaining {
		return 0, errors.New("multipart request exceeds the declared service action limits")
	}
	written, err := writer.writer.Write(buffer)
	writer.remaining -= int64(written)
	return written, err
}

func findArtifactAction(raw json.RawMessage, pageID, actionID string) (artifactActionDescriptor, bool) {
	var surface struct {
		Pages []struct {
			ID       string `json:"id"`
			Sections []struct {
				Actions []artifactActionDescriptor `json:"actions"`
			} `json:"sections"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(raw, &surface); err != nil {
		return artifactActionDescriptor{}, false
	}
	var match *artifactActionDescriptor
	for _, page := range surface.Pages {
		if page.ID != pageID {
			continue
		}
		for _, section := range page.Sections {
			for index := range section.Actions {
				action := &section.Actions[index]
				if action.ID != actionID {
					continue
				}
				if match != nil || action.ArtifactInput == nil {
					return artifactActionDescriptor{}, false
				}
				match = action
			}
		}
	}
	if match == nil {
		return artifactActionDescriptor{}, false
	}
	return artifactActionDescriptor{ID: match.ID, ArtifactInput: match.ArtifactInput}, true
}

func writeServiceArtifactMultipart(writer *multipart.Writer, publication domain.ServiceArtifactPublication, contentType string, maximum int64) error {
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return err
	}
	if _, err := metadataPart.Write(publication.Metadata); err != nil {
		return err
	}
	artifactHeader := make(textproto.MIMEHeader)
	artifactHeader.Set("Content-Disposition", `form-data; name="artifact"`)
	artifactHeader.Set("Content-Type", contentType)
	artifactPart, err := writer.CreatePart(artifactHeader)
	if err != nil {
		return err
	}
	written, err := io.Copy(artifactPart, io.LimitReader(publication.Artifact, maximum+1))
	if err != nil {
		return err
	}
	if written > maximum {
		return errors.New("artifact exceeds the declared service action limit")
	}
	return writer.Close()
}

func validSurfaceSHA256(value string) bool {
	return validSHA256Hex(strings.TrimPrefix(value, "sha256:"))
}

func validManagementHeader(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validManagementID(value string) bool {
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

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (c Client) RegisterService(ctx context.Context, metadata domain.ServiceRegistrationMetadata) (json.RawMessage, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	key := "service-register-" + metadata.InstanceID + "-" + metadata.ReplicaID + "-" + strings.TrimPrefix(metadata.Digest, "sha256:")
	body, status, err := c.post(ctx, "/api/services/"+url.PathEscape(metadata.InstanceID)+"/replicas/register", metadata, key)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service register", status, body)
	}
	if !json.Valid(body) {
		return nil, domain.CoreContractError{Operation: "service register", Message: "invalid JSON"}
	}
	return json.RawMessage(body), nil
}

// EnsureServiceLink reconciles one canonical project link through the Core
// Management API. The CLI never contacts either service for this operation.
func (c Client) EnsureServiceLink(ctx context.Context, policy domain.ServiceLinkPolicyRequest, idempotencyKey string) (json.RawMessage, error) {
	if !policy.Valid() {
		return nil, errors.New("invalid service link policy")
	}
	path := "/api/service-links/" + url.PathEscape(policy.CallerInstanceID) + "/" + url.PathEscape(policy.TargetInstanceID)
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
	if status == http.StatusNotFound {
		body, status, err = c.post(ctx, "/api/service-links", policy, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, domain.NewCoreHTTPError("service link create", status, body)
		}
		return json.RawMessage(body), nil
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service link inspect", status, body)
	}
	var current struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(body, &current); err != nil || current.Revision < 1 {
		return nil, domain.CoreContractError{Operation: "service link inspect", Message: "missing revision"}
	}
	replacement := struct {
		Rules []domain.ServiceLinkRule `json:"rules"`
	}{Rules: policy.Rules}
	encoded, err := json.Marshal(replacement)
	if err != nil {
		return nil, err
	}
	putRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url(path), bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	putRequest.Header.Set("Accept", "application/json")
	putRequest.Header.Set("Content-Type", "application/json")
	putRequest.Header.Set("Authorization", "Bearer "+c.Token)
	putRequest.Header.Set("Idempotency-Key", idempotencyKey)
	putRequest.Header.Set("If-Match", strconv.Quote(strconv.FormatInt(current.Revision, 10)))
	body, status, err = c.do(putRequest)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("service link replace", status, body)
	}
	return json.RawMessage(body), nil
}

func (c Client) post(ctx context.Context, path string, value any, idempotencyKey string) ([]byte, int, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, 0, errors.New("core token is required")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return c.do(request)
}

func (c Client) do(request *http.Request) ([]byte, int, error) {
	body, status, _, err := c.doWithHeaders(request)
	return body, status, err
}

func (c Client) doWithHeaders(request *http.Request) ([]byte, int, http.Header, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
	if err != nil {
		return nil, response.StatusCode, response.Header.Clone(), err
	}
	return body, response.StatusCode, response.Header.Clone(), nil
}

func (c Client) url(path string) string {
	return strings.TrimRight(c.Endpoint, "/") + path
}

func New(connection domain.CoreConnection) (Client, error) {
	endpoint := strings.TrimSpace(connection.Endpoint)
	parsed, err := url.Parse(endpoint)
	if endpoint == "" {
		return Client{}, errors.New("core endpoint is required")
	}
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return Client{}, errors.New("core endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme == "http" && (connection.CAFile != "" || connection.ClientCertFile != "" || connection.ClientKeyFile != "" || connection.ServerName != "") {
		return Client{}, errors.New("TLS settings require an HTTPS core endpoint")
	}
	if parsed.Scheme == "https" && (connection.ClientCertFile == "" || connection.ClientKeyFile == "") {
		return Client{}, errors.New("HTTPS core endpoint requires client certificate and key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if parsed.Scheme == "https" {
		tlsConfig, err := tlsConfig(connection)
		if err != nil {
			return Client{}, err
		}
		transport.TLSClientConfig = tlsConfig
	}
	return Client{
		Endpoint:   endpoint,
		Token:      connection.Token,
		HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
}

func tlsConfig(connection domain.CoreConnection) (*tls.Config, error) {
	configuration := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: connection.ServerName} //nolint:gosec // TLS 1.2 is the minimum supported Core profile.
	if connection.CAFile != "" {
		body, err := os.ReadFile(connection.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read core CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(body) {
			return nil, errors.New("core CA file contains no certificates")
		}
		configuration.RootCAs = roots
	}
	if (connection.ClientCertFile == "") != (connection.ClientKeyFile == "") {
		return nil, errors.New("core client certificate and key must be provided together")
	}
	if connection.ClientCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(connection.ClientCertFile, connection.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load core client certificate: %w", err)
		}
		configuration.Certificates = []tls.Certificate{certificate}
	}
	return configuration, nil
}
