package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/labstack/echo/v5"
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
