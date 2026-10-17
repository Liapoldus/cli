package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
	"github.com/Liapoldus/cli/internal/infrastructure/coreapi"
)

func TestCoreSettingsInspectReturnsTypedRedactedView(t *testing.T) {
	const privatePath = "/liapoldus/tls/management.crt"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v2/settings" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer management-token" {
			t.Errorf("authorization header was not sourced from target credential store")
		}
		response.Header().Set("ETag", `"core-settings-8"`)
		_, _ = response.Write([]byte(`{"desiredRevision":8,"effectiveRevision":6,"pending":true,"restartRequired":true,"settings":{"management":{"listen":"127.0.0.1:18180","tls":{"certificate":"` + privatePath + `","key":"/liapoldus/private/tls/management.key","clientCA":""},"maxBodyBytes":1048576,"headerTimeout":"5s","requestTimeout":"10s"},"serviceControl":{"listen":"127.0.0.1:18181","publicURL":"https://core.local:18181","tls":{"certificate":"/liapoldus/private/tls/control.crt","key":"/liapoldus/private/tls/control.key"},"replicaClientCA":"/liapoldus/private/tls/replica-client-ca.pem","replicaServerCA":"/liapoldus/private/tls/replica-server-ca.pem"},"secretRoot":"/liapoldus/private/secrets"}}`))
	}))
	defer server.Close()

	certificate := filepath.Join(t.TempDir(), "management.crt")
	if err := os.WriteFile(certificate, []byte("fixture certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Target{Name: "local", Endpoint: server.URL, CredentialRef: "target-credential"}
	target.Bootstrap.ManagementCertificate = certificate
	runner := Runner{
		Registry:    targetRegistryStub{target: target},
		Credentials: credentialProviderStub{value: "management-token"},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "settings", "inspect", "--target", "local", studioEventsFlag}, &output, &output); code != application.ExitOK {
		t.Fatalf("inspect code=%d output=%s", code, output.String())
	}
	if strings.Contains(output.String(), privatePath) || strings.Contains(output.String(), "management.key") || strings.Contains(output.String(), "/liapoldus/private") {
		t.Fatalf("renderer output leaked Core filesystem paths: %s", output.String())
	}
	var events struct {
		Type    string `json:"type"`
		Payload struct {
			SchemaVersion     string `json:"schemaVersion"`
			Target            string `json:"target"`
			DesiredRevision   int64  `json:"desiredRevision"`
			EffectiveRevision int64  `json:"effectiveRevision"`
			Pending           bool   `json:"pending"`
			RestartRequired   bool   `json:"restartRequired"`
			Settings          struct {
				Management struct {
					Listen string `json:"listen"`
					TLS    struct {
						Certificate struct {
							ReferenceID string `json:"referenceId"`
							Configured  bool   `json:"configured"`
							Available   bool   `json:"available"`
						} `json:"certificate"`
					} `json:"tls"`
				} `json:"management"`
			} `json:"settings"`
		} `json:"payload"`
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid JSONL event %q: %v", line, err)
		}
		if event.Type != "run.output" {
			continue
		}
		events.Type = event.Type
		if err := json.Unmarshal(event.Payload, &events.Payload); err != nil {
			t.Fatalf("decode safe settings view: %v", err)
		}
	}
	if events.Type != "run.output" || events.Payload.SchemaVersion != "core-settings-view/v1" || events.Payload.Target != "local" || events.Payload.DesiredRevision != 8 || events.Payload.EffectiveRevision != 6 || !events.Payload.Pending || !events.Payload.RestartRequired || events.Payload.Settings.Management.Listen != "127.0.0.1:18180" || !events.Payload.Settings.Management.TLS.Certificate.Configured || !events.Payload.Settings.Management.TLS.Certificate.Available || events.Payload.Settings.Management.TLS.Certificate.ReferenceID != "target-file/v1/local/tls/management.crt" {
		t.Fatalf("missing typed Core settings view: %+v output=%s", events, output.String())
	}
}

func TestCoreSettingsInspectFailsClosedForUnsupportedTarget(t *testing.T) {
	runner := Runner{Registry: targetRegistryStub{target: domain.Target{Name: "local"}}}
	var output bytes.Buffer
	if code := runner.Run([]string{"core", "settings", "inspect", "--target", "remote"}, &output, &output); code == application.ExitOK || !strings.Contains(output.String(), "require --target local") {
		t.Fatalf("unsupported target accepted or unclear: code=%d output=%s", code, output.String())
	}
}

