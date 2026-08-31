package httpapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/publish"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/ca-x/tailcat-webui/internal/version"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"golang.org/x/time/rate"
)

const sessionCookie = "tailcat_session"

type API struct {
	db             *ent.Client
	auth           *auth.Service
	audit          *audit.Service
	tailnet        *tailnet.Manager
	publish        *publish.Service
	cfg            config.Config
	logger         *slog.Logger
	web            fs.FS
	startedAt      time.Time
	tunnelMu       sync.Mutex
	tunnels        map[string]int
	mutationMu     sync.Mutex
	mutationRates  map[string]*rate.Limiter
	mutationActive map[string]int
	mutationSlots  chan struct{}
	eventMu        sync.Mutex
	eventStreams   map[string]int
	tunnelDial     func(context.Context, string, string, string) (net.Conn, error)
}

func New(db *ent.Client, authService *auth.Service, auditService *audit.Service, manager *tailnet.Manager, publisher *publish.Service, cfg config.Config, logger *slog.Logger, web fs.FS) (*API, error) {
	if db == nil || authService == nil || auditService == nil || manager == nil || publisher == nil || logger == nil || web == nil {
		return nil, errors.New("HTTP API: nil dependency")
	}
	return &API{db: db, auth: authService, audit: auditService, tailnet: manager, publish: publisher, cfg: cfg, logger: logger, web: web, startedAt: time.Now(), tunnels: make(map[string]int), mutationRates: make(map[string]*rate.Limiter), mutationActive: make(map[string]int), mutationSlots: make(chan struct{}, 64), eventStreams: make(map[string]int), tunnelDial: manager.Dial}, nil
}

func (a *API) Handler() (http.Handler, error) {
	e := echo.NewWithConfig(echo.Config{JSONSerializer: jsonV2Serializer{}, Logger: a.logger})
	e.IPExtractor = trustedIPExtractor(a.cfg)
	e.HTTPErrorHandler = errorHandler
	e.Pre(a.enforceOriginBoundary)
	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{
		LimitBytes: 1 << 20,
		Skipper:    func(c *echo.Context) bool { return strings.HasPrefix(c.Request().URL.Path, "/r/") },
	}))
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		HandleError: true, LogRequestID: true, LogMethod: true, LogURIPath: true, LogStatus: true, LogLatency: true,
		LogValuesFunc: func(c *echo.Context, values middleware.RequestLoggerValues) error {
			a.logger.InfoContext(c.Request().Context(), "HTTP request", "request_id", values.RequestID, "method", values.Method, "path", values.URIPath, "status", values.Status, "latency", values.Latency)
			return nil
		},
	}))
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		Skipper:               func(c *echo.Context) bool { return strings.HasPrefix(c.Request().URL.Path, "/r/") },
		XFrameOptions:         "DENY",
		ContentTypeNosniff:    "nosniff",
		HSTSMaxAge:            hstsMaxAge(a.cfg),
		ContentSecurityPolicy: "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'",
		ReferrerPolicy:        "same-origin",
	}))

	e.GET("/api/v1/health", a.health)
	e.GET("/api/v1/config", a.publicConfig)

	authLimiter := middleware.RateLimiter(middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{Rate: 10.0 / (15 * 60), Burst: 10, ExpiresIn: 30 * time.Minute}))
	e.GET("/api/v1/auth/login", a.login, authLimiter)
	e.GET("/api/v1/auth/callback", a.callback, authLimiter)
	e.POST("/api/v1/auth/demo", a.demoLogin, authLimiter)
	e.POST("/api/v1/auth/logout", a.logout, a.optionalPrincipal)

	api := e.Group("/api/v1", a.requirePrincipal)
	api.Use(a.mutationRateLimit)
	api.Use(a.auditMutations)
	api.GET("/auth/me", a.me)
	api.GET("/dashboard", a.dashboard)
	api.GET("/servers", a.listServers)
	api.POST("/servers", a.createServer)
	api.POST("/servers/:id/start", a.startServer)
	api.POST("/servers/:id/stop", a.stopServer)
	api.POST("/servers/:id/exit-node", a.setExitNodeEnabled)
	api.DELETE("/servers/:id", a.deleteServer)
	api.GET("/servers/:id/exit-rules", a.listExitRules)
	api.POST("/servers/:id/exit-rules", a.createExitRule)
	api.DELETE("/exit-rules/:id", a.deleteExitRule)
	api.GET("/servers/:id/mappings", a.listMappings)
	api.POST("/servers/:id/mappings", a.createMapping)
	api.DELETE("/mappings/:id", a.deleteMapping)
	api.GET("/servers/:id/allowed-clients", a.listAllowedClients)
	api.POST("/servers/:id/allowed-clients", a.createAllowedClient)
	api.DELETE("/allowed-clients/:id", a.deleteAllowedClient)
	api.GET("/clients", a.listClients)
	api.POST("/clients", a.createClient)
	api.POST("/clients/:id/ping", a.pingClient)
	api.GET("/clients/:id/tunnel", a.tunnelClient)
	api.DELETE("/clients/:id", a.deleteClient)
	api.POST("/tokens/parse", a.parseToken)
	api.POST("/tokens/resolve", a.resolveToken)
	api.GET("/routes", a.listRoutes)
	api.POST("/routes", a.createRoute)
	api.GET("/routes/:id/open", a.openRoute)
	api.DELETE("/routes/:id", a.deleteRoute)
	api.GET("/events", a.events)

	e.Any("/r/:slug", a.handlePublished)
	e.Any("/r/:slug/*", a.handlePublished)
	e.RouteNotFound("/*", a.spa)
	return e, nil
}

