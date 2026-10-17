package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liapoldus/cli/internal/application"
	"github.com/Liapoldus/cli/internal/infrastructure/artifacts"
)

func TestServiceCommandsReplaceRuntimePluginCommands(t *testing.T) {
	for _, args := range [][]string{{"plugin", "artifact", "list"}, {"plugin", "status", "server", "--target", "local"}} {
		var stdout, stderr bytes.Buffer
		if code := (Runner{}).Run(args, &stdout, &stderr); code != application.ExitArguments || stderr.String() != "unsupported command\n" || stdout.Len() != 0 {
			t.Fatalf("legacy command %v: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run([]string{"service"}, &stdout, &stderr); code != application.ExitArguments || stderr.String() != "service artifact|inspect|manifest|schema|status|admin-surface|resource is required\n" {
		t.Fatalf("service command: code=%d stderr=%q", code, stderr.String())
	}
}

func TestServiceArtifactCommandsReuseExistingRegistry(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "echo")
	if err := os.WriteFile(binary, []byte("prebuilt"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := artifacts.Digest(binary)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Artifacts: artifacts.Registry{Path: filepath.Join(directory, "artifacts.json")}}
	for _, args := range [][]string{
		{"service", "artifact", "register", "--id", "echo", "--version", "1.0.0", "--platform", "linux/arm64", "--path", binary},
		{"service", "artifact", "trust", "--id", "echo", "--version", "1.0.0", "--platform", "linux/arm64", "--digest", digest},
		{"service", "artifact", "inspect", "--id", "echo", "--version", "1.0.0", "--platform", "linux/arm64"},
		{"service", "artifact", "list"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runner.Run(args, &stdout, &stderr); code != application.ExitOK || !strings.Contains(stdout.String(), digest) {
			t.Fatalf("command %v: code=%d out=%s err=%s", args, code, stdout.String(), stderr.String())
		}
	}
	registered, err := runner.Artifacts.Find("echo", "1.0.0", "linux/arm64")
	if err != nil || !registered.Trusted {
		t.Fatalf("registration lost: %+v, %v", registered, err)
	}
}
