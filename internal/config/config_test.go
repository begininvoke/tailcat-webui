package config

import (
	"net/url"
	"strings"
	"testing"
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
