package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/labstack/echo/v5"

	_ "github.com/lib-x/entsqlite"
	"tailscale.com/types/key"
)

type exitPolicyTestRuntime struct {
	events []string
}

func (r *exitPolicyTestRuntime) Start() error {
	r.events = append(r.events, "start")
	return nil
}

func (r *exitPolicyTestRuntime) Close() error {
	r.events = append(r.events, "close")
	return nil
}

func (r *exitPolicyTestRuntime) DrainTCP(context.Context) error {
	r.events = append(r.events, "drain")
	return nil
}

func (*exitPolicyTestRuntime) ConnectionToken() string { return "fake-token" }
func (*exitPolicyTestRuntime) PublicKey() string       { return "nodekey:fake-server" }
func (*exitPolicyTestRuntime) AddAllowedClient(key.NodePublic) {
}

type exitPolicyTestFactory struct {
	server *exitPolicyTestRuntime
}

func (f exitPolicyTestFactory) NewServer(context.Context, tailnet.ServerSpec) (tailnet.ServerRuntime, error) {
	return f.server, nil
}

func (exitPolicyTestFactory) NewClient(context.Context, tailnet.ClientSpec) (tailnet.ClientRuntime, error) {
	return nil, nil
}

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

func TestCreateExitRuleHandlerRequiresEnabledBeforeMutation(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:http-exit-rule-required?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("http-exit-required-owner").SaveX(t.Context())
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SetRegion("tailcat.dev").SaveX(t.Context())
	runtime := new(exitPolicyTestRuntime)
	box, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := tailnet.NewManager(db, box, tailnet.NewTargetPolicy(nil), tailnet.NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), exitPolicyTestFactory{server: runtime})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.StartServer(t.Context(), owner.ID, server.ID); err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	api := &API{tailnet: manager}

	err = api.createExitRule(exitPolicyTestContext(t, owner.ID, server.ID, `{}`))
	requireEnabledFieldError(t, err)
	if got := db.ExitRule.Query().CountX(t.Context()); got != 0 {
		t.Fatalf("exit rules after missing enabled = %d, want 0", got)
	}
	if !db.TailServer.GetX(t.Context(), server.ID).DesiredRunning || !slices.Equal(runtime.events, []string{"start"}) {
		t.Fatalf("missing enabled mutated runtime: desired_running=%t events=%v", db.TailServer.GetX(t.Context(), server.ID).DesiredRunning, runtime.events)
	}

	ctx := exitPolicyTestContext(t, owner.ID, server.ID, `{"prefix":"10.0.0.0/8","start_port":443,"end_port":443,"enabled":false}`)
	if err := api.createExitRule(ctx); err != nil {
		t.Fatalf("explicit false: %v", err)
	}
	rule := db.ExitRule.Query().OnlyX(t.Context())
	if rule.Enabled || !slices.Equal(runtime.events, []string{"start", "drain", "close"}) {
		t.Fatalf("explicit false result: enabled=%t events=%v", rule.Enabled, runtime.events)
	}
}

func TestSetExitNodeHandlerRequiresEnabledBeforeMutation(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:http-exit-node-required?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("http-exit-node-required-owner").SaveX(t.Context())
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("server").SetRegion("tailcat.dev").SetExitNodeEnabled(true).SaveX(t.Context())
	db.ExitRule.Create().SetUserID(owner.ID).SetServerID(server.ID).SetPrefix("10.0.0.0/8").SetStartPort(443).SetEndPort(443).SaveX(t.Context())
	runtime := new(exitPolicyTestRuntime)
	box, err := secrets.NewBox(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := tailnet.NewManager(db, box, tailnet.NewTargetPolicy(nil), tailnet.NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), exitPolicyTestFactory{server: runtime})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.StartServer(t.Context(), owner.ID, server.ID); err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	api := &API{tailnet: manager}

	err = api.setExitNodeEnabled(exitPolicyTestContext(t, owner.ID, server.ID, `{}`))
	requireEnabledFieldError(t, err)
	row := db.TailServer.GetX(t.Context(), server.ID)
	if !row.ExitNodeEnabled || !row.DesiredRunning || !slices.Equal(runtime.events, []string{"start"}) {
		t.Fatalf("missing enabled mutated state: exit=%t desired_running=%t events=%v", row.ExitNodeEnabled, row.DesiredRunning, runtime.events)
	}

	ctx := exitPolicyTestContext(t, owner.ID, server.ID, `{"enabled":false}`)
	if err := api.setExitNodeEnabled(ctx); err != nil {
		t.Fatalf("explicit false: %v", err)
	}
	row = db.TailServer.GetX(t.Context(), server.ID)
	if row.ExitNodeEnabled || row.DesiredRunning || !slices.Equal(runtime.events, []string{"start", "drain", "close"}) {
		t.Fatalf("explicit false result: exit=%t desired_running=%t events=%v", row.ExitNodeEnabled, row.DesiredRunning, runtime.events)
	}
}

func exitPolicyTestContext(t *testing.T, ownerID, serverID, body string) *echo.Context {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://tailcat.example.com", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := echo.New().NewContext(request, httptest.NewRecorder())
	ctx.Set(principalKey, auth.Principal{ID: ownerID})
	ctx.SetPathValues(echo.PathValues{{Name: "id", Value: serverID}})
	return ctx
}

func requireEnabledFieldError(t *testing.T, err error) {
	t.Helper()
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusUnprocessableEntity || apiErr.Code != "VALIDATION_ERROR" || apiErr.Message != "The enabled field is required" {
		t.Fatalf("missing enabled error = %#v", err)
	}
}