func (a *API) enforceOriginBoundary(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		isPublishedPath := c.Request().URL.Path == "/r" || strings.HasPrefix(c.Request().URL.Path, "/r/")
		isPublishedHost := isPublishedRouteHost(c.Request().Host, a.cfg)
		isManagementHost := strings.EqualFold(c.Request().Host, a.cfg.BaseURL.Host)
		if (isPublishedPath && !isPublishedHost) || (!isPublishedPath && !isManagementHost) {
			return echo.ErrNotFound
		}
		return next(c)
	}
}

func isPublishedRouteHost(authority string, cfg config.Config) bool {
	hostname := authority
	port := ""
	if host, parsedPort, err := net.SplitHostPort(authority); err == nil {
		hostname, port = host, parsedPort
	}
	if port != cfg.PublishURL.Port() {
		return false
	}
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	base := strings.ToLower(strings.TrimSuffix(cfg.PublishURL.Hostname(), "."))
	prefix, ok := strings.CutSuffix(hostname, "."+base)
	return ok && prefix != "" && !strings.Contains(prefix, ".")
}

func (a *API) auditMutations(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		method := c.Request().Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return next(c)
		}
		err := next(c)
		p, principalErr := principal(c)
		if principalErr == nil {
			outcome := "success"
			if err != nil {
				outcome = "failure"
			}
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 2*time.Second)
			defer cancel()
			if auditErr := a.audit.Record(auditCtx, audit.Entry{UserID: p.ID, Action: method + " " + c.Path(), ResourceKind: c.Path(), ResourceID: c.Param("id"), Outcome: outcome, RequestID: c.Response().Header().Get(echo.HeaderXRequestID)}); auditErr != nil {
				a.logger.ErrorContext(auditCtx, "Write audit event failed", "error", auditErr)
			}
		}
		return err
	}
}

func (a *API) mutationRateLimit(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		method := c.Request().Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return next(c)
		}
		p, err := principal(c)
		if err != nil {
			return err
		}
		a.mutationMu.Lock()
		limiter := a.mutationRates[p.ID]
		if limiter == nil {
			limiter = rate.NewLimiter(2, 20)
			a.mutationRates[p.ID] = limiter
		}
		allowed := limiter.Allow()
		if allowed && a.mutationActive[p.ID] < 8 {
			select {
			case a.mutationSlots <- struct{}{}:
				a.mutationActive[p.ID]++
			default:
				allowed = false
			}
		} else if allowed {
			allowed = false
		}
		a.mutationMu.Unlock()
		if !allowed {
			return &APIError{Status: http.StatusTooManyRequests, Code: "RATE_LIMITED", Message: "Too many management changes; try again shortly"}
		}
		defer func() {
			a.mutationMu.Lock()
			<-a.mutationSlots
			if a.mutationActive[p.ID] <= 1 {
				delete(a.mutationActive, p.ID)
			} else {
				a.mutationActive[p.ID]--
			}
			a.mutationMu.Unlock()
		}()
		controller := http.NewResponseController(c.Response())
		_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		return next(c)
	}
}

func (a *API) health(c *echo.Context) error {
	if _, err := a.db.User.Query().Limit(1).Exist(c.Request().Context()); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "version": version.Version})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "version": version.Version, "uptime_seconds": int64(time.Since(a.startedAt).Seconds())})
}

func (a *API) publicConfig(c *echo.Context) error {
	mode := "oidc"
	if a.cfg.DemoMode {
		mode = "demo"
	}
	return c.JSON(http.StatusOK, map[string]any{"auth_mode": mode, "unsafe_ssh": a.cfg.UnsafeSSH, "version": version.Version})
}

