package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/labstack/echo/v5"

	_ "github.com/lib-x/entsqlite"
)

func TestTunnelLimitIsPerUser(t *testing.T) {
	api := &API{tunnels: make(map[string]int)}
	for range 8 {
		if !api.acquireTunnel("user-a") {
			t.Fatal("tunnel limit rejected an allowed slot")
		}
	}
	if api.acquireTunnel("user-a") {
		t.Fatal("ninth tunnel was accepted")
	}
	if !api.acquireTunnel("user-b") {
		t.Fatal("one user exhausted another user's tunnel slots")
	}
	api.releaseTunnel("user-a")
	if !api.acquireTunnel("user-a") {
		t.Fatal("released tunnel slot was not reusable")
	}
}

func TestJSONDeserializerRejectsUnknownFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/servers", strings.NewReader(`{"name":"server","unexpected":true}`))
	ctx := echo.New().NewContext(request, httptest.NewRecorder())
	var target struct {
		Name string `json:"name"`
	}
	if err := (jsonV2Serializer{}).Deserialize(ctx, &target); err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
}

func TestOriginBoundarySeparatesManagementAndPublishedContent(t *testing.T) {
	management, _ := url.Parse("https://tailcat.example.com")
	published, _ := url.Parse("https://publish.tailcat.example.com")
	api := &API{cfg: config.Config{BaseURL: management, PublishURL: published}}
	handler := api.enforceOriginBoundary(func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	tests := []struct {
		host, path string
		allowed    bool
	}{
		{"tailcat.example.com", "/api/v1/health", true},
		{"routeid.publish.tailcat.example.com", "/r/demo/", true},
		{"tailcat.example.com", "/r/demo/", false},
		{"publish.tailcat.example.com", "/api/v1/auth/me", false},
		{"unknown.example.com", "/", false},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "https://"+test.host+test.path, nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		err := handler(ctx)
		if test.allowed && err != nil {
			t.Errorf("%s%s rejected: %v", test.host, test.path, err)
		}
		if !test.allowed && err == nil {
			t.Errorf("%s%s was allowed", test.host, test.path)
		}
	}
}

func TestTunnelRejectsCrossOriginBeforeDial(t *testing.T) {
	dialCalls := 0
	api := &API{
		tunnels: make(map[string]int),
		tunnelDial: func(context.Context, string, string, string) (net.Conn, error) {
			dialCalls++
			return nil, nil
		},
	}
	request := httptest.NewRequest(http.MethodGet, "https://tailcat.example.com/api/v1/clients/client-a/tunnel?address=server.tailcat:80", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Origin", "https://route.publish.tailcat.example.com")
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set(principalKey, auth.Principal{ID: "user-a"})
	ctx.SetPathValues(echo.PathValues{{Name: "id", Value: "client-a"}})

	if err := api.tunnelClient(ctx); err != nil {
		t.Fatal(err)
	}
	if dialCalls != 0 {
		t.Fatalf("cross-origin WebSocket rejection triggered %d outbound dials", dialCalls)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestCreateExitRuleHandlerUsesAuthenticatedOwner(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:http-exit-rule?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("http-exit-owner").SaveX(t.Context())
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SaveX(t.Context())
	box, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := tailnet.NewManager(db, box, tailnet.NewTargetPolicy(nil), tailnet.NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{tailnet: manager}
	request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com/api/v1/servers/"+server.ID+"/exit-rules", strings.NewReader(`{"prefix":"10.1.2.3/8","start_port":443,"end_port":443,"enabled":true}`))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set(principalKey, auth.Principal{ID: owner.ID})
	ctx.SetPathValues(echo.PathValues{{Name: "id", Value: server.ID}})

	if err := api.createExitRule(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create exit rule status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if got := db.ExitRule.Query().OnlyX(t.Context()).Prefix; got != "10.0.0.0/8" {
		t.Fatalf("stored prefix = %q, want 10.0.0.0/8", got)
	}
}
