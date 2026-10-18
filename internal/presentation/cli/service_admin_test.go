package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

const serviceAdminTestSurface = `{"service":"server","version":1,"pages":[{"id":"sites","sections":[{"id":"sites","kind":"table","dataCapability":"server.sites.list","queryResource":"sites","columns":["siteId","currentRevision"],"actions":[{"id":"rollback","title":"Rollback","capability":"server.sites.rollback","confirmation":"Confirm rollback?","dangerous":true,"ifMatchField":"expectedCurrentRevision","inputSchema":{"type":"object","properties":{"siteId":{"type":"string","minLength":1,"maxLength":128},"expectedCurrentRevision":{"type":"string","minLength":1,"maxLength":256},"targetRevision":{"type":"string","minLength":1,"maxLength":256}},"required":["siteId","expectedCurrentRevision","targetRevision"],"additionalProperties":false}}]}]}]}`

func serviceAdminTestDescriptor() domain.ServiceAdminSurface {
	descriptor := json.RawMessage(serviceAdminTestSurface)
	digest := sha256.Sum256(descriptor)
	return domain.ServiceAdminSurface{Digest: "sha256:" + hex.EncodeToString(digest[:]), Descriptor: descriptor}
}

type serviceAdminTestCore struct {
	adminSurfaceCoreClient
	query  domain.ServiceAdminSectionQuery
	rows   json.RawMessage
	action domain.ServiceAdminActionInvocation
	result json.RawMessage
}

func (client *serviceAdminTestCore) QueryServiceAdminSection(_ context.Context, request domain.ServiceAdminSectionQuery) (json.RawMessage, error) {
	client.query = request
	return client.rows, nil
}

func (client *serviceAdminTestCore) InvokeServiceAdminAction(_ context.Context, request domain.ServiceAdminActionInvocation) (json.RawMessage, error) {
	client.action = request
	return client.result, nil
}

func TestServiceAdminQueryNarrowsRowsToLiveDescriptorColumns(t *testing.T) {
	core := &serviceAdminTestCore{
		adminSurfaceCoreClient: adminSurfaceCoreClient{surface: serviceAdminTestDescriptor()},
		rows:                   json.RawMessage(`{"items":[{"siteId":"docs","currentRevision":"r-2","privateKey":"must-not-leak"}],"nextCursor":"cursor-2"}`),
	}
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
		Credentials: credentialProviderStub{value: "not-returned"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
	}
	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"service", "admin", "query", "server", "--page", "sites", "--section", "sites", "--cursor", "cursor-1", "--target", "local"}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if core.query.ServiceID != "server" || core.query.PageID != "sites" || core.query.SectionID != "sites" ||
		core.query.ResourceID != "sites" || core.query.Cursor != "cursor-1" || core.query.SurfaceDigest != core.adminSurfaceCoreClient.surface.Digest {
		t.Fatalf("unexpected Core query: %+v", core.query)
	}
	if strings.Contains(stdout.String(), "privateKey") || strings.Contains(stdout.String(), "must-not-leak") || !strings.Contains(stdout.String(), `"currentRevision":"r-2"`) {
		t.Fatalf("query result was not narrowed to declared columns: %s", stdout.String())
	}
}

func TestServiceAdminActionUsesProjectInputAndHidesServiceResult(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "action.json"), []byte(`{"siteId":"docs","expectedCurrentRevision":"r-2","targetRevision":"r-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &serviceAdminTestCore{
		adminSurfaceCoreClient: adminSurfaceCoreClient{surface: serviceAdminTestDescriptor()},
		result:                 json.RawMessage(`{"completed":true,"credential":"must-not-reach-plugin"}`),
	}
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
		Credentials: credentialProviderStub{value: "not-returned"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
	}
	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"service", "admin", "invoke", "server", "--page", "sites", "--section", "sites", "--action", "rollback",
		"--project", project, "--request-file", "action.json", "--if-match", "r-2", "--idempotency-key", "action-1", "--target", "local", "--confirm"}, &stdout, &stderr)
	if code != application.ExitOK || string(stdout.Bytes()) != "{\"completed\":true}\n" {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if core.action.ServiceID != "server" || core.action.PageID != "sites" || core.action.SectionID != "sites" || core.action.ActionID != "rollback" ||
		core.action.IfMatch != "r-2" || core.action.IdempotencyKey != "action-1" || string(core.action.Input) != `{"siteId":"docs","expectedCurrentRevision":"r-2","targetRevision":"r-1"}` {
		t.Fatalf("unexpected action request: %+v", core.action)
	}
	if strings.Contains(stdout.String(), "must-not-reach-plugin") || strings.Contains(stdout.String(), project) || strings.Contains(stderr.String(), "not-returned") {
		t.Fatalf("action output leaked local or service data: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestServiceAdminActionRejectsUnconfirmedAndMismatchedCASBeforeCore(t *testing.T) {
	for name, extra := range map[string][]string{
		"no confirmation": {"--if-match", "r-2", "--idempotency-key", "action-1", "--target", "local"},
		"mismatched CAS":  {"--if-match", "r-1", "--idempotency-key", "action-1", "--target", "local", "--confirm"},
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			if err := os.WriteFile(filepath.Join(project, "action.json"), []byte(`{"siteId":"docs","expectedCurrentRevision":"r-2","targetRevision":"r-1"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			core := &serviceAdminTestCore{adminSurfaceCoreClient: adminSurfaceCoreClient{surface: serviceAdminTestDescriptor()}}
			runner := Runner{
				Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
				Credentials: credentialProviderStub{value: "not-returned"},
				CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
			}
			args := []string{"service", "admin", "invoke", "server", "--page", "sites", "--section", "sites", "--action", "rollback", "--project", project, "--request-file", "action.json"}
			args = append(args, extra...)
			var stdout, stderr bytes.Buffer
			if code := runner.Run(args, &stdout, &stderr); code == application.ExitOK || core.action.ActionID != "" {
				t.Fatalf("unsafe action reached Core: code=%d request=%+v stdout=%s stderr=%s", code, core.action, stdout.String(), stderr.String())
			}
		})
	}
}

func TestSanitizeServiceAdminRowsRejectsMalformedAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"nextCursor":"x"}`, `{"items":null}`, `{"items":[]} {"credential":"hidden"}`} {
		if _, err := sanitizeServiceAdminRows(json.RawMessage(raw), []string{"siteId"}); err == nil {
			t.Fatalf("invalid response accepted: %s", raw)
		}
	}
}