func (a *API) login(c *echo.Context) error {
	redirect, err := a.auth.StartLogin(c.Request().Context(), c.QueryParamOr("return_to", "/"))
	if err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, redirect)
}

func (a *API) callback(c *echo.Context) error {
	p, token, returnTo, err := a.auth.CompleteLogin(c.Request().Context(), c.QueryParam("state"), c.QueryParam("code"))
	if err != nil {
		a.recordAudit(c, audit.Entry{Action: "auth.callback", Outcome: "failure"})
		return err
	}
	a.recordAudit(c, audit.Entry{UserID: p.ID, Action: "auth.callback", ResourceKind: "session", Outcome: "success"})
	a.setSessionCookie(c, token)
	return c.Redirect(http.StatusFound, returnTo)
}

func (a *API) demoLogin(c *echo.Context) error {
	principal, token, err := a.auth.DemoLogin(c.Request().Context())
	if err != nil {
		return err
	}
	a.recordAudit(c, audit.Entry{UserID: principal.ID, Action: "auth.demo", ResourceKind: "session", Outcome: "success"})
	a.setSessionCookie(c, token)
	return c.JSON(http.StatusOK, principal)
}

func (a *API) logout(c *echo.Context) error {
	p, _ := principal(c)
	if err := a.auth.Logout(c.Request().Context(), sessionToken(c)); err != nil {
		return err
	}
	a.recordAudit(c, audit.Entry{UserID: p.ID, Action: "auth.logout", ResourceKind: "session", Outcome: "success"})
	c.SetCookie(&http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	return c.NoContent(http.StatusNoContent)
}

func (a *API) me(c *echo.Context) error {
	principal, err := principal(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, principal)
}

func (a *API) dashboard(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	servers, err := a.tailnet.ListServers(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	clients, err := a.tailnet.ListClients(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	routes, err := a.publish.List(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	running, reachable, publicRoutes := 0, 0, 0
	for _, server := range servers {
		if server.RuntimeState == "running" {
			running++
		}
	}
	for _, client := range clients {
		if client.RuntimeState == "ready" && client.LastPingAt != nil && time.Since(*client.LastPingAt) < 5*time.Minute {
			reachable++
		}
	}
	for _, route := range routes {
		if route.Access == "public" {
			publicRoutes++
		}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"servers":        map[string]int{"total": len(servers), "running": running},
		"clients":        map[string]int{"total": len(clients), "reachable": reachable},
		"routes":         map[string]int{"total": len(routes), "public": publicRoutes},
		"recent_servers": servers[:min(3, len(servers))],
		"recent_clients": clients[:min(3, len(clients))],
	})
}

func (a *API) listServers(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.tailnet.ListServers(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createServerRequest struct {
	Name            string `json:"name"`
	KeyMode         string `json:"key_mode"`
	Region          string `json:"region"`
	DERPMapURL      string `json:"derp_map_url"`
	ExitNodeEnabled bool   `json:"exit_node_enabled"`
	Start           bool   `json:"start"`
}

func (a *API) createServer(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createServerRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	view, err := a.tailnet.CreateServer(c.Request().Context(), p.ID, tailnet.CreateServerInput{Name: request.Name, KeyMode: request.KeyMode, Region: request.Region, DERPMapURL: request.DERPMapURL, ExitNodeEnabled: request.ExitNodeEnabled, Start: request.Start})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) startServer(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := a.tailnet.StartServer(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view)
}

func (a *API) stopServer(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.tailnet.StopServer(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) deleteServer(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.tailnet.DeleteServer(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

type setExitNodeRequest struct {
	Enabled *bool `json:"enabled"`
}

func (a *API) setExitNodeEnabled(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request setExitNodeRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	if request.Enabled == nil {
		return badRequest("VALIDATION_ERROR", "The enabled field is required")
	}
	view, err := a.tailnet.SetExitNodeEnabled(c.Request().Context(), p.ID, c.Param("id"), *request.Enabled)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view)
}

func (a *API) listExitRules(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.tailnet.ListExitRules(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createExitRuleRequest struct {
	Prefix    string `json:"prefix"`
	StartPort uint16 `json:"start_port"`
	EndPort   uint16 `json:"end_port"`
	Enabled   *bool  `json:"enabled"`
}

func (a *API) createExitRule(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createExitRuleRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	if request.Enabled == nil {
		return badRequest("VALIDATION_ERROR", "The enabled field is required")
	}
	view, err := a.tailnet.CreateExitRule(c.Request().Context(), p.ID, c.Param("id"), tailnet.CreateExitRuleInput{Prefix: request.Prefix, StartPort: request.StartPort, EndPort: request.EndPort, Enabled: *request.Enabled})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) deleteExitRule(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.tailnet.DeleteExitRule(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listMappings(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.tailnet.ListMappings(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createMappingRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ListenPort uint16 `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort uint16 `json:"target_port"`
}

func (a *API) createMapping(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createMappingRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	view, err := a.tailnet.CreateMapping(c.Request().Context(), p.ID, c.Param("id"), tailnet.CreateMappingInput{Name: request.Name, Kind: request.Kind, ListenPort: request.ListenPort, TargetHost: request.TargetHost, TargetPort: request.TargetPort})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) deleteMapping(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.tailnet.DeleteMapping(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listAllowedClients(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.tailnet.ListAllowedClients(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createAllowedClientRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

func (a *API) createAllowedClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createAllowedClientRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	view, err := a.tailnet.CreateAllowedClient(c.Request().Context(), p.ID, c.Param("id"), request.Name, request.PublicKey)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) deleteAllowedClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.tailnet.DeleteAllowedClient(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listClients(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.tailnet.ListClients(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createClientRequest struct {
	Name         string `json:"name"`
	Server       string `json:"server"`
	DERPMapURL   string `json:"derp_map_url"`
	SaveIdentity bool   `json:"save_identity"`
}

func (a *API) createClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createClientRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	view, err := a.tailnet.CreateClient(c.Request().Context(), p.ID, tailnet.CreateClientInput{Name: request.Name, Server: request.Server, DERPMapURL: request.DERPMapURL, SaveIdentity: request.SaveIdentity})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) pingClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Second)
	defer cancel()
	view, err := a.tailnet.PingClient(ctx, p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view)
}

func (a *API) deleteClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	clientID := c.Param("id")
	if err := a.publish.InvalidateClient(c.Request().Context(), p.ID, clientID); err != nil {
		return err
	}
	defer a.publish.CompleteClientInvalidation(p.ID, clientID)
	if err := a.tailnet.DeleteClient(c.Request().Context(), p.ID, clientID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) tunnelClient(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if !a.acquireTunnel(p.ID) {
		return &APIError{Status: http.StatusTooManyRequests, Code: "TUNNEL_LIMIT", Message: "Too many active tunnels"}
	}
	defer a.releaseTunnel(p.ID)
	tunnelCtx, cancelTunnel := context.WithCancel(c.Request().Context())
	defer cancelTunnel()
	connection, err := websocket.Accept(c.Response(), c.Request(), nil)
	if err != nil {
		return nil
	}
	defer connection.CloseNow()
	address := strings.TrimSpace(c.QueryParamOr("address", "server.tailcat:80"))
	dialCtx, cancel := context.WithTimeout(tunnelCtx, 20*time.Second)
	defer cancel()
	remote, err := a.tunnelDial(dialCtx, p.ID, c.Param("id"), address)
	if err != nil {
		_ = connection.Close(websocket.StatusInternalError, "Tailcat target is unavailable")
		return nil
	}
	stream := websocket.NetConn(tunnelCtx, connection, websocket.MessageBinary)
	tailnet.ProxyConnectionsUntilClosed(tunnelCtx, stream, remote)
	return nil
}

func (a *API) acquireTunnel(userID string) bool {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if a.tunnels[userID] >= 8 {
		return false
	}
	a.tunnels[userID]++
	return true
}

func (a *API) releaseTunnel(userID string) {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if a.tunnels[userID] <= 1 {
		delete(a.tunnels, userID)
		return
	}
	a.tunnels[userID]--
}

type tokenRequest struct {
	Token string `json:"token"`
}

func (a *API) parseToken(c *echo.Context) error {
	var request tokenRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	parsed, err := a.tailnet.ParseToken(request.Token)
	if err != nil {
		return badRequest("INVALID_TOKEN", "The Tailcat token is invalid")
	}
	return c.JSON(http.StatusOK, map[string]any{"parsed": parsed})
}

func (a *API) resolveToken(c *echo.Context) error {
	var request tokenRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Second)
	defer cancel()
	resolved, err := a.tailnet.ResolveToken(ctx, request.Token)
	if err != nil {
		return badRequest("TOKEN_RESOLUTION_FAILED", "The Tailcat token could not be resolved")
	}
	return c.JSON(http.StatusOK, map[string]string{"token": resolved})
}

func (a *API) listRoutes(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	rows, err := a.publish.List(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows})
}

type createRouteRequest struct {
	ClientID       string   `json:"client_id"`
	Name           string   `json:"name"`
	Slug           string   `json:"slug"`
	RemotePort     uint16   `json:"remote_port"`
	BasePath       string   `json:"base_path"`
	Access         string   `json:"access"`
	AllowedMethods []string `json:"allowed_methods"`
}

func (a *API) createRoute(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createRouteRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	view, err := a.publish.Create(c.Request().Context(), p.ID, publish.CreateRouteInput{ClientID: request.ClientID, Name: request.Name, Slug: request.Slug, RemotePort: request.RemotePort, BasePath: request.BasePath, Access: request.Access, AllowedMethods: request.AllowedMethods})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view)
}

func (a *API) deleteRoute(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.publish.Delete(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) openRoute(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	target, err := a.publish.OpenURL(c.Request().Context(), p.ID, c.Param("id"), sessionToken(c))
	if err != nil {
		return err
	}
	a.recordAudit(c, audit.Entry{UserID: p.ID, Action: "route.open", ResourceKind: "route", ResourceID: c.Param("id"), Outcome: "success"})
	return c.Redirect(http.StatusSeeOther, target)
}

func (a *API) recordAudit(c *echo.Context, entry audit.Entry) {
	entry.RequestID = c.Response().Header().Get(echo.HeaderXRequestID)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 2*time.Second)
	defer cancel()
	if err := a.audit.Record(auditCtx, entry); err != nil {
		a.logger.ErrorContext(auditCtx, "Write audit event failed", "error", err)
	}
}

func (a *API) events(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if !a.acquireEventStream(p.ID) {
		return &APIError{Status: http.StatusTooManyRequests, Code: "EVENT_STREAM_LIMIT", Message: "Too many event streams"}
	}
	defer a.releaseEventStream(p.ID)
	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "text/event-stream")
	response.Header().Set(echo.HeaderCacheControl, "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	stream, unsubscribe := a.tailnet.Events(p.ID).Subscribe(16)
	defer unsubscribe()
	for {
		select {
		case event := <-stream:
			data, marshalErr := json.Marshal(&event)
			if marshalErr != nil {
				return marshalErr
			}
			if _, writeErr := fmt.Fprintf(response, "event: runtime\ndata: %s\n\n", data); writeErr != nil {
				return nil
			}
			_ = http.NewResponseController(response).Flush()
		case <-c.Request().Context().Done():
			return nil
		}
	}
}

func (a *API) acquireEventStream(userID string) bool {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if a.eventStreams[userID] >= 8 {
		return false
	}
	a.eventStreams[userID]++
	return true
}

func (a *API) releaseEventStream(userID string) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if a.eventStreams[userID] <= 1 {
		delete(a.eventStreams, userID)
		a.tailnet.ReleaseEvents(userID)
	} else {
		a.eventStreams[userID]--
	}
}

func (a *API) handlePublished(c *echo.Context) error {
	source := "unknown"
	if address, err := netip.ParseAddr(c.RealIP()); err == nil {
		source = address.Unmap().String()
	}
	a.publish.Proxy(c.Response(), c.Request(), c.Param("slug"), c.Param("*"), source)
	return nil
}

func (a *API) spa(c *echo.Context) error {
	path := strings.TrimPrefix(c.Request().URL.Path, "/")
	if path == "api" || path == "r" || strings.HasPrefix(path, "api/") || strings.HasPrefix(path, "r/") {
		return echo.ErrNotFound
	}
	if path != "" {
		if file, err := fs.Stat(a.web, path); err == nil && !file.IsDir() {
			return c.FileFS(path, a.web)
		}
	}
	return c.FileFS("index.html", a.web)
}

func (a *API) setSessionCookie(c *echo.Context, token string) {
	c.SetCookie(&http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode, MaxAge: int(a.cfg.SessionMax.Seconds())})
}

func hstsMaxAge(cfg config.Config) int {
	if cfg.SecureCookies() {
		return 31536000
	}
	return 0
}

func trustedIPExtractor(cfg config.Config) echo.IPExtractor {
	if len(cfg.TrustedProxies) == 0 {
		return echo.ExtractIPDirect()
	}
	options := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustPrivateNet(false), echo.TrustLinkLocal(false)}
	for _, prefix := range cfg.TrustedProxies {
		options = append(options, echo.TrustIPRange(&net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())}))
	}
	return echo.ExtractIPFromXFFHeader(options...)
}

func parsePort(raw string) (uint16, error) {
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || value == 0 {
		return 0, errors.New("invalid port")
	}
	return uint16(value), nil
}
