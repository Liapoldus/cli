package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/domain"
)

func TestServiceResourceQueryRequiresAnExplicitCoreBackedCatalog(t *testing.T) {
	manifest := json.RawMessage(`{"id":"server","resources":[{"id":"sites","kind":"site","provider":"service-admin","pageId":"sites","queryResource":"sites","valuePointer":"/siteId","labelPointer":"/domain"},{"id":"site-releases","kind":"site-release","provider":"service-admin","pageId":"sites","queryResource":"releases","valuePointer":"/revisionId","labelPointer":"/revisionId"}]}`)
	query, err := serviceResourceQuery(manifest, "sites")
	if err != nil || query.pageID != "sites" || query.resourceID != "sites" || query.valuePointer != "/siteId" || query.labelPointer != "/domain" {
		t.Fatalf("query=%+v error=%v", query, err)
	}
	releaseQuery, err := serviceResourceQuery(manifest, "site-releases")
	if err != nil || releaseQuery.pageID != "sites" || releaseQuery.resourceID != "releases" || releaseQuery.valuePointer != "/revisionId" || releaseQuery.labelPointer != "/revisionId" {
		t.Fatalf("site-release query=%+v error=%v", releaseQuery, err)
	}
	for name, document := range map[string]json.RawMessage{
		"unknown resource":  json.RawMessage(`{"resources":[]}`),
		"unknown kind":      json.RawMessage(`{"resources":[{"id":"sites","kind":"directory","provider":"service-admin","pageId":"sites","queryResource":"sites","valuePointer":"/siteId","labelPointer":"/domain"}]}`),
		"project resource":  json.RawMessage(`{"resources":[{"id":"sites","kind":"site","provider":"project"}]}`),
		"sensitive pointer": json.RawMessage(`{"resources":[{"id":"sites","kind":"site","provider":"service-admin","pageId":"sites","queryResource":"sites","valuePointer":"/privateKeyRef","labelPointer":"/domain"}]}`),
		"traversal":         json.RawMessage(`{"resources":[{"id":"sites","kind":"site","provider":"service-admin","pageId":"../sites","queryResource":"sites","valuePointer":"/siteId","labelPointer":"/domain"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serviceResourceQuery(document, "sites"); err == nil {
				t.Fatal("invalid or unsupported catalog was accepted")
			}
		})
	}
}

func TestServiceResourceItemsExposeOnlyBoundedMappedValues(t *testing.T) {
	response := json.RawMessage(`{"items":[{"siteId":"docs","domain":"docs.example.test","credential":"must-not-leak"},{"siteId":"api","domain":"api.example.test","credential":"also-hidden"}]}`)
	items, err := sanitizeServiceResourceItems(response, "/siteId", "/domain")
	if err != nil || len(items) != 2 || items[0].Value != "docs" || items[0].Label != "docs.example.test" {
		t.Fatalf("items=%+v error=%v", items, err)
	}
	encoded, err := json.Marshal(items)
	if err != nil || string(encoded) != `[{"value":"docs","label":"docs.example.test"},{"value":"api","label":"api.example.test"}]` {
		t.Fatalf("unexpected exposed fields %s: %v", encoded, err)
	}
	if _, err := sanitizeServiceResourceItems(json.RawMessage(`{"items":[1]}`), "/siteId", "/domain"); err == nil {
		t.Fatal("malformed item was silently accepted")
	}
	if _, err := sanitizeServiceResourceItems(json.RawMessage(`{"items":[]}`), "/privateKeyRef", "/domain"); err == nil {
		t.Fatal("sensitive pointer was accepted")
	}
	if _, err := sanitizeServiceResourceItems(json.RawMessage(`{"items":[]} {"items":[]}`), "/siteId", "/domain"); err == nil {
		t.Fatal("trailing JSON document was accepted")
	}
}

func TestServiceResourcePointersFollowJSONPointerEscapesAndArrayIndexes(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(json.RawMessage(`{"items":[{"a~1b":"selected","nested":[{"id":"item-1"}]}]}`), &document); err != nil {
		t.Fatal(err)
	}
	items := document["items"].([]any)
	if value, ok := valueAtResourcePointer(items[0], "/a~01b"); !ok || value != "selected" {
		t.Fatalf("RFC 6901 ~01 escape was not preserved: value=%v ok=%v", value, ok)
	}
	if value, ok := valueAtResourcePointer(items[0], "/nested/0/id"); !ok || value != "item-1" {
		t.Fatalf("array index was not resolved: value=%v ok=%v", value, ok)
	}
	for _, pointer := range []string{"/a~2b", "/nested/00/id", "/nested/-/id"} {
		if _, ok := valueAtResourcePointer(items[0], pointer); ok {
			t.Fatalf("ambiguous JSON Pointer %q was accepted", pointer)
		}
	}
}

type resourceCoreClient struct{ healthyCoreClient }

func (resourceCoreClient) ServiceMetadata(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"resources":[{"id":"sites","kind":"site","provider":"service-admin","pageId":"sites","queryResource":"sites","valuePointer":"/siteId","labelPointer":"/domain"}]}`), nil
}

func (resourceCoreClient) ServiceAdminQuery(context.Context, string, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"items":[{"siteId":"docs","domain":"docs.example.test","credential":"secret"}]}`), nil
}

func TestServiceResourceCommandUsesCoreOnlyAndReturnsMappedFields(t *testing.T) {
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
		Credentials: credentialProviderStub{value: "not-returned"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return resourceCoreClient{}, nil },
	}
	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"service", "resource", "list", "server", "--resource", "sites", "--target", "local"}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "credential") || strings.Contains(stdout.String(), "secret") || !strings.Contains(stdout.String(), `"value":"docs"`) {
		t.Fatalf("resource output was not narrowed to the declared safe fields: %s", stdout.String())
	}
}

type adminSurfaceCoreClient struct {
	resourceCoreClient
	surface domain.ServiceAdminSurface
}

func (client adminSurfaceCoreClient) ReadServiceAdminSurface(context.Context, string) (domain.ServiceAdminSurface, error) {
	return client.surface, nil
}

func TestServiceAdminSurfaceCommandUsesTypedCoreReader(t *testing.T) {
	descriptor := json.RawMessage(`{"version":1,"pages":[{"id":"sites","sections":[{"id":"operations","actions":[{"id":"publish","artifactInput":{"mediaTypes":["application/gzip"],"maxBytes":1024}}]}]}]}`)
	descriptorDigest := sha256.Sum256(descriptor)
	client := adminSurfaceCoreClient{surface: domain.ServiceAdminSurface{
		Digest:     "sha256:" + hex.EncodeToString(descriptorDigest[:]),
		Descriptor: descriptor,
	}}
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
		Credentials: credentialProviderStub{value: "not-returned"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return client, nil },
	}
	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"service", "admin-surface", "inspect", "server", "--target", "local"}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result struct {
		ServiceID  string          `json:"serviceId"`
		Digest     string          `json:"digest"`
		Descriptor json.RawMessage `json:"descriptor"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.ServiceID != "server" || result.Digest != client.surface.Digest || string(result.Descriptor) != string(client.surface.Descriptor) {
		t.Fatalf("unexpected descriptor result: %+v err=%v output=%s", result, err, stdout.String())
	}
	if strings.Count(stdout.String(), "sha256:") != 1 || strings.Contains(stdout.String(), "sha256:sha256:") {
		t.Fatalf("CLI JSON must expose one canonical digest prefix: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "not-returned") || strings.Contains(stderr.String(), "not-returned") {
		t.Fatalf("Core credential leaked: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestServiceAdminSurfaceRejectsMissingTargetOrUnsafeDescriptor(t *testing.T) {
	for name, args := range map[string][]string{
		"remote target":  {"service", "admin-surface", "inspect", "server", "--target", "production"},
		"missing target": {"service", "admin-surface", "inspect", "server"},
	} {
		t.Run(name, func(t *testing.T) {
			runner := Runner{CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return adminSurfaceCoreClient{}, nil }}
			var stdout, stderr bytes.Buffer
			if code := runner.Run(args, &stdout, &stderr); code == application.ExitOK {
				t.Fatalf("unsafe admin-surface command accepted: %s", stdout.String())
			}
		})
	}
	for _, surface := range []domain.ServiceAdminSurface{
		{Digest: "sha256:" + strings.Repeat("a", 64), Descriptor: json.RawMessage(`{}`)},
		{Digest: "invalid", Descriptor: json.RawMessage(`{"version":1}`)},
	} {
		runner := Runner{
			Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
			Credentials: credentialProviderStub{value: "not-returned"},
			CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) {
				return adminSurfaceCoreClient{surface: surface}, nil
			},
		}
		var stdout, stderr bytes.Buffer
		if code := runner.Run([]string{"service", "admin-surface", "inspect", "server", "--target", "local"}, &stdout, &stderr); code == application.ExitOK {
			t.Fatalf("invalid Core descriptor accepted: %s", stdout.String())
		}
	}
}

func TestServiceAdminSurfaceDigestMustMatchExactDescriptor(t *testing.T) {
	descriptor := json.RawMessage(`{"version":1}`)
	digest := sha256.Sum256(descriptor)
	valid := "sha256:" + hex.EncodeToString(digest[:])
	for name, candidate := range map[string]string{
		"canonical":     valid,
		"double prefix": "sha256:" + valid,
		"uppercase":     "sha256:" + strings.ToUpper(valid[len("sha256:"):]),
		"mismatch":      "sha256:" + strings.Repeat("0", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if got := validServiceAdminSurfaceDigest(candidate, descriptor); got != (name == "canonical") {
				t.Fatalf("validServiceAdminSurfaceDigest(%q)=%v", candidate, got)
			}
		})
	}
}

