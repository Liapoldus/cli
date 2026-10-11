package coreapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.RequestID == "" {
		return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "missing required fields"}
	}
	for _, operationID := range envelope.Operations {
		if strings.TrimSpace(operationID) == "" {
			return domain.ApplyResponse{}, domain.CoreContractError{Operation: "apply", Message: "empty operation ID"}
		}
	}
	return domain.ApplyResponse{OperationIDs: envelope.Operations, Raw: json.RawMessage(body)}, nil
}

func (c Client) Operation(ctx context.Context, operationID string) (domain.OperationResponse, error) {
	if strings.TrimSpace(operationID) == "" {
		return domain.OperationResponse{}, errors.New("operation ID is required")
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
		OperationID string `json:"operationId"`
		ID          string `json:"id"`
		State       string `json:"state"`
		RequestID   string `json:"requestId"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "missing required fields"}
	}
	if strings.TrimSpace(envelope.OperationID) == "" {
		envelope.OperationID = envelope.ID
	}
	if strings.TrimSpace(envelope.OperationID) == "" || strings.TrimSpace(envelope.State) == "" || strings.TrimSpace(envelope.RequestID) == "" {
		return domain.OperationResponse{}, domain.CoreContractError{Operation: "operation", Message: "missing required fields"}
	}
	return domain.OperationResponse{State: envelope.State, Raw: json.RawMessage(body)}, nil
}

// PluginMetadata is a Core-backed read. The CLI deliberately knows only the
// Management API resource; it has no Plugin SDK REST client or plugin URL
// routing logic.
func (c Client) PluginMetadata(ctx context.Context, pluginID, view string) (json.RawMessage, error) {
	if strings.TrimSpace(pluginID) == "" || (view != "manifest" && view != "schema" && view != "status") {
		return nil, errors.New("plugin id and supported metadata view are required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/plugins/"+url.PathEscape(pluginID)+"/"+view), nil)
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
		return nil, domain.NewCoreHTTPError("plugin "+view, status, body)
	}
	if !json.Valid(body) {
		return nil, domain.CoreContractError{Operation: "plugin " + view, Message: "invalid JSON"}
	}
	return json.RawMessage(body), nil
}

func (c Client) RegisterPlugin(ctx context.Context, metadata domain.PluginRegistrationMetadata) (json.RawMessage, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("core token is required")
	}
	key := "plugin-register-" + metadata.InstanceID + "-" + metadata.ReplicaID + "-" + strings.TrimPrefix(metadata.Digest, "sha256:")
	body, status, err := c.post(ctx, "/api/plugins/"+url.PathEscape(metadata.InstanceID)+"/replicas/register", metadata, key)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("plugin register", status, body)
	}
	if !json.Valid(body) {
		return nil, domain.CoreContractError{Operation: "plugin register", Message: "invalid JSON"}
	}
	return json.RawMessage(body), nil
}

// EnsurePluginLink reconciles one canonical project link through the Core
// Management API. The CLI never contacts either plugin for this operation.
func (c Client) EnsurePluginLink(ctx context.Context, policy domain.PluginLinkPolicyRequest, idempotencyKey string) (json.RawMessage, error) {
	if !policy.Valid() {
		return nil, errors.New("invalid plugin link policy")
	}
	path := "/api/plugin-links/" + url.PathEscape(policy.CallerInstanceID) + "/" + url.PathEscape(policy.TargetInstanceID)
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
		body, status, err = c.post(ctx, "/api/plugin-links", policy, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, domain.NewCoreHTTPError("plugin link create", status, body)
		}
		return json.RawMessage(body), nil
	}
	if status < 200 || status >= 300 {
		return nil, domain.NewCoreHTTPError("plugin link inspect", status, body)
	}
	var current struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(body, &current); err != nil || current.Revision < 1 {
		return nil, domain.CoreContractError{Operation: "plugin link inspect", Message: "missing revision"}
	}
	replacement := struct {
		Rules []domain.PluginLinkRule `json:"rules"`
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
		return nil, domain.NewCoreHTTPError("plugin link replace", status, body)
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
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 10<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
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
