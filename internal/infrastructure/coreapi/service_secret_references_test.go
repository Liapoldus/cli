package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestServiceSecretReferenceOperationsUseMetadataOnlyManagementRoutes(t *testing.T) {
	const reference = "ssr:v1:00112233445566778899aabbccddeeff"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer core-token" {
			t.Errorf("Core authorization missing")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/services/server/secrets":
			body, err := io.ReadAll(request.Body)
			if err != nil || strings.Contains(string(body), "secret-value") {
				t.Fatalf("secret value crossed the Core API: body=%s error=%v", body, err)
			}
			var input map[string]json.RawMessage
			if err := json.Unmarshal(body, &input); err != nil || len(input) != 3 || string(input["reference"]) != `"`+reference+`"` || string(input["purpose"]) != `"http.auth.bearer"` || string(input["label"]) != `"edge token"` {
				t.Fatalf("unexpected metadata request: %s error=%v", body, err)
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"reference":"`+reference+`","serviceId":"server","purpose":"http.auth.bearer","label":"edge token","state":"active","createdAt":"2026-10-17T00:00:00Z"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/api/services/server/secrets" && request.URL.Query().Get("purpose") == "http.auth.bearer":
			_, _ = io.WriteString(response, `{"items":[{"reference":"`+reference+`","serviceId":"server","purpose":"http.auth.bearer","label":"edge token","state":"active","createdAt":"2026-10-17T00:00:00Z"}]}`)
		case request.Method == http.MethodDelete && request.URL.Path == "/api/services/server/secrets/"+reference:
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected secret metadata route: %s %s", request.Method, request.URL.String())
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "core-token"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := client.CreateServiceSecretReference(context.Background(), "server", domain.ServiceSecretReference{Reference: reference, Purpose: "http.auth.bearer", Label: "edge token", State: "active"})
	if err != nil || created.Reference != reference || created.ServiceID != "server" || created.State != "active" {
		t.Fatalf("created=%+v error=%v", created, err)
	}
	items, err := client.ListServiceSecretReferences(context.Background(), "server", "http.auth.bearer")
	if err != nil || len(items) != 1 || items[0] != created {
		t.Fatalf("items=%+v error=%v", items, err)
	}
	if err := client.RevokeServiceSecretReference(context.Background(), "server", reference); err != nil {
		t.Fatalf("revoke: %v", err)
	}
}
