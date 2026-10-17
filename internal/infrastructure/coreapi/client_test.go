package coreapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/domain"
)

func TestServiceMetadataNeverFallsBackToRetiredRoutes(t *testing.T) {
	for _, view := range []string{"manifest", "schema", "status"} {
		t.Run(view, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/api/services/echo/"+view {
					t.Errorf("unexpected route: %s", request.URL.Path)
				}
				response.WriteHeader(http.StatusNotFound)
				_, _ = response.Write([]byte(`{"code":"service_not_found","detail":"missing service"}`))
			}))
			defer server.Close()
			client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ServiceMetadata(context.Background(), "echo", view)
			var failure domain.CoreHTTPError
			if calls != 1 || !errors.As(err, &failure) || failure.Code != "service_not_found" || failure.Operation != "service "+view {
				t.Fatalf("unexpected fallback/error: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestResolveOperationsUsesOnlyCoreAndReturnsSafeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/operations/resolve" ||
			request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected Core request: %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		var body struct {
			Scope string   `json:"scope"`
			Keys  []string `json:"keys"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Scope != "config-bundle/revision" || len(body.Keys) != 1 || body.Keys[0] != "apply-key:server" {
			t.Fatalf("unexpected recovery request: %+v error=%v", body, err)
		}
		_, _ = io.WriteString(response, `{"items":[{"id":"op-1","state":"running","resourceId":"server"}],"requestId":"req-1"}`)
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	operations, err := client.ResolveOperations(context.Background(), "config-bundle/revision", []string{"apply-key:server"})
	if err != nil || len(operations) != 1 || operations[0] != (domain.OperationReference{ID: "op-1", State: "running", ResourceID: "server"}) {
		t.Fatalf("operations=%+v error=%v", operations, err)
	}
}

func TestResolveOperationsRejectsUnboundedOrAmbiguousKeysBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{nil, {"duplicate", "duplicate"}, {strings.Repeat("x", 513)}} {
		if _, err := client.ResolveOperations(context.Background(), "scope", keys); err == nil {
			t.Fatalf("invalid key set accepted: %#v", keys)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached Core: %d", calls)
	}
}

func TestUpdateCoreSettingsUsesRevisionETagAndValidatesSnapshot(t *testing.T) {
	settings := `{"management":{"listen":"127.0.0.1:8081","tls":{"certificate":"/liapoldus/tls/management.crt","key":"/liapoldus/tls/management.key"},"maxBodyBytes":1048576,"headerTimeout":"10s","requestTimeout":"30s"},"serviceControl":{"listen":"127.0.0.1:9443","publicURL":"https://core:9443","tls":{"certificate":"/liapoldus/tls/control.crt","key":"/liapoldus/tls/control.key"},"replicaClientCA":"/liapoldus/tls/replica-client-ca.pem","replicaServerCA":"/liapoldus/tls/replica-server-ca.pem"},"secretRoot":"/var/lib/liapoldus/secrets"}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/api/v2/settings" ||
			request.Header.Get("Authorization") != "Bearer secret" ||
			request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("If-Match") != `"core-settings-4"` {
			t.Fatalf("unexpected settings mutation request: %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || string(body) != settings {
			t.Fatalf("settings body changed: %s error=%v", body, err)
		}
		response.Header().Set("ETag", `"core-settings-5"`)
		_, _ = io.WriteString(response, `{"desiredRevision":5,"effectiveRevision":4,"pending":true,"restartRequired":true,"settings":`+settings+`}`)
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.UpdateCoreSettings(context.Background(), `"core-settings-4"`, json.RawMessage(settings))
	if err != nil || snapshot.DesiredRevision != 5 || snapshot.EffectiveRevision != 4 || !snapshot.Pending || !snapshot.RestartRequired || snapshot.ETag != `"core-settings-5"` {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
}

func TestUpdateCoreSettingsRejectsInvalidDocumentBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		etag string
		body json.RawMessage
	}{{`W/"core-settings-1"`, json.RawMessage(`{}`)}, {`"core-settings-1"`, json.RawMessage(`[]`)}, {`"core-settings-1"`, json.RawMessage(strings.Repeat("x", 262145))}} {
		if _, err := client.UpdateCoreSettings(context.Background(), test.etag, test.body); err == nil {
			t.Fatalf("invalid update accepted: etag=%q body-size=%d", test.etag, len(test.body))
		}
	}
	if calls != 0 {
		t.Fatalf("invalid update reached Core: %d requests", calls)
	}
}

func TestServiceAdminQueryUsesOnlyCoreManagementAPIAndLiveSurfaceDigest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing Core authorization")
		}
		if calls == 1 {
			if request.Method != http.MethodGet || request.URL.Path != "/api/services/admin-surfaces" {
				t.Fatalf("unexpected surface request: %s %s", request.Method, request.URL.Path)
			}
			_, _ = response.Write([]byte(`{"items":[{"instanceId":"server","descriptor":{"version":1},"sha256":"` + strings.Repeat("a", 64) + `"}]}`))
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/services/server/admin/pages/sites/query" {
			t.Fatalf("unexpected admin query: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Liapoldus-Admin-Surface-Digest") != strings.Repeat("a", 64) || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("missing safe surface binding headers: %v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["resource"] != "sites" || body["limit"] != float64(50) {
			t.Fatalf("unexpected resource query body: %v %v", body, err)
		}
		_, _ = response.Write([]byte(`{"items":[{"siteId":"docs"}]}`))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.ServiceAdminQuery(context.Background(), "server", "sites", "sites")
	if err != nil || calls != 2 || string(body) != `{"items":[{"siteId":"docs"}]}` {
		t.Fatalf("calls=%d body=%s error=%v", calls, body, err)
	}
}

func TestServiceAdminQueryRejectsMissingOrAmbiguousCoreSurface(t *testing.T) {
	for name, items := range map[string]string{
		"missing":   `[]`,
		"ambiguous": `[{"instanceId":"server","sha256":"` + strings.Repeat("a", 64) + `"},{"instanceId":"server","sha256":"` + strings.Repeat("b", 64) + `"}]`,
		"no digest": `[{"instanceId":"server"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls++
				_, _ = response.Write([]byte(`{"items":` + items + `}`))
			}))
			defer server.Close()
			client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ServiceAdminQuery(context.Background(), "server", "sites", "sites"); err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v; want fail before dispatch", calls, err)
			}
		})
	}
}

func TestReadServiceAdminSurfaceReturnsOnlyCoreBackedDescriptorAndDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	descriptor := `{"version":1,"pages":[{"id":"sites","actions":[{"id":"publish"}]}]}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodGet || request.URL.Path != "/api/services/admin-surfaces" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected Core request: %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(response, `{"items":[{"instanceId":"server","sha256":"`+digest+`","descriptor":`+descriptor+`}]}`)
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ReadServiceAdminSurface(context.Background(), "server")
	if err != nil || calls != 1 || result.Digest != digest || string(result.Descriptor) != descriptor {
		t.Fatalf("calls=%d result=%+v error=%v", calls, result, err)
	}
}

func TestReadServiceAdminSurfaceRejectsUnboundedOrInvalidDescriptors(t *testing.T) {
	for name, descriptor := range map[string]string{
		"invalid JSON": `{not-json}`,
		"oversized":    `"` + strings.Repeat("x", (1<<20)+1) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(response, `{"items":[{"instanceId":"server","sha256":"`+strings.Repeat("a", 64)+`","descriptor":`+descriptor+`}]}`)
			}))
			defer server.Close()
			client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ReadServiceAdminSurface(context.Background(), "server"); err == nil {
				t.Fatal("invalid or oversized descriptor accepted")
			}
		})
	}
}

