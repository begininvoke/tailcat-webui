package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"

	_ "github.com/lib-x/entsqlite"
)

func TestJoinPath(t *testing.T) {
	tests := map[string]struct{ base, rest, want string }{
		"root":      {"/", "/api", "/api"},
		"base":      {"/v1", "/users", "/v1/users"},
		"trim":      {"/v1/", "users", "/v1/users"},
		"traversal": {"/v1", "../admin", "/v1/admin"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := joinPath(tt.base, tt.rest); got != tt.want {
				t.Fatalf("joinPath(%q, %q) = %q, want %q", tt.base, tt.rest, got, tt.want)
			}
		})
	}
}

func TestValidateMethodsDefaultsSafeAndRequiresExplicitWrites(t *testing.T) {
	defaults, err := validateMethods(nil)
	if err != nil || !slices.Equal(defaults, []string{"GET", "HEAD"}) {
		t.Fatalf("default methods = %v, err=%v", defaults, err)
	}
	methods, err := validateMethods([]string{"post", "GET", "POST"})
	if err != nil || !slices.Equal(methods, []string{"GET", "POST"}) {
		t.Fatalf("validated methods = %v, err=%v", methods, err)
	}
	if _, err := validateMethods([]string{"CONNECT"}); err == nil {
		t.Fatal("CONNECT was accepted")
	}
}

func TestPrivateRouteSessionRevokesWithManagementSession(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:publish-session?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	user := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	sessionHash := strings.Repeat("b", 64)
	record := db.Session.Create().SetUserID(user.ID).SetTokenHash(sessionHash).SetExpiresAt(time.Now().Add(time.Hour)).SetLastSeenAt(time.Now()).SaveX(t.Context())
	service := &Service{db: db, grantKey: bytes.Repeat([]byte{7}, 32), sessionIdle: time.Hour}
	grant := service.signGrant("route-a", sessionHash, time.Now().Add(time.Minute), "session")
	gotHash, valid := service.verifyGrant(grant, "route-a", "session")
	if !valid || !service.sessionActive(t.Context(), gotHash) {
		t.Fatal("active management session did not authorize private route")
	}
	db.Session.DeleteOneID(record.ID).ExecX(t.Context())
	if service.sessionActive(t.Context(), gotHash) {
		t.Fatal("revoked management session still authorized private route")
	}
}

func TestRouteURLUsesImmutablePerRouteOrigin(t *testing.T) {
	publishURL, _ := url.Parse("https://publish.tailcat.example.com")
	service := &Service{baseURL: publishURL}
	row := &ent.PublishedRoute{ID: "01a04db1-52c0-7a40-932d-ebc36c123a5e", Slug: "studio"}
	got := service.routeURL(row)
	if got.Host != "01a04db152c07a40932debc36c123a5e.publish.tailcat.example.com" || got.Path != "/r/studio" {
		t.Fatalf("route URL = %s", got)
	}
}

func TestRouteGrantIsScopedAndExpires(t *testing.T) {
	service := &Service{grantKey: bytes.Repeat([]byte{9}, 32)}
	sessionHash := strings.Repeat("a", 64)
	token := service.signGrant("route-a", sessionHash, time.Now().Add(time.Minute), "open")
	if got, valid := service.verifyGrant(token, "route-a", "open"); !valid || got != sessionHash {
		t.Fatal("valid route grant was rejected")
	}
	if _, valid := service.verifyGrant(token, "route-b", "open"); valid {
		t.Fatal("route grant escaped its route")
	}
	if _, valid := service.verifyGrant(token, "route-a", "session"); valid {
		t.Fatal("route grant escaped its route or purpose")
	}
	expired := service.signGrant("route-a", sessionHash, time.Now().Add(-time.Second), "open")
	if _, valid := service.verifyGrant(expired, "route-a", "open"); valid {
		t.Fatal("expired route grant was accepted")
	}
}

func TestRouteCookiesAreNamespaced(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/r/studio/", nil)
	request.AddCookie(&http.Cookie{Name: "tailcat_session", Value: "management"})
	request.AddCookie(&http.Cookie{Name: "other", Value: "another-route"})
	request.AddCookie(&http.Cookie{Name: "tc_studio_sid", Value: "remote"})
	isolateRouteCookies(request, "tc_studio_")
	cookies := request.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "sid" || cookies[0].Value != "remote" {
		t.Fatalf("outbound cookies = %#v", cookies)
	}

	response := &http.Response{Header: make(http.Header)}
	response.Header.Add("Set-Cookie", "sid=new; Path=/; Domain=server.tailcat; HttpOnly")
	scopeCookies(response, "/r/studio", "tc_studio_")
	set := response.Cookies()
	if len(set) != 1 || set[0].Name != "tc_studio_sid" || set[0].Path != "/r/studio" || set[0].Domain != "" {
		t.Fatalf("scoped cookies = %#v", set)
	}
}

