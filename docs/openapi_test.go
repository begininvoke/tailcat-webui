package docs

import (
	"os"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIIsValidYAMLWithPaths(t *testing.T) {
	data, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI    string                    `yaml:"openapi"`
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `yaml:"properties"`
				Enum       []string       `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse OpenAPI YAML: %v", err)
	}
	if document.OpenAPI != "3.1.0" || len(document.Paths) < 10 {
		t.Fatalf("unexpected OpenAPI document: version=%q paths=%d", document.OpenAPI, len(document.Paths))
	}
	for path, method := range map[string]string{"/servers": "post", "/servers/{id}/exit-node": "post", "/servers/{id}/exit-rules": "post", "/exit-rules/{id}": "delete", "/clients/{id}/tunnel": "get", "/routes/{id}/open": "get", "/events": "get"} {
		if document.Paths[path][method] == nil {
			t.Errorf("OpenAPI operation %s %s is missing", method, path)
		}
	}
	for schema, fields := range map[string][]string{"Server": {"allowlist_enabled", "mapping_count", "created_at"}, "ExitRule": {"server_id", "prefix", "start_port", "end_port", "enabled", "created_at", "updated_at"}, "Client": {"saved_key", "last_ping_at", "created_at"}, "Route": {"allowed_methods", "enabled", "updated_at"}} {
		for _, field := range fields {
			if document.Components.Schemas[schema].Properties[field] == nil {
				t.Errorf("OpenAPI schema %s.%s is missing", schema, field)
			}
		}
	}
	if got, want := document.Components.Schemas["RuntimePhase"].Enum, []string{"idle", "starting", "connecting", "ready", "running", "stopping", "stopped", "error", "interrupted"}; !slices.Equal(got, want) {
		t.Errorf("RuntimePhase enum = %v, want %v", got, want)
	}
	for _, field := range []string{"version", "type", "resource_kind", "resource_id", "operation_id", "phase", "sequence", "at", "payload"} {
		if document.Components.Schemas["RuntimeEvent"].Properties[field] == nil {
			t.Errorf("OpenAPI schema RuntimeEvent.%s is missing", field)
		}
	}
}