func TestPublishServiceArtifactUsesLiveCoreSurfaceAndStreamsOnlyDeclaredArtifactAction(t *testing.T) {
	digest := strings.Repeat("a", 64)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing Core authorization")
		}
		if calls == 1 {
			if request.Method != http.MethodGet || request.URL.Path != "/api/services/admin-surfaces" {
				t.Fatalf("unexpected surface request: %s %s", request.Method, request.URL.Path)
			}
			_, _ = io.WriteString(response, `{"items":[{"instanceId":"server","sha256":"`+digest+`","descriptor":{"pages":[{"id":"sites","sections":[{"actions":[{"id":"publish","artifactInput":{"mediaTypes":["application/gzip"],"maxBytes":1024,"maxMetadataBytes":1024,"maxMultipartOverheadBytes":4096}}]}]}]}}]}`)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/services/server/admin/pages/sites/actions/publish" {
			t.Fatalf("unexpected Core action: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Liapoldus-Admin-Surface-Digest") != digest || request.Header.Get("Idempotency-Key") != "publish-1" || request.Header.Get("If-Match") != `"revision-1"` {
			t.Fatalf("missing live surface/CAS/idempotency binding: %v", request.Header)
		}
		mediaType := request.Header.Get("Content-Type")
		if !strings.HasPrefix(mediaType, "multipart/form-data; boundary=") {
			t.Fatalf("unexpected content type: %s", mediaType)
		}
		reader, err := request.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		metadataPart, err := reader.NextPart()
		if err != nil || metadataPart.FormName() != "metadata" {
			t.Fatalf("first multipart part must be metadata: %v", err)
		}
		metadata, err := io.ReadAll(metadataPart)
		if err != nil || string(metadata) != `{"siteId":"docs"}` {
			t.Fatalf("metadata part=%s error=%v", metadata, err)
		}
		artifactPart, err := reader.NextPart()
		if err != nil || artifactPart.FormName() != "artifact" || artifactPart.Header.Get("Content-Type") != "application/gzip" {
			t.Fatalf("invalid artifact part: part=%v error=%v", artifactPart, err)
		}
		artifact, err := io.ReadAll(artifactPart)
		if err != nil || string(artifact) != "compressed-site" {
			t.Fatalf("artifact=%q error=%v", artifact, err)
		}
		if extra, err := reader.NextPart(); err != io.EOF || extra != nil {
			t.Fatalf("unexpected multipart part: %v %v", extra, err)
		}
		response.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(response, `{"operationId":"publish-operation","requestId":"request"}`)
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PublishServiceArtifact(context.Background(), domain.ServiceArtifactPublication{
		ServiceID: "server", PageID: "sites", ActionID: "publish", Metadata: json.RawMessage(`{"siteId":"docs"}`),
		ContentType: "application/gzip", Artifact: strings.NewReader("compressed-site"), IdempotencyKey: "publish-1", IfMatch: `"revision-1"`,
	})
	if err != nil || calls != 2 || string(result) != `{"operationId":"publish-operation","requestId":"request"}` {
		t.Fatalf("calls=%d result=%s error=%v", calls, result, err)
	}
}

