package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIDoesNotContainPluginRESTClient(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	var forbidden []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if filepath.Base(path) == "plugin_architecture_test.go" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(body)
		for _, marker := range []string{"plugin-sdk", "/_liapoldus/v1/manifest", "/_liapoldus/v1/config-schema", "/_liapoldus/v1/reload"} {
			if strings.Contains(text, marker) {
				forbidden = append(forbidden, path+":"+marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(forbidden) > 0 {
		t.Fatalf("CLI contains a direct plugin REST path: %v", forbidden)
	}
}
