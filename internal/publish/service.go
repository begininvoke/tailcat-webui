package publish

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/publishedroute"
	"github.com/ca-x/tailcat-webui/ent/session"
	"github.com/ca-x/tailcat-webui/ent/tailclient"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"golang.org/x/time/rate"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

const (
	publishedConnectionIdleTimeout = 5 * time.Minute
	maxActiveBySource              = 16
	maxActiveByRouteSource         = 4
	sourceRequestRate              = rate.Limit(20)
	sourceRequestBurst             = 40
	globalSourceRequestRate        = rate.Limit(50)
	globalSourceRequestBurst       = 100
	sourceRateRetention            = 10 * time.Minute
	sourceRateCleanupInterval      = time.Minute
	maxTrackedSourceRates          = 4096
)

type RouteView struct {
	ID             string    `json:"id"`
	ClientID       string    `json:"client_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	RemotePort     uint16    `json:"remote_port"`
	BasePath       string    `json:"base_path"`
	Access         string    `json:"access"`
	AllowedMethods []string  `json:"allowed_methods"`
	Enabled        bool      `json:"enabled"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateRouteInput struct {
	ClientID       string
	Name           string
	Slug           string
	RemotePort     uint16
	BasePath       string
	Access         string
	AllowedMethods []string
}

type PortDialer interface {
	DialPort(context.Context, string, string, uint16) (net.Conn, error)
}

type sourceRateState struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	element  *list.Element
}

type Service struct {
	db                  *ent.Client
	dialer              PortDialer
	baseURL             *url.URL
	managementURL       *url.URL
	grantKey            []byte
	sessionIdle         time.Duration
	logger              *slog.Logger
	slots               chan struct{}
	quotaMu             sync.Mutex
	activeMu            sync.Mutex
	activeByOwner       map[string]int
	activeByRoute       map[string]int
	activeBySource      map[string]int
	activeByRouteSource map[string]int
	sourceRates         map[string]*sourceRateState
	sourceRateLRU       *list.List
	lastRateCleanup     time.Time
	activeCancels       map[string]map[uint64]context.CancelFunc
	deleting            map[string]bool
	nextActive          uint64
}

func NewService(db *ent.Client, dialer PortDialer, managementURL, publishURL *url.URL, grantKey []byte, sessionIdle time.Duration, logger *slog.Logger) (*Service, error) {
	if db == nil || dialer == nil || managementURL == nil || publishURL == nil {
		return nil, errors.New("publish service: nil dependency")
	}
	if len(grantKey) == 0 {
		grantKey = make([]byte, 32)
		if _, err := rand.Read(grantKey); err != nil {
			return nil, fmt.Errorf("publish service: generate grant key: %w", err)
		}
	}
	return &Service{db: db, dialer: dialer, baseURL: publishURL.Clone(), managementURL: managementURL.Clone(), grantKey: append([]byte(nil), grantKey...), sessionIdle: sessionIdle, logger: logger, slots: make(chan struct{}, 128), activeByOwner: make(map[string]int), activeByRoute: make(map[string]int), activeBySource: make(map[string]int), activeByRouteSource: make(map[string]int), sourceRates: make(map[string]*sourceRateState), sourceRateLRU: list.New(), activeCancels: make(map[string]map[uint64]context.CancelFunc), deleting: make(map[string]bool)}, nil
}