func TestCoreSettingsProjectionRedactsUnmappedPathsAndRejectsUnknownFields(t *testing.T) {
	const privatePath = "/operator/private/custom-ca.pem"
	settings := json.RawMessage(`{"management":{"listen":"127.0.0.1:18180","tls":{"certificate":"` + privatePath + `","key":"` + privatePath + `"},"maxBodyBytes":1048576,"headerTimeout":"5s","requestTimeout":"10s"},"serviceControl":{"listen":"127.0.0.1:18181","publicURL":"https://core.local:18181","tls":{"certificate":"/liapoldus/tls/control.crt","key":"/liapoldus/tls/control.key"},"replicaClientCA":"/liapoldus/tls/replica-client-ca.pem","replicaServerCA":"/liapoldus/tls/replica-server-ca.pem"},"secretRoot":"/var/lib/liapoldus/secrets"}`)
	view, err := projectCoreSettings(domain.Target{Name: "local"}, domain.CoreSettingsSnapshot{
		DesiredRevision: 1, EffectiveRevision: 1, Settings: settings,
	})
	if err != nil {
		t.Fatalf("projectCoreSettings() error: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privatePath) || !strings.Contains(string(encoded), `"state":"unmapped"`) {
		t.Fatalf("unmapped path was not safely projected: %s", encoded)
	}
	settings = json.RawMessage(strings.TrimSuffix(string(settings), "}") + `,"unexpectedPath":"/private/must-not-pass"}`)
	if _, err := projectCoreSettings(domain.Target{Name: "local"}, domain.CoreSettingsSnapshot{DesiredRevision: 1, EffectiveRevision: 1, Settings: settings}); err == nil {
		t.Fatal("unsupported Core settings fields were accepted")
	}
}

func TestCoreSettingsPatchMergesTypedFieldsAndPreservesPrivateReferences(t *testing.T) {
	const privatePath = "/liapoldus/tls/management.key"
	current := json.RawMessage(`{"management":{"listen":"127.0.0.1:18180","tls":{"certificate":"/liapoldus/tls/management.crt","key":"` + privatePath + `"},"maxBodyBytes":1048576,"headerTimeout":"5s","requestTimeout":"10s"},"serviceControl":{"listen":"127.0.0.1:18181","publicURL":"https://core.local:18181","tls":{"certificate":"/liapoldus/tls/control.crt","key":"/liapoldus/tls/control.key"},"replicaClientCA":"/liapoldus/tls/replica-client-ca.pem","replicaServerCA":"/liapoldus/tls/replica-server-ca.pem"},"secretRoot":"/var/lib/liapoldus/secrets"}`)
	maxBody := int64(2097152)
	patch := domain.CoreSettingsPatch{SchemaVersion: "core-settings-patch/v1", ExpectedRevision: 8, Management: domain.CoreManagementSettingsPatch{MaxBodyBytes: &maxBody}}
	merged, err := mergeCoreSettingsPatch(current, patch)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(merged, &result); err != nil {
		t.Fatal(err)
	}
	management := result["management"].(map[string]any)
	tls := management["tls"].(map[string]any)
	if management["maxBodyBytes"] != float64(2097152) || management["listen"] != "127.0.0.1:18180" || tls["key"] != privatePath {
		t.Fatalf("typed update changed more than its allowed scalar field: %s", merged)
	}
}

func TestReadCoreSettingsPatchRequiresStrictBoundedTypedRequest(t *testing.T) {
	valid := `{"schemaVersion":"core-settings-patch/v1","expectedRevision":8,"management":{"maxBodyBytes":2097152},"serviceControl":{}}`
	path := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, err := readCoreSettingsPatch(path)
	if err != nil || patch.ExpectedRevision != 8 || patch.Management.MaxBodyBytes == nil || *patch.Management.MaxBodyBytes != 2097152 {
		t.Fatalf("patch=%+v error=%v", patch, err)
	}
	for name, content := range map[string]string{
		"unknown key":    strings.TrimSuffix(valid, "}") + `,"management":{"secret":"bad"}}`,
		"path injection": `{"schemaVersion":"core-settings-patch/v1","expectedRevision":8,"management":{"certificate":"/private/key"},"serviceControl":{}}`,
		"no-op":          `{"schemaVersion":"core-settings-patch/v1","expectedRevision":8,"management":{},"serviceControl":{}}`,
		"zero revision":  `{"schemaVersion":"core-settings-patch/v1","expectedRevision":0,"management":{"maxBodyBytes":2097152},"serviceControl":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCoreSettingsPatch(file); err == nil {
				t.Fatal("invalid typed patch accepted")
			}
		})
	}
	if _, err := readCoreSettingsPatch("relative.json"); err == nil {
		t.Fatal("relative request path accepted")
	}
}

func TestCoreSettingsUpdateUsesTypedPatchCASAndRedactsOutput(t *testing.T) {
	const privatePath = "/liapoldus/tls/management.crt"
	initial := `{"management":{"listen":"127.0.0.1:18180","tls":{"certificate":"` + privatePath + `","key":"/liapoldus/tls/management.key"},"maxBodyBytes":1048576,"headerTimeout":"5s","requestTimeout":"10s"},"serviceControl":{"listen":"127.0.0.1:18181","publicURL":"https://core.local:18181","tls":{"certificate":"/liapoldus/tls/control.crt","key":"/liapoldus/tls/control.key"},"replicaClientCA":"/liapoldus/tls/replica-client-ca.pem","replicaServerCA":"/liapoldus/tls/replica-server-ca.pem"},"secretRoot":"/var/lib/liapoldus/secrets"}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-token" {
			t.Errorf("unexpected Core credential source")
		}
		switch request.Method {
		case http.MethodGet:
			if request.URL.Path != "/api/v2/settings" {
				t.Errorf("unexpected GET route %q", request.URL.Path)
			}
			response.Header().Set("ETag", `"core-settings-8"`)
			_, _ = response.Write([]byte(`{"desiredRevision":8,"effectiveRevision":8,"pending":false,"restartRequired":false,"settings":` + initial + `}`))
		case http.MethodPut:
			if request.URL.Path != "/api/v2/settings" || request.Header.Get("If-Match") != `"core-settings-8"` {
				t.Errorf("PUT lost CAS: route=%q etag=%q", request.URL.Path, request.Header.Get("If-Match"))
			}
			var updated map[string]any
			if err := json.NewDecoder(request.Body).Decode(&updated); err != nil {
				t.Errorf("decode PUT: %v", err)
			}
			management := updated["management"].(map[string]any)
			if management["maxBodyBytes"] != float64(2097152) || management["tls"].(map[string]any)["certificate"] != privatePath {
				t.Errorf("typed update lost existing config values: %#v", updated)
			}
			response.Header().Set("ETag", `"core-settings-9"`)
			_, _ = response.Write([]byte(`{"desiredRevision":9,"effectiveRevision":8,"pending":true,"restartRequired":true,"settings":` + string(mustJSON(t, updated)) + `}`))
		default:
			t.Errorf("unexpected Core method %s", request.Method)
		}
	}))
	defer server.Close()
	patchPath := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(patchPath, []byte(`{"schemaVersion":"core-settings-patch/v1","expectedRevision":8,"management":{"maxBodyBytes":2097152},"serviceControl":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	target := domain.Target{Name: "local", Endpoint: server.URL, CredentialRef: "target-credential"}
	runner := Runner{
		Registry:    targetRegistryStub{target: target},
		Credentials: credentialProviderStub{value: "management-token"},
		CoreFactory: func(connection domain.CoreConnection) (domain.CoreClient, error) { return coreapi.New(connection) },
	}
	var output bytes.Buffer
	args := []string{"core", "settings", "update", "--target", "local", "--request-file", patchPath, "--confirm", studioEventsFlag}
	if code := runner.Run(args, &output, &output); code != application.ExitOK {
		t.Fatalf("update code=%d output=%s", code, output.String())
	}
	if strings.Contains(output.String(), privatePath) || strings.Contains(output.String(), "management.key") || strings.Contains(output.String(), patchPath) {
		t.Fatalf("Studio output leaked a path: %s", output.String())
	}
	if !strings.Contains(output.String(), `"schemaVersion":"core-settings-update-result/v1"`) || !strings.Contains(output.String(), `"desiredRevision":9`) || !strings.Contains(output.String(), `"restartRequired":true`) {
		t.Fatalf("update result omitted desired/effective state: %s", output.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