func TestPublishServiceArtifactRejectsUndeclaredActionsAndContentTypesBeforeUpload(t *testing.T) {
	for name, publication := range map[string]domain.ServiceArtifactPublication{
		"missing action":     {ServiceID: "server", PageID: "sites", ActionID: "missing", Metadata: json.RawMessage(`{}`), ContentType: "application/gzip", Artifact: strings.NewReader("x"), IdempotencyKey: "publish-1", IfMatch: `"revision-1"`},
		"wrong content type": {ServiceID: "server", PageID: "sites", ActionID: "publish", Metadata: json.RawMessage(`{}`), ContentType: "application/json", Artifact: strings.NewReader("x"), IdempotencyKey: "publish-1", IfMatch: `"revision-1"`},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls++
				_, _ = io.WriteString(response, `{"items":[{"instanceId":"server","sha256":"`+strings.Repeat("b", 64)+`","descriptor":{"pages":[{"id":"sites","sections":[{"actions":[{"id":"publish","artifactInput":{"mediaTypes":["application/gzip"],"maxBytes":1024,"maxMetadataBytes":1024,"maxMultipartOverheadBytes":4096}}]}]}]}}]}`)
			}))
			defer server.Close()
			client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.PublishServiceArtifact(context.Background(), publication); err == nil || calls != 1 {
				t.Fatalf("calls=%d error=%v; must reject before artifact upload", calls, err)
			}
		})
	}
}

