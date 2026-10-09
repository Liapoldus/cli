package coreapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestClientHealthDoesNotRequireToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(domain.CoreConnection{Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Health(context.Background())
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestClientApplySendsContractHeadersAndEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Idempotency-Key") != "apply-1" {
			t.Fatalf("headers=%v", request.Header)
		}
		var value domain.ConfigBundleRequest
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if value.Project.Revision != "abc" || value.Target.Environment != "production" {
			t.Fatalf("request=%+v", value)
		}
		response.WriteHeader(http.StatusAccepted)
		_, _ = response.Write([]byte(`{"operations":[],"requestId":"req-1"}`))
	}))
	defer server.Close()

	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Apply(context.Background(), domain.ConfigBundleRequest{
		Project: domain.ProjectRef{ID: "demo", Revision: "abc"},
		Bundle:  domain.BundleRef{SchemaVersion: "core-config/v2", Digest: "sha256:" + strings.Repeat("1", 64)},
		Target:  domain.TargetRef{Environment: "production"},
	}, "apply-1")
	if err != nil || len(result.OperationIDs) != 0 {
		t.Fatalf("response=%+v err=%v", result, err)
	}
}

func TestClientOperationRejectsIncompleteEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"operationId":"op-1","state":"succeeded"}`))
	}))
	defer server.Close()

	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Operation(context.Background(), "op-1"); err == nil || !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("error=%v", err)
	}
}

func TestNewRejectsNonHTTPSEndpoint(t *testing.T) {
	if _, err := New(domain.CoreConnection{Endpoint: "not-a-url", Token: "secret"}); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}

func TestNewRejectsTLSSettingsOnHTTP(t *testing.T) {
	if _, err := New(domain.CoreConnection{Endpoint: "http://127.0.0.1:8080", CAFile: "ca.pem"}); err == nil {
		t.Fatal("expected TLS/HTTP configuration error")
	}
}

func TestNewRejectsIncompleteClientCertificate(t *testing.T) {
	if _, err := New(domain.CoreConnection{Endpoint: "https://core.example", ClientCertFile: "client.crt"}); err == nil {
		t.Fatal("expected client certificate pair error")
	}
}