func TestRewriteLocationKeepsRedirectInsidePublishedRoute(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://server.tailcat/v1/current", nil)
	response := &http.Response{Header: make(http.Header), Request: request}
	response.Header.Set("Location", "/v1/login?next=%2F")
	rewriteLocation(response, "/v1", "/r/studio")
	if got, want := response.Header.Get("Location"), "/r/studio/login?next=%2F"; got != want {
		t.Fatalf("rewritten Location = %q, want %q", got, want)
	}

	response.Header.Set("Location", "https://id.example.com/login")
	rewriteLocation(response, "/v1", "/r/studio")
	if got := response.Header.Get("Location"); got != "https://id.example.com/login" {
		t.Fatalf("external Location was rewritten: %q", got)
	}
}

func TestAcquirePartitionsCapacityBySource(t *testing.T) {
	service := &Service{
		slots:               make(chan struct{}, 128),
		activeByOwner:       make(map[string]int),
		activeByRoute:       make(map[string]int),
		activeBySource:      make(map[string]int),
		activeByRouteSource: make(map[string]int),
		sourceRates:         make(map[string]*sourceRateState),
		activeCancels:       make(map[string]map[uint64]context.CancelFunc),
		routeInvalidations:  make(map[string]int),
	}
	var releases []func()
	for range 4 {
		_, release, ok := service.acquire(t.Context(), "owner-a", "client-a", "route-a", "192.0.2.10")
		if !ok {
			t.Fatal("source was rejected before reaching its per-route limit")
		}
		releases = append(releases, release)
	}
	if _, _, ok := service.acquire(t.Context(), "owner-a", "client-a", "route-a", "192.0.2.10"); ok {
		t.Fatal("one source exceeded its per-route connection limit")
	}
	if _, release, ok := service.acquire(t.Context(), "owner-a", "client-a", "route-a", "192.0.2.11"); !ok {
		t.Fatal("one source exhausted another source's route capacity")
	} else {
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}

	releases = nil
	for i := range 16 {
		_, release, ok := service.acquire(t.Context(), "owner-a", "client-a", fmt.Sprintf("route-%d", i), "192.0.2.10")
		if !ok {
			t.Fatalf("source was rejected before reaching its global limit at connection %d", i+1)
		}
		releases = append(releases, release)
	}
	if _, _, ok := service.acquire(t.Context(), "owner-a", "client-a", "route-overflow", "192.0.2.10"); ok {
		t.Fatal("one source exceeded its global connection limit")
	}
	for _, release := range releases {
		release()
	}

	now := time.Now()
	for range 40 {
		if !service.allowRate("route-rate\x00192.0.2.20", now, sourceRequestRate, sourceRequestBurst) {
			t.Fatal("source was rate-limited before its documented burst")
		}
	}
	if service.allowRate("route-rate\x00192.0.2.20", now, sourceRequestRate, sourceRequestBurst) {
		t.Fatal("one source exceeded its per-route request burst")
	}
	if !service.allowRate("route-rate\x00192.0.2.21", now, sourceRequestRate, sourceRequestBurst) {
		t.Fatal("one source rate-limited another source")
	}
}

func TestActivityConnTimesOutSilentPeer(t *testing.T) {
	proxy, peer := net.Pipe()
	t.Cleanup(func() {
		_ = proxy.Close()
		_ = peer.Close()
	})
	connection := newActivityConn(proxy, 25*time.Millisecond)
	errCh := make(chan error, 1)
	go func() {
		_, err := connection.Read(make([]byte, 1))
		errCh <- err
	}()

	select {
	case err := <-errCh:
		netErr, ok := errors.AsType[net.Error](err)
		if !ok || !netErr.Timeout() {
			t.Fatalf("silent connection error = %v, want timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("silent connection did not reach its activity deadline")
	}
}

func TestInvalidPublishedSlugDoesNotAllocateRateState(t *testing.T) {
	service := &Service{sourceRates: make(map[string]*sourceRateState)}
	request := httptest.NewRequest(http.MethodGet, "https://route.publish.example/r/invalid", nil)
	recorder := httptest.NewRecorder()
	service.Proxy(recorder, request, strings.Repeat("a", 64), "", "192.0.2.10")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("invalid slug status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if len(service.sourceRates) != 0 {
		t.Fatalf("invalid slug allocated %d rate-limit entries", len(service.sourceRates))
	}
}

func TestSourceRateLRUEvictsInsteadOfDenyingNewKeys(t *testing.T) {
	service := &Service{}
	now := time.Now()
	for i := range maxTrackedSourceRates {
		if !service.allowRate(fmt.Sprintf("source-%d", i), now, sourceRequestRate, sourceRequestBurst) {
			t.Fatalf("rate state %d was rejected before the bounded cache filled", i)
		}
	}
	if !service.allowRate("new-source", now, sourceRequestRate, sourceRequestBurst) {
		t.Fatal("full rate cache rejected a new source instead of evicting the oldest")
	}
	if len(service.sourceRates) != maxTrackedSourceRates {
		t.Fatalf("rate cache size = %d, want %d", len(service.sourceRates), maxTrackedSourceRates)
	}
	if _, exists := service.sourceRates["source-0"]; exists {
		t.Fatal("oldest rate state was not evicted")
	}
}
