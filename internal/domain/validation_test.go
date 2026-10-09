package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBundleValidateChecksDigestAndOrdering(t *testing.T) {
	files := []File{
		{Path: "project.yaml", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 1},
		{Path: "services/orders/settings.json", Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 2},
	}
	bundle := Bundle{SchemaVersion: "core-config/v2", Revision: "abc", Files: files, Services: []Service{{ID: "orders", Settings: json.RawMessage(`{"enabled":true}`)}}}
	bundle.Digest = BundleDigest(files)
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	bundle.Files[0], bundle.Files[1] = bundle.Files[1], bundle.Files[0]
	if err := bundle.Validate(); err == nil {
		t.Fatal("expected ordering error")
	}
}