func (s *Service) List(ctx context.Context, userID string) ([]RouteView, error) {
	rows, err := s.db.PublishedRoute.Query().Where(publishedroute.UserIDEQ(userID)).Order(ent.Desc(publishedroute.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list published routes: %w", err)
	}
	views := make([]RouteView, 0, len(rows))
	for _, row := range rows {
		views = append(views, s.view(row))
	}
	return views, nil
}

func (s *Service) Create(ctx context.Context, userID string, in CreateRouteInput) (RouteView, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.BasePath = strings.TrimSpace(in.BasePath)
	if in.BasePath == "" {
		in.BasePath = "/"
	}
	if in.Name == "" || !slugPattern.MatchString(in.Slug) || in.RemotePort == 0 || !strings.HasPrefix(in.BasePath, "/") || strings.HasPrefix(in.BasePath, "//") {
		return RouteView{}, tailnet.ErrInvalid
	}
	if len(in.Name) > 80 || len(in.BasePath) > 1024 || (in.Access != "private" && in.Access != "public") {
		return RouteView{}, tailnet.ErrInvalid
	}
	methods, err := validateMethods(in.AllowedMethods)
	if err != nil {
		return RouteView{}, err
	}
	if !slices.Contains(methods, http.MethodGet) {
		return RouteView{}, tailnet.ErrInvalid
	}
	if _, err := s.db.TailClient.Query().Where(tailclient.IDEQ(in.ClientID), tailclient.UserIDEQ(userID)).Only(ctx); ent.IsNotFound(err) {
		return RouteView{}, tailnet.ErrNotFound
	} else if err != nil {
		return RouteView{}, err
	}
	s.quotaMu.Lock()
	count, countErr := s.db.PublishedRoute.Query().Where(publishedroute.UserIDEQ(userID)).Count(ctx)
	if countErr != nil {
		s.quotaMu.Unlock()
		return RouteView{}, fmt.Errorf("count published routes: %w", countErr)
	}
	if count >= 256 {
		s.quotaMu.Unlock()
		return RouteView{}, tailnet.ErrCapacity
	}
	row, err := s.db.PublishedRoute.Create().SetUserID(userID).SetClientID(in.ClientID).SetName(in.Name).SetSlug(in.Slug).SetRemotePort(in.RemotePort).SetBasePath(in.BasePath).SetAccess(publishedroute.Access(in.Access)).SetAllowedMethods(methods).Save(ctx)
	s.quotaMu.Unlock()
	if err != nil {
		if ent.IsConstraintError(err) {
			return RouteView{}, tailnet.ErrConflict
		}
		return RouteView{}, fmt.Errorf("create published route: %w", err)
	}
	return s.view(row), nil
}

func (s *Service) Delete(ctx context.Context, userID, id string) error {
	s.activeMu.Lock()
	s.deleting[id] = true
	s.activeMu.Unlock()
	count, err := s.db.PublishedRoute.Delete().Where(publishedroute.IDEQ(id), publishedroute.UserIDEQ(userID)).Exec(ctx)
	if err != nil {
		s.activeMu.Lock()
		delete(s.deleting, id)
		s.activeMu.Unlock()
		return fmt.Errorf("delete published route: %w", err)
	}
	if count == 0 {
		s.activeMu.Lock()
		delete(s.deleting, id)
		s.activeMu.Unlock()
		return tailnet.ErrNotFound
	}
	s.activeMu.Lock()
	for _, cancel := range s.activeCancels[id] {
		cancel()
	}
	delete(s.deleting, id)
	s.activeMu.Unlock()
	return nil
}

func (s *Service) OpenURL(ctx context.Context, userID, id, rawSessionToken string) (string, error) {
	row, err := s.db.PublishedRoute.Query().Where(publishedroute.IDEQ(id), publishedroute.UserIDEQ(userID), publishedroute.Enabled(true)).Only(ctx)
	if ent.IsNotFound(err) {
		return "", tailnet.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load published route: %w", err)
	}
	target := s.routeURL(row)
	if row.Access == publishedroute.AccessPublic {
		return target.String(), nil
	}
	if rawSessionToken == "" {
		return "", tailnet.ErrNotFound
	}
	sessionHash := hashToken(rawSessionToken)
	query := target.Query()
	query.Set("_tailcat_grant", s.signGrant(row.ID, sessionHash, time.Now().Add(time.Minute), "open"))
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func (s *Service) Proxy(w http.ResponseWriter, r *http.Request, slug, remainder, source string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	}
	if !slugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}
	source = normalizeSource(source)
	if !s.allowRate("source\x00"+source, time.Now(), globalSourceRequestRate, globalSourceRequestBurst) {
		http.Error(w, "Published route request rate exceeded", http.StatusTooManyRequests)
		return
	}
	row, err := s.db.PublishedRoute.Query().Where(publishedroute.SlugEQ(slug), publishedroute.Enabled(true)).Only(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(r.Host, s.routeURL(row).Host) {
		http.NotFound(w, r)
		return
	}
	if !s.allowRate("route\x00"+row.ID+"\x00"+source, time.Now(), sourceRequestRate, sourceRequestBurst) {
		http.Error(w, "Published route request rate exceeded", http.StatusTooManyRequests)
		return
	}
	if !slices.Contains(row.AllowedMethods, r.Method) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if row.Access == publishedroute.AccessPrivate {
		grant := r.URL.Query().Get("_tailcat_grant")
		if sessionHash, valid := s.verifyGrant(grant, row.ID, "open"); grant != "" && valid && s.sessionActive(r.Context(), sessionHash) {
			http.SetCookie(w, &http.Cookie{Name: accessCookieName(row.ID), Value: s.signGrant(row.ID, sessionHash, time.Now().Add(8*time.Hour), "session"), Path: "/r/" + row.Slug, HttpOnly: true, Secure: s.baseURL.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: int((8 * time.Hour).Seconds())})
			clean := r.URL.Clone()
			query := clean.Query()
			query.Del("_tailcat_grant")
			clean.RawQuery = query.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
			return
		}
		cookie, cookieErr := r.Cookie(accessCookieName(row.ID))
		sessionHash, valid := "", false
		if cookieErr == nil {
			sessionHash, valid = s.verifyGrant(cookie.Value, row.ID, "session")
		}
		if cookieErr != nil || !valid || !s.sessionActive(r.Context(), sessionHash) {
			http.NotFound(w, r)
			return
		}
	}
	proxyCtx, release, ok := s.acquire(r.Context(), row.UserID, row.ID, source)
	if !ok {
		http.Error(w, "Published route is at capacity", http.StatusServiceUnavailable)
		return
	}
	defer release()
	r = r.WithContext(proxyCtx)
	stillEnabled, checkErr := s.db.PublishedRoute.Query().Where(publishedroute.IDEQ(row.ID), publishedroute.Enabled(true)).Exist(r.Context())
	if checkErr != nil || !stillEnabled {
		http.NotFound(w, r)
		return
	}
	s.logger.InfoContext(r.Context(), "Published route access", "route_id", row.ID, "access", row.Access, "method", r.Method)
	target := &url.URL{Scheme: "http", Host: "server.tailcat", Path: joinPath(row.BasePath, remainder)}
	cookiePrefix := "tc_" + strings.ReplaceAll(row.ID, "-", "") + "_"
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, dialErr := s.dialer.DialPort(ctx, row.UserID, row.ClientID, row.RemotePort)
			if dialErr != nil {
				return nil, dialErr
			}
			return newActivityConn(connection, publishedConnectionIdleTimeout), nil
		},
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL = target.Clone()
			request.Out.URL.RawQuery = request.In.URL.RawQuery
			request.SetXForwarded()
			request.Out.Host = "server.tailcat"
			request.Out.Header.Set("X-Tailcat-Route", row.Slug)
			isolateRouteCookies(request.Out, cookiePrefix)
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Alt-Svc")
			response.Header.Del("Service-Worker-Allowed")
			scopeCookies(response, "/r/"+row.Slug, cookiePrefix)
			rewriteLocation(response, row.BasePath, "/r/"+row.Slug)
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
			s.logger.WarnContext(request.Context(), "Published route failed", "route_id", row.ID, "error", proxyErr)
			http.Error(writer, "Tailcat resource is unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) view(row *ent.PublishedRoute) RouteView {
	path := s.routeURL(row).String()
	if row.Access == publishedroute.AccessPrivate {
		path = s.managementURL.JoinPath("/api/v1/routes/" + row.ID + "/open").String()
	}
	return RouteView{ID: row.ID, ClientID: row.ClientID, Name: row.Name, Slug: row.Slug, RemotePort: row.RemotePort, BasePath: row.BasePath, Access: string(row.Access), AllowedMethods: row.AllowedMethods, Enabled: row.Enabled, URL: path, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func validateMethods(methods []string) ([]string, error) {
	if len(methods) == 0 {
		return []string{http.MethodGet, http.MethodHead}, nil
	}
	allowed := map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true}
	result := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if !allowed[method] {
			return nil, tailnet.ErrInvalid
		}
		result = append(result, method)
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func (s *Service) routeURL(row *ent.PublishedRoute) *url.URL {
	target := s.baseURL.Clone()
	host := strings.ReplaceAll(row.ID, "-", "") + "." + s.baseURL.Hostname()
	if port := s.baseURL.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	target.Host = host
	target.Path = "/r/" + row.Slug
	return target
}

func (s *Service) signGrant(routeID, sessionHash string, expiry time.Time, purpose string) string {
	payload := fmt.Sprintf("%d.%s.%s.%s", expiry.Unix(), routeID, purpose, sessionHash)
	mac := hmac.New(sha256.New, s.grantKey)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyGrant(token, routeID, purpose string) (string, bool) {
	payloadPart, signaturePart, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return "", false
	}
	fields := strings.Split(string(payload), ".")
	if len(fields) != 4 || fields[1] != routeID || fields[2] != purpose || len(fields[3]) != 64 {
		return "", false
	}
	expiry, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || time.Now().After(time.Unix(expiry, 0)) {
		return "", false
	}
	mac := hmac.New(sha256.New, s.grantKey)
	_, _ = mac.Write(payload)
	return fields[3], hmac.Equal(signature, mac.Sum(nil))
}

func (s *Service) sessionActive(ctx context.Context, tokenHash string) bool {
	now := time.Now()
	active, err := s.db.Session.Query().Where(session.TokenHashEQ(tokenHash), session.ExpiresAtGT(now), session.LastSeenAtGT(now.Add(-s.sessionIdle))).Exist(ctx)
	return err == nil && active
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func accessCookieName(routeID string) string {
	return "tailcat_access_" + strings.ReplaceAll(routeID, "-", "")
}

func (s *Service) acquire(ctx context.Context, ownerID, routeID, source string) (context.Context, func(), bool) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	source = normalizeSource(source)
	routeSource := routeID + "\x00" + source
	if s.deleting[routeID] || s.activeByOwner[ownerID] >= 32 || s.activeByRoute[routeID] >= 16 || s.activeBySource[source] >= maxActiveBySource || s.activeByRouteSource[routeSource] >= maxActiveByRouteSource {
		return nil, nil, false
	}
	select {
	case s.slots <- struct{}{}:
		s.nextActive++
		activeID := s.nextActive
		requestCtx, cancel := context.WithCancel(ctx)
		if s.activeCancels[routeID] == nil {
			s.activeCancels[routeID] = make(map[uint64]context.CancelFunc)
		}
		s.activeCancels[routeID][activeID] = cancel
		s.activeByOwner[ownerID]++
		s.activeByRoute[routeID]++
		s.activeBySource[source]++
		s.activeByRouteSource[routeSource]++
		return requestCtx, sync.OnceFunc(func() { s.release(ownerID, routeID, source, routeSource, activeID, cancel) }), true
	default:
		return nil, nil, false
	}
}

func (s *Service) allowRate(key string, now time.Time, requestRate rate.Limit, burst int) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.sourceRates == nil {
		s.sourceRates = make(map[string]*sourceRateState)
	}
	if s.sourceRateLRU == nil {
		s.sourceRateLRU = list.New()
	}
	if s.lastRateCleanup.IsZero() || now.Sub(s.lastRateCleanup) >= sourceRateCleanupInterval {
		for element := s.sourceRateLRU.Back(); element != nil; element = s.sourceRateLRU.Back() {
			oldestKey := element.Value.(string)
			state := s.sourceRates[oldestKey]
			if now.Sub(state.lastSeen) < sourceRateRetention {
				break
			}
			delete(s.sourceRates, oldestKey)
			s.sourceRateLRU.Remove(element)
		}
		s.lastRateCleanup = now
	}
	state := s.sourceRates[key]
	if state == nil {
		if len(s.sourceRates) >= maxTrackedSourceRates {
			oldest := s.sourceRateLRU.Back()
			delete(s.sourceRates, oldest.Value.(string))
			s.sourceRateLRU.Remove(oldest)
		}
		state = &sourceRateState{limiter: rate.NewLimiter(requestRate, burst)}
		state.element = s.sourceRateLRU.PushFront(key)
		s.sourceRates[key] = state
	} else {
		s.sourceRateLRU.MoveToFront(state.element)
	}
	state.lastSeen = now
	return state.limiter.AllowN(now, 1)
}

func normalizeSource(source string) string {
	if source = strings.TrimSpace(source); source != "" {
		return source
	}
	return "unknown"
}

func (s *Service) release(ownerID, routeID, source, routeSource string, activeID uint64, cancel context.CancelFunc) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	cancel()
	<-s.slots
	delete(s.activeCancels[routeID], activeID)
	if len(s.activeCancels[routeID]) == 0 {
		delete(s.activeCancels, routeID)
	}
	if s.activeByOwner[ownerID] <= 1 {
		delete(s.activeByOwner, ownerID)
	} else {
		s.activeByOwner[ownerID]--
	}
	if s.activeByRoute[routeID] <= 1 {
		delete(s.activeByRoute, routeID)
	} else {
		s.activeByRoute[routeID]--
	}
	if s.activeBySource[source] <= 1 {
		delete(s.activeBySource, source)
	} else {
		s.activeBySource[source]--
	}
	if s.activeByRouteSource[routeSource] <= 1 {
		delete(s.activeByRouteSource, routeSource)
	} else {
		s.activeByRouteSource[routeSource]--
	}
}

type activityConn struct {
	net.Conn
	idleTimeout time.Duration
}

func newActivityConn(connection net.Conn, idleTimeout time.Duration) net.Conn {
	wrapped := &activityConn{Conn: connection, idleTimeout: idleTimeout}
	wrapped.refreshDeadline()
	return wrapped
}

func (c *activityConn) Read(buffer []byte) (int, error) {
	c.refreshDeadline()
	read, err := c.Conn.Read(buffer)
	if read > 0 {
		c.refreshDeadline()
	}
	return read, err
}

func (c *activityConn) Write(buffer []byte) (int, error) {
	c.refreshDeadline()
	written, err := c.Conn.Write(buffer)
	if written > 0 {
		c.refreshDeadline()
	}
	return written, err
}

func (c *activityConn) refreshDeadline() {
	_ = c.Conn.SetDeadline(time.Now().Add(c.idleTimeout))
}

func joinPath(base, remainder string) string {
	trailingSlash := strings.HasSuffix(remainder, "/")
	base = path.Clean("/" + strings.Trim(base, "/"))
	remainder = strings.TrimPrefix(path.Clean("/"+strings.TrimLeft(remainder, "/")), "/")
	joined := path.Join(base, remainder)
	if trailingSlash && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

func isolateRouteCookies(request *http.Request, prefix string) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		name, ok := strings.CutPrefix(cookie.Name, prefix)
		if ok && name != "" {
			copy := *cookie
			copy.Name = name
			request.AddCookie(&copy)
		}
	}
}

func scopeCookies(response *http.Response, routePath, prefix string) {
	cookies := response.Cookies()
	if len(cookies) == 0 {
		return
	}
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		cookie.Name = prefix + cookie.Name
		cookie.Path = routePath
		cookie.Domain = ""
		response.Header.Add("Set-Cookie", cookie.String())
	}
}

func rewriteLocation(response *http.Response, basePath, routePath string) {
	raw := response.Header.Get("Location")
	if raw == "" || response.Request == nil || response.Request.URL == nil {
		return
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return
	}
	resolved := response.Request.URL.ResolveReference(reference)
	if resolved.Host != "" && !strings.EqualFold(resolved.Host, "server.tailcat") {
		return
	}
	basePath = path.Clean("/" + strings.Trim(basePath, "/"))
	remainder := strings.TrimPrefix(resolved.Path, basePath)
	resolved.Scheme = ""
	resolved.Host = ""
	resolved.Path = joinPath(routePath, remainder)
	resolved.RawPath = ""
	response.Header.Set("Location", resolved.String())
}