func TestEnsureServiceLinkUsesCanonicalCoreResourcesAndCAS(t *testing.T) {
	policy := domain.ServiceLinkPolicyRequest{CallerInstanceID: "runtime", TargetInstanceID: "domain", Rules: []domain.ServiceLinkRule{{PlacementRule: "any", Carrier: "tcp", Weight: 1, RequiredContracts: []domain.ServiceLinkContractRange{{ContractID: "domain.objects", MinimumVersion: "1.0.0", MaximumVersionExclusive: "2.0.0"}}}}}
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "replace"}[existing], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls++
				if request.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing Core credentials")
				}
				resource := "/api/service-links/runtime/domain"
				if calls == 1 {
					if request.Method != http.MethodGet || request.URL.Path != resource {
						t.Errorf("unexpected inspect: %s %s", request.Method, request.URL.Path)
					}
					if existing {
						_, _ = response.Write([]byte(`{"revision":7}`))
					} else {
						response.WriteHeader(http.StatusNotFound)
					}
					return
				}
				if calls != 2 || request.Header.Get("Idempotency-Key") != "link-operation" {
					t.Error("unexpected mutation or missing idempotency key")
				}
				if existing {
					if request.Method != http.MethodPut || request.URL.Path != resource || request.Header.Get("If-Match") != `"7"` {
						t.Errorf("unexpected replace: %s %s %v", request.Method, request.URL.Path, request.Header)
					}
				} else if request.Method != http.MethodPost || request.URL.Path != "/api/service-links" {
					t.Errorf("unexpected create: %s %s", request.Method, request.URL.Path)
				}
				var payload domain.ServiceLinkPolicyRequest
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Rules) != 1 || len(payload.Rules[0].RequiredContracts) != 1 || payload.Rules[0].RequiredContracts[0].ContractID != "domain.objects" {
					t.Errorf("contract IDs changed: %+v, %v", payload, err)
				}
				_, _ = response.Write([]byte(`{"revision":8}`))
			}))
			defer server.Close()
			client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := client.EnsureServiceLink(context.Background(), policy, "link-operation")
			if err != nil || calls != 2 || string(body) != `{"revision":8}` {
				t.Fatalf("calls=%d body=%s error=%v", calls, body, err)
			}
		})
	}
}

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

func TestClientApplyRejectsUnknownFieldsInsteadOfForwardingThem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"operations":["op-1"],"requestId":"req-1","accessToken":"never-forward"}`))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Apply(context.Background(), domain.ConfigBundleRequest{}, "apply-1"); err == nil || !strings.Contains(err.Error(), "invalid response contract") || strings.Contains(err.Error(), "never-forward") {
		t.Fatalf("unexpected Apply validation result: %v", err)
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
	if _, err := client.Operation(context.Background(), "op-1"); err == nil || !strings.Contains(err.Error(), "invalid response contract") {
		t.Fatalf("error=%v", err)
	}
}

func TestClientOperationAcceptsCoreIDEnvelopeAndBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		_, _ = response.Write([]byte(`{"id":"op-1","kind":"service-settings-apply","state":"succeeded","createdAt":"2026-10-16T10:00:00Z","requestId":"req-1","replicaOutcomes":[{"replicaId":"server-1","desiredGeneration":2,"state":"applied"}]}`))
	}))
	defer server.Close()

	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Operation(context.Background(), "op-1")
	if err != nil || result.ID != "op-1" || result.State != "succeeded" || len(result.ReplicaOutcomes) != 1 || result.ReplicaOutcomes[0].ReplicaID != "server-1" || result.ReplicaOutcomes[0].DesiredGeneration != 2 || result.ReplicaOutcomes[0].State != "applied" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClientOperationRejectsUnknownSensitiveFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"id":"op-1","kind":"service-settings-apply","state":"succeeded","createdAt":"2026-10-16T10:00:00Z","requestId":"req-1","accessToken":"do-not-forward"}`))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Operation(context.Background(), "op-1"); err == nil || !strings.Contains(err.Error(), "invalid response contract") || strings.Contains(err.Error(), "do-not-forward") {
		t.Fatalf("unexpected response validation: %v", err)
	}
}

