package auth

import (
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/internal/config"

	_ "github.com/lib-x/entsqlite"
)

func TestDemoSessionLifecycle(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:auth-service?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	baseURL, err := url.Parse("http://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BaseURL: baseURL, DemoMode: true, DemoEmail: "operator@example.test", SessionIdle: time.Hour, SessionMax: 24 * time.Hour}
	service, err := NewService(t.Context(), db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	created, token, err := service.DemoLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := service.ResolveSession(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != created.ID || resolved.Email != "operator@example.test" {
		t.Fatalf("resolved principal = %#v, created = %#v", resolved, created)
	}
	if err := service.Logout(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveSession(t.Context(), token); err == nil {
		t.Fatal("revoked session still resolves")
	}
}

func TestSafeReturnToRejectsOpenRedirects(t *testing.T) {
	for _, value := range []string{"https://example.com", "//example.com", "/\\example.com", "/%5cexample.com", "/%2fexample.com", "/r/hostile", "/ok\r\nLocation: https://example.com"} {
		if safeReturnTo(value) {
			t.Fatalf("safeReturnTo(%q) = true", value)
		}
	}
	if !safeReturnTo("/servers?new=1") {
		t.Fatal("safe local return path was rejected")
	}
}
