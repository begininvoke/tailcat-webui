package docs

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ca-x/tailcat-webui/internal/httpapi"

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
	transferRoutes := map[string][]string{
		"/transfers/shares":                             {"get", "post"},
		"/transfers/shares/{id}":                        {"get", "delete"},
		"/transfers/shares/{id}/files":                  {"get", "post"},
		"/transfers/shares/{id}/finalize":               {"post"},
		"/transfers/shares/{id}/rotate":                 {"post"},
		"/transfers/jobs":                               {"get", "post"},
		"/transfers/jobs/{id}":                          {"get", "delete"},
		"/transfers/jobs/{id}/start":                    {"post"},
		"/transfers/jobs/{id}/cancel":                   {"post"},
		"/transfers/jobs/{id}/retry":                    {"post"},
		"/transfers/jobs/{id}/items":                    {"get"},
		"/transfers/jobs/{id}/items/{item_id}":          {"get"},
		"/transfers/jobs/{id}/items/{item_id}/download": {"get", "head"},
	}
	for path, methods := range transferRoutes {
		for _, method := range methods {
			operation, ok := document.Paths[path][method].(map[string]any)
			if !ok {
				t.Errorf("OpenAPI transfer operation %s %s is missing", method, path)
				continue
			}
			responses, ok := operation["responses"].(map[string]any)
			if !ok || responses["503"] == nil {
				t.Errorf("OpenAPI transfer operation %s %s is missing 503", method, path)
			}
			if (method == "post" || method == "delete") && (responses == nil || responses["413"] == nil) {
				t.Errorf("OpenAPI transfer mutation %s %s is missing global body-limit 413", method, path)
			}
		}
	}
	openAPIUploadPath := "/api/v1" + strings.ReplaceAll(httpapi.TransferUploadRoute, ":id", "{id}")
	if openAPIUploadPath != "/api/v1/transfers/shares/{id}/files" || document.Paths[strings.TrimPrefix(openAPIUploadPath, "/api/v1")]["post"] == nil {
		t.Fatalf("upload body-limit route %q does not have exact OpenAPI parity", httpapi.TransferUploadRoute)
	}
	upload := document.Paths["/transfers/shares/{id}/files"]["post"].(map[string]any)
	requestBody := upload["requestBody"].(map[string]any)
	content := requestBody["content"].(map[string]any)
	if content["application/octet-stream"] == nil {
		t.Error("transfer upload is not documented as raw application/octet-stream")
	}
	parameters, ok := upload["parameters"].([]any)
	if !ok {
		t.Fatal("transfer upload parameters are missing")
	}
	uploadHeaders := make(map[string]bool)
	for _, value := range parameters {
		parameter, _ := value.(map[string]any)
		if parameter["in"] == "header" {
			uploadHeaders[parameter["name"].(string)] = parameter["required"] == true
		}
	}
	if !uploadHeaders["Content-Length"] || !uploadHeaders["X-Tailcat-Virtual-Path"] {
		t.Errorf("transfer upload required headers = %v", uploadHeaders)
	}
	for _, status := range []string{"201", "400", "401", "403", "404", "409", "413", "415", "422", "429"} {
		if upload["responses"].(map[string]any)[status] == nil {
			t.Errorf("transfer upload response %s is missing", status)
		}
	}
	download := document.Paths["/transfers/jobs/{id}/items/{item_id}/download"]["get"].(map[string]any)
	downloadResponses := download["responses"].(map[string]any)
	for _, status := range []string{"200", "206", "304", "412", "416", "503"} {
		if downloadResponses[status] == nil {
			t.Errorf("transfer download response %s is missing", status)
		}
	}
	for status, wantHeaders := range map[string][]string{
		"200": {"Accept-Ranges", "Content-Disposition", "Content-Length", "Last-Modified"},
		"206": {"Accept-Ranges", "Content-Disposition", "Content-Length", "Content-Range", "Last-Modified"},
		"304": {"Last-Modified"},
		"412": {"Last-Modified"},
		"416": {"Content-Range"},
	} {
		response := downloadResponses[status].(map[string]any)
		headers, _ := response["headers"].(map[string]any)
		for _, header := range wantHeaders {
			if headers[header] == nil {
				t.Errorf("transfer download %s header %s is missing", status, header)
			}
		}
	}
	for schema, forbidden := range map[string][]string{
		"TransferShare":     {"capability", "capability_hash", "storage_name", "blake3"},
		"TransferShareFile": {"capability", "storage_name", "blake3", "block_hashes"},
		"TransferJob":       {"capability", "remote_capability_cipher", "storage_name", "blake3"},
		"TransferItem":      {"capability", "remote_capability_cipher", "storage_name", "blake3", "block_hashes"},
	} {
		for _, field := range forbidden {
			if document.Components.Schemas[schema].Properties[field] != nil {
				t.Errorf("OpenAPI schema %s leaks %s", schema, field)
			}
		}
	}
	if document.Components.Schemas["TransferShareCreated"].Properties["capability"] == nil || document.Components.Schemas["TransferCapabilityRotated"].Properties["capability"] == nil {
		t.Error("one-time transfer capability schemas are missing capability")
	}
	for schema, want := range map[string][]string{
		"TransferShare":     {"created_at", "expires_at", "file_count", "id", "ready_at", "server_id", "status", "total_bytes", "updated_at"},
		"TransferShareFile": {"created_at", "id", "mtime", "size", "virtual_path"},
		"TransferJob":       {"client_id", "created_at", "error_code", "expires_at", "finished_at", "id", "received_bytes", "remote_share_id", "started_at", "status", "total_bytes", "updated_at"},
		"TransferItem":      {"completed_blocks", "created_at", "finished_at", "id", "job_id", "mtime", "received_bytes", "size", "started_at", "status", "updated_at", "virtual_path"},
	} {
		if got := slices.Sorted(maps.Keys(document.Components.Schemas[schema].Properties)); !slices.Equal(got, want) {
			t.Errorf("OpenAPI schema %s fields = %v, want exact DTO allowlist %v", schema, got, want)
		}
	}
	startDiagnostic, ok := document.Paths["/clients/{id}/diagnostics"]["post"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI start diagnostic operation is missing")
	}
	responses, ok := startDiagnostic["responses"].(map[string]any)
	if !ok || responses["400"] == nil {
		t.Error("OpenAPI start diagnostic 400 BAD_REQUEST response is missing")
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
