package config

import (
	"math"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/internal/tailnet"
)

func TestLoadTransferDefaultsAndEnvironmentOverrides(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantDefaults := Transfer{
		MaxFileBytes: 512 << 20, MaxShareBytes: 1 << 30, MaxJobBytes: 1 << 30,
		MaxOwnerBytes: 2 << 30, MaxFilesPerShare: 1000, Workers: 4,
		MaxJobsPerOwner: 2, Expiry: 24 * time.Hour, Retention: 24 * time.Hour,
		UploadTimeout: 30 * time.Minute,
	}
	if cfg.Transfer != wantDefaults {
		t.Fatalf("Transfer defaults = %+v, want %+v", cfg.Transfer, wantDefaults)
	}

	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_FILE_BYTES", " 64 MiB ")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_SHARE_BYTES", "128mib")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_JOB_BYTES", "256MIB")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_OWNER_BYTES", " 512 mib")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_FILES_PER_SHARE", "25")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_WORKERS", "4")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_JOBS_PER_OWNER", "1")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_EXPIRY", "12h")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_RETENTION", "12h")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_UPLOAD_TIMEOUT", "15m")

	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	wantOverrides := Transfer{
		MaxFileBytes: 64 << 20, MaxShareBytes: 128 << 20, MaxJobBytes: 256 << 20,
		MaxOwnerBytes: 512 << 20, MaxFilesPerShare: 25, Workers: 4,
		MaxJobsPerOwner: 1, Expiry: 12 * time.Hour, Retention: 12 * time.Hour,
		UploadTimeout: 15 * time.Minute,
	}
	if cfg.Transfer != wantOverrides {
		t.Fatalf("Transfer overrides = %+v, want %+v", cfg.Transfer, wantOverrides)
	}
}

func TestParseIECBytesPolicyAndFailures(t *testing.T) {
	for raw, want := range map[string]int64{
		"1B": 1, "1 KiB": 1 << 10, "2kib": 2 << 10,
		"3 MIB": 3 << 20, " 4GiB ": 4 << 30,
	} {
		got, err := parseIECBytes(raw)
		if err != nil || got != want {
			t.Errorf("parseIECBytes(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0", "0MiB", "-1MiB", "+1MiB", "1.5MiB", "1 MB", "1Ki", "1MiB trailing", "18446744073709551615GiB"} {
		if _, err := parseIECBytes(raw); err == nil {
			t.Errorf("parseIECBytes(%q) succeeded", raw)
		}
	}
	if _, err := checkedIECBytes(math.MaxInt64, 2); err == nil {
		t.Fatal("checkedIECBytes accepted overflow")
	}
}

func TestLoadRejectsUnsafeTransferConfiguration(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"TAILCAT_WEBUI_TRANSFER_MAX_FILE_BYTES", "0MiB"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_SHARE_BYTES", "2GiB"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_JOB_BYTES", "2GiB"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_OWNER_BYTES", "3GiB"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_FILES_PER_SHARE", "1001"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_FILES_PER_SHARE", "-1"},
		{"TAILCAT_WEBUI_TRANSFER_WORKERS", "5"},
		{"TAILCAT_WEBUI_TRANSFER_MAX_JOBS_PER_OWNER", "3"},
		{"TAILCAT_WEBUI_TRANSFER_EXPIRY", "0s"},
		{"TAILCAT_WEBUI_TRANSFER_EXPIRY", "25h"},
		{"TAILCAT_WEBUI_TRANSFER_RETENTION", "721h"},
		{"TAILCAT_WEBUI_TRANSFER_UPLOAD_TIMEOUT", "-1s"},
		{"TAILCAT_WEBUI_TRANSFER_UPLOAD_TIMEOUT", "61m"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestLoadRequiresExactlyFourTransferWorkers(t *testing.T) {
	for _, workers := range []string{"1", "2", "3", "5"} {
		t.Run(workers, func(t *testing.T) {
			t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
			t.Setenv("TAILCAT_WEBUI_TRANSFER_WORKERS", workers)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted %s transfer workers", workers)
			}
		})
	}
	t.Run("four", func(t *testing.T) {
		t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
		t.Setenv("TAILCAT_WEBUI_TRANSFER_WORKERS", "4")
		cfg, err := Load()
		if err != nil || cfg.Transfer.Workers != 4 {
			t.Fatalf("Load workers = %d, %v", cfg.Transfer.Workers, err)
		}
	})
}

func TestTransferRetentionAloneAliasesTheExpiryLifetime(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_RETENTION", "12h")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transfer.Retention != 12*time.Hour || cfg.Transfer.Expiry != 12*time.Hour {
		t.Fatalf("retention alias = expiry %s retention %s", cfg.Transfer.Expiry, cfg.Transfer.Retention)
	}
}

func TestTransferExpiryAndRetentionMustMatch(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_EXPIRY", "12h")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_RETENTION", "13h")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted divergent expiry and retention lifetimes")
	}
}

func TestLoadRejectsInvertedTransferByteLimits(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_FILE_BYTES", "256MiB")
	t.Setenv("TAILCAT_WEBUI_TRANSFER_MAX_SHARE_BYTES", "128MiB")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted max file bytes above max share bytes")
	}
}

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
