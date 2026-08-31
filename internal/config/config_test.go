package config

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ca-x/tailcat-webui/internal/tailnet"
)

func TestSQLiteDSNUsesWALWithoutSharedCache(t *testing.T) {
	dsn := SQLiteDSN("/tmp/tailcat.db")
	if !strings.Contains(dsn, "journal_mode(WAL)") {
		t.Fatalf("SQLiteDSN must enable WAL: %q", dsn)
	}
	if strings.Contains(dsn, "cache=shared") {
		t.Fatalf("SQLiteDSN must not serialize WAL readers with shared cache: %q", dsn)
	}
}

func TestValidateRequiresSeparatePublishOrigin(t *testing.T) {
	base, _ := url.Parse("http://localhost:8080")
	cfg := Config{BaseURL: base, PublishURL: base, DemoMode: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted the management origin for published content")
	}
}

func TestDemoModeRequiresLoopback(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_BASE_URL", "http://example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted non-loopback demo mode")
	}
}

func TestLoadParsesPortAwareTargetRules(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_ALLOWED_MAPPING_TARGETS", "127.0.0.0/8,example.com@443")
	t.Setenv("TAILCAT_WEBUI_ALLOWED_EXIT_TARGETS", "2001:db8::/32@8000-8001")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantMapping, err := tailnet.ParseTargetRules("127.0.0.0/8,example.com@443")
	if err != nil {
		t.Fatal(err)
	}
	wantExit, err := tailnet.ParseTargetRules("2001:db8::/32@8000-8001")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.MappingTargets, wantMapping) {
		t.Fatalf("MappingTargets = %#v, want %#v", cfg.MappingTargets, wantMapping)
	}
	if !reflect.DeepEqual(cfg.ExitTargets, wantExit) {
		t.Fatalf("ExitTargets = %#v, want %#v", cfg.ExitTargets, wantExit)
	}
}

func TestLoadPreservesAllowedTargetsAlias(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_ALLOWED_MAPPING_TARGETS", "")
	t.Setenv("TAILCAT_WEBUI_ALLOWED_TARGETS", "192.0.2.0/24@8080")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want, err := tailnet.ParseTargetRules("192.0.2.0/24@8080")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.MappingTargets, want) {
		t.Fatalf("MappingTargets = %#v, want legacy alias %#v", cfg.MappingTargets, want)
	}
}