func TestClientOperationRejectsMismatchedReplicaAndDuplicateOutcomes(t *testing.T) {
	for _, body := range []string{
		`{"id":"op-2","kind":"service-settings-apply","state":"succeeded","createdAt":"2026-10-16T10:00:00Z","requestId":"req-1","replicaOutcomes":[{"replicaId":"server-2","desiredGeneration":2,"state":"applied"}]}`,
		`{"id":"op-1","kind":"service-settings-apply","state":"degraded","createdAt":"2026-10-16T10:00:00Z","requestId":"req-1","replicaOutcomes":[{"replicaId":"server-1","desiredGeneration":2,"state":"failed","failureCode":"replica_unavailable"},{"replicaId":"server-1","desiredGeneration":2,"state":"failed","failureCode":"replica_unavailable"}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) { _, _ = response.Write([]byte(body)) }))
		client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Operation(context.Background(), "op-1"); err == nil {
			t.Fatalf("accepted invalid operation response: %s", body)
		}
		server.Close()
	}
}

func TestClientServiceMetadataIsCoreBacked(t *testing.T) {
	manifest := `{"id":"server","configurationUI":{"version":"service-configuration-ui/v1","groups":[{"id":"listener"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services/server/manifest" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request=%s auth=%s", request.URL.Path, request.Header.Get("Authorization"))
		}
		_, _ = response.Write([]byte(manifest))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.ServiceMetadata(context.Background(), "server", "manifest")
	if err != nil || string(body) != manifest {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestClientServiceStatusPreservesCoreArtifactProvenance(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	status := `{"instanceId":"server","replicaId":"replica-1","artifactId":"server","artifactVersion":"1.2.3","artifactDigest":"` + digest + `","identity":{"instanceId":"server","replicaId":"replica-1"},"readiness":{"ready":true},"replicas":[{"replicaId":"replica-1","artifactVersion":"1.2.3","artifactDigest":"` + digest + `"},{"replicaId":"replica-2","artifactVersion":"1.2.3","artifactDigest":"` + digest + `"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services/server/status" {
			t.Fatalf("request path=%s", request.URL.Path)
		}
		_, _ = response.Write([]byte(status))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.ServiceMetadata(context.Background(), "server", "status")
	if err != nil || string(body) != status || !strings.Contains(string(body), `"replicaId":"replica-2"`) {
		t.Fatalf("Core status provenance was changed: body=%s err=%v", body, err)
	}
}

func TestClientRegisterServiceSendsSafeMetadataToCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services/server/replicas/register" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Idempotency-Key") == "" {
			t.Fatalf("request=%s auth=%s key=%s", request.URL.Path, request.Header.Get("Authorization"), request.Header.Get("Idempotency-Key"))
		}
		var metadata domain.ServiceRegistrationMetadata
		if err := json.NewDecoder(request.Body).Decode(&metadata); err != nil || metadata.InstanceID != "server" || metadata.ControlEndpoint != "https://server:8443" {
			t.Fatalf("metadata=%+v err=%v", metadata, err)
		}
		_, _ = response.Write([]byte(`{"accepted":true,"status":"pending"}`))
	}))
	defer server.Close()
	client, err := New(domain.CoreConnection{Endpoint: server.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.RegisterService(context.Background(), domain.ServiceRegistrationMetadata{ArtifactID: "server", ArtifactVersion: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64), InstanceID: "server", ReplicaID: "replica-1", ControlEndpoint: "https://server:8443", RuntimeStatus: "started"})
	if err != nil || !json.Valid(body) {
		t.Fatalf("body=%s err=%v", body, err)
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