type resourcePublishCoreClient struct {
	resourceCoreClient
	publication domain.ServiceArtifactPublication
}

func (client *resourcePublishCoreClient) PublishServiceArtifact(_ context.Context, publication domain.ServiceArtifactPublication) (json.RawMessage, error) {
	client.publication = publication
	content, err := io.ReadAll(publication.Artifact)
	if err != nil {
		return nil, err
	}
	if string(content) != "archive-bytes" {
		return nil, errInvalidServiceResource
	}
	return json.RawMessage(`{"operationId":"publish-1","state":"pending"}`), nil
}

func TestServiceResourcePublishUsesTypedCoreClientAndProjectScopedFiles(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "site.tar.gz"), []byte("archive-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "metadata.json"), []byte(`{"siteId":"docs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &resourcePublishCoreClient{}
	runner := Runner{
		Registry:    targetRegistryStub{target: domain.Target{Name: "local", Endpoint: "https://core.example.test", CredentialRef: "core-token"}},
		Credentials: credentialProviderStub{value: "not-returned"},
		CoreFactory: func(domain.CoreConnection) (domain.CoreClient, error) { return core, nil },
	}
	var stdout, stderr bytes.Buffer
	code := runner.Run([]string{"service", "resource", "publish", "server", "--project", project,
		"--page", "sites", "--action", "publish", "--artifact-file", "site.tar.gz",
		"--content-type", "application/gzip", "--metadata-file", "metadata.json",
		"--if-match", `"surface-1"`, "--idempotency-key", "operation-1", "--target", "local"}, &stdout, &stderr)
	if code != application.ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if stdout.String() != "{\"operationId\":\"publish-1\",\"state\":\"pending\"}\n" || core.publication.ServiceID != "server" || core.publication.PageID != "sites" || core.publication.ActionID != "publish" || string(core.publication.Metadata) != `{"siteId":"docs"}` || core.publication.ContentType != "application/gzip" {
		t.Fatalf("unexpected CLI publish result/request: stdout=%s publication=%+v", stdout.String(), core.publication)
	}
	if strings.Contains(stdout.String(), project) || strings.Contains(stdout.String(), "archive-bytes") || strings.Contains(stderr.String(), "not-returned") {
		t.Fatalf("CLI leaked local data: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestResolveProjectFileRejectsSymlinkEscape(t *testing.T) {
	project := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(external, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(project, "linked.json")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := resolveProjectFile(project, link); err == nil {
		t.Fatal("project file symlink escape accepted")
	}
}
