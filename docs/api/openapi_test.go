package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCEOpenAPIYAMLParses(t *testing.T) {
	path := filepath.Join("openapi.ce.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal(%q): %v", path, err)
	}

	if got := doc["openapi"]; got == nil || got == "" {
		t.Fatalf("%q missing openapi version", path)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] == nil {
		t.Fatalf("%q missing info.title", path)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatalf("%q missing paths", path)
	}
}
