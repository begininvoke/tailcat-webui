package publish

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/ent/publishedroute"

	_ "github.com/lib-x/entsqlite"
)

var registryTestDatabaseID atomic.Uint64

func TestTransportRegistryReusesConnectionWithinRoute(t *testing.T) {
	dialer := newRegistryTestDialer()
	registry := newTransportRegistry(dialer, maxPublishedTransports)
	t.Cleanup(registry.Close)
	key := RouteTransportKey{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}

	for range 2 {
		transport := registry.Get(key)
		response, err := transport.RoundTrip(httptestRequest(t))
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		registry.Release(transport)
	}

	if got := dialer.Dials(key); got != 1 {
		t.Fatalf("route dial count = %d, want 1", got)
	}
}

func TestTransportRegistryIsolatesRoutes(t *testing.T) {
	dialer := newRegistryTestDialer()
	registry := newTransportRegistry(dialer, maxPublishedTransports)
	t.Cleanup(registry.Close)
	routeA := RouteTransportKey{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}
	routeB := RouteTransportKey{RouteID: "route-b", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}

	transportA := registry.Get(routeA)
	transportB := registry.Get(routeB)
	if transportA == transportB {
		t.Fatal("different routes shared one transport")
	}
	for _, transport := range []*http.Transport{transportA, transportB} {
		response, err := transport.RoundTrip(httptestRequest(t))
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = response.Body.Close()
		registry.Release(transport)
	}

	if got := dialer.TotalDials(); got != 2 {
		t.Fatalf("isolated route dial count = %d, want 2", got)
	}
}

func TestTransportRegistryRouteInvalidationClosesIdleConnectionAndPreventsReuse(t *testing.T) {
	dialer := newRegistryTestDialer()
	registry := newTransportRegistry(dialer, maxPublishedTransports)
	t.Cleanup(registry.Close)
	key := RouteTransportKey{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}
	transport := registry.Get(key)
	response, err := transport.RoundTrip(httptestRequest(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	registry.Release(transport)

	registry.InvalidateRoute(key.RouteID)
	dialer.WaitClosed(t, key, 1)
	fresh := registry.Get(key)
	if fresh == transport {
		t.Fatal("invalidated route reused stale transport")
	}
	response, err = fresh.RoundTrip(httptestRequest(t))
	if err != nil {
		t.Fatalf("RoundTrip after invalidation: %v", err)
	}
	_ = response.Body.Close()
	registry.Release(fresh)
	if got := dialer.Dials(key); got != 2 {
		t.Fatalf("route dial count after invalidation = %d, want 2", got)
	}
}

func TestTransportRegistryClientInvalidationIsOwnerScoped(t *testing.T) {
	dialer := newRegistryTestDialer()
	registry := newTransportRegistry(dialer, maxPublishedTransports)
	t.Cleanup(registry.Close)
	ownedRoutes := []RouteTransportKey{
		{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080},
		{RouteID: "route-b", OwnerID: "owner-a", ClientID: "client-a", Port: 8081},
	}
	otherOwner := RouteTransportKey{RouteID: "route-c", OwnerID: "owner-b", ClientID: "client-a", Port: 8082}
	original := make(map[string]*http.Transport)
	for _, key := range append(ownedRoutes, otherOwner) {
		transport := registry.Get(key)
		original[key.RouteID] = transport
		response, err := transport.RoundTrip(httptestRequest(t))
		if err != nil {
			t.Fatalf("RoundTrip %s: %v", key.RouteID, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		registry.Release(transport)
	}

	registry.InvalidateClient("owner-a", "client-a")
	for _, key := range ownedRoutes {
		dialer.WaitClosed(t, key, 1)
		fresh := registry.Get(key)
		if fresh == original[key.RouteID] {
			t.Fatalf("client invalidation reused route %s transport", key.RouteID)
		}
		registry.Release(fresh)
	}
	if preserved := registry.Get(otherOwner); preserved != original[otherOwner.RouteID] {
		t.Fatal("client invalidation crossed owner boundary")
	} else {
		registry.Release(preserved)
	}
}

func TestTransportRegistryNeverEvictsActiveLease(t *testing.T) {
	registry := newTransportRegistry(newRegistryTestDialer(), 2)
	t.Cleanup(registry.Close)
	routeA := RouteTransportKey{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}
	routeB := RouteTransportKey{RouteID: "route-b", OwnerID: "owner-a", ClientID: "client-a", Port: 8081}
	routeC := RouteTransportKey{RouteID: "route-c", OwnerID: "owner-a", ClientID: "client-a", Port: 8082}

	activeA := registry.Get(routeA)
	idleB := registry.Get(routeB)
	registry.Release(idleB)
	activeC := registry.Get(routeC)
	if activeC == nil {
		t.Fatal("idle transport was not evicted for a new route")
	}
	if got := registry.Get(routeB); got != nil {
		registry.Release(got)
		t.Fatal("registry exceeded its capacity while all entries were active")
	}
	secondLeaseA := registry.Get(routeA)
	if secondLeaseA != activeA {
		t.Fatal("active route transport was evicted")
	}
	registry.Release(secondLeaseA)
	registry.Release(activeA)
	registry.Release(activeC)
}

func TestTransportRegistryCloseInvalidatesIdleConnections(t *testing.T) {
	dialer := newRegistryTestDialer()
	registry := newTransportRegistry(dialer, maxPublishedTransports)
	key := RouteTransportKey{RouteID: "route-a", OwnerID: "owner-a", ClientID: "client-a", Port: 8080}
	transport := registry.Get(key)
	response, err := transport.RoundTrip(httptestRequest(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	registry.Release(transport)

	registry.Close()
	dialer.WaitClosed(t, key, 1)
	if got := registry.Get(key); got != nil {
		t.Fatal("closed registry returned a transport")
	}
}

func TestServiceCloseDrainsActiveUpgradeBeforeReturning(t *testing.T) {
	db := enttest.Open(t, "sqlite3", registryTestDatabaseDSN("transport-close-upgrade"))
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	route := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route").SetSlug("route-a").SetRemotePort(8080).SetAccess(publishedroute.AccessPublic).SaveX(t.Context())
	dialer := &registryUpgradeDialer{upgraded: make(chan struct{}), exited: make(chan struct{})}
	service := newRegistryTestService(t, db, dialer)
	proxyDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		service.Proxy(w, request, route.Slug, "", "192.0.2.10")
		close(proxyDone)
	}))
	t.Cleanup(server.Close)
	frontend, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = frontend.Close() })
	_, err = fmt.Fprintf(frontend, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", service.routeURL(route).Host)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(frontend), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d, want %d", response.StatusCode, http.StatusSwitchingProtocols)
	}
	select {
	case <-dialer.upgraded:
	case <-time.After(time.Second):
		t.Fatal("upstream upgrade did not become active")
	}

	closeDone := make(chan struct{})
	go func() {
		service.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Service.Close did not drain active upgrade")
	}
	select {
	case <-proxyDone:
	default:
		t.Fatal("Service.Close returned before active upgrade work exited")
	}
	select {
	case <-dialer.exited:
	default:
		t.Fatal("Service.Close returned before upstream upgrade exited")
	}
	if _, _, ok := service.acquire(t.Context(), owner.ID, client.ID, route.ID, "192.0.2.11"); ok {
		t.Fatal("closed service admitted new published work")
	}
}

func TestRouteDeleteInvalidatesRuntimeBeforeDurableDeletion(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transport-delete?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	route := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route").SetSlug("route-a").SetRemotePort(8080).SetAccess(publishedroute.AccessPublic).SaveX(t.Context())
	dialer := newRegistryTestDialer()
	service := newRegistryTestService(t, db, dialer)
	key := RouteTransportKey{RouteID: route.ID, OwnerID: owner.ID, ClientID: client.ID, Port: route.RemotePort}
	transport := service.transports.Get(key)
	response, err := transport.RoundTrip(httptestRequest(t))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	service.transports.Release(transport)
	activeCtx, release, ok := service.acquire(t.Context(), owner.ID, client.ID, route.ID, "192.0.2.10")
	if !ok {
		t.Fatal("acquire active route work")
	}
	defer release()

	db.PublishedRoute.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op() == ent.OpDelete {
				select {
				case <-activeCtx.Done():
				default:
					t.Fatal("durable delete began before active route work was canceled")
				}
				dialer.WaitClosed(t, key, 1)
			}
			return next.Mutate(ctx, mutation)
		})
	})
	if err := service.Delete(t.Context(), owner.ID, route.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestPublishedRouteSequentialRequestsReuseConnection(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transport-proxy?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	route := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route").SetSlug("route-a").SetRemotePort(8080).SetAccess(publishedroute.AccessPublic).SaveX(t.Context())
	dialer := newRegistryTestDialer()
	service := newRegistryTestService(t, db, dialer)

	for range 2 {
		request := httptestRequest(t)
		request.Host = service.routeURL(route).Host
		recorder := httptest.NewRecorder()
		service.Proxy(recorder, request, route.Slug, "", "192.0.2.10")
		if recorder.Code != http.StatusOK {
			t.Fatalf("proxy status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	key := RouteTransportKey{RouteID: route.ID, OwnerID: owner.ID, ClientID: client.ID, Port: route.RemotePort}
	if got := dialer.Dials(key); got != 1 {
		t.Fatalf("published route dial count = %d, want 1", got)
	}
}

func TestClientInvalidationCancelsAllOwnedRouteWork(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transport-client-delete?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	otherClient := db.TailClient.Create().SetUserID(owner.ID).SetName("other").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	routeA := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route a").SetSlug("route-a").SetRemotePort(8080).SaveX(t.Context())
	routeB := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route b").SetSlug("route-b").SetRemotePort(8081).SaveX(t.Context())
	otherRoute := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(otherClient.ID).SetName("other route").SetSlug("route-c").SetRemotePort(8082).SaveX(t.Context())
	service := newRegistryTestService(t, db, newRegistryTestDialer())

	ctxA, releaseA, ok := service.acquire(t.Context(), owner.ID, client.ID, routeA.ID, "192.0.2.10")
	if !ok {
		t.Fatal("acquire route A")
	}
	defer releaseA()
	ctxB, releaseB, ok := service.acquire(t.Context(), owner.ID, client.ID, routeB.ID, "192.0.2.11")
	if !ok {
		t.Fatal("acquire route B")
	}
	defer releaseB()

	if err := service.InvalidateClient(t.Context(), owner.ID, client.ID); err != nil {
		t.Fatalf("InvalidateClient: %v", err)
	}
	for name, ctx := range map[string]context.Context{"route A": ctxA, "route B": ctxB} {
		select {
		case <-ctx.Done():
		default:
			t.Fatalf("%s work was not canceled", name)
		}
	}
	if _, _, ok := service.acquire(t.Context(), owner.ID, client.ID, routeA.ID, "192.0.2.12"); ok {
		t.Fatal("invalidated client admitted new route work")
	}
	service.CompleteClientInvalidation(owner.ID, client.ID)
	if _, release, ok := service.acquire(t.Context(), owner.ID, client.ID, routeA.ID, "192.0.2.12"); !ok {
		t.Fatal("completed client invalidation left a stale admission barrier")
	} else {
		release()
	}
	if _, release, ok := service.acquire(t.Context(), owner.ID, otherClient.ID, otherRoute.ID, "192.0.2.12"); !ok {
		t.Fatal("client invalidation crossed into another client")
	} else {
		release()
	}
}

func TestRouteDeleteTombstoneSurvivesFailedClientInvalidationOverlap(t *testing.T) {
	db, owner, client, route, service := newRouteInvalidationTest(t, "failed-client-overlap")
	deleteStarted := make(chan struct{})
	allowDelete := make(chan struct{})
	allowDeleteOnce := sync.OnceFunc(func() { close(allowDelete) })
	t.Cleanup(allowDeleteOnce)
	db.PublishedRoute.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op() == ent.OpDelete {
				close(deleteStarted)
				select {
				case <-allowDelete:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	deleteResult := make(chan error, 1)
	go func() { deleteResult <- service.Delete(t.Context(), owner.ID, route.ID) }()
	select {
	case <-deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("route delete did not reach durable barrier")
	}
	if err := service.InvalidateClient(t.Context(), owner.ID, client.ID); err != nil {
		t.Fatalf("InvalidateClient: %v", err)
	}
	service.CompleteClientInvalidation(owner.ID, client.ID)
	if _, release, ok := service.acquire(t.Context(), owner.ID, client.ID, route.ID, "192.0.2.20"); ok {
		release()
		t.Fatal("failed client invalidation cleared an overlapping route-delete tombstone")
	}
	allowDeleteOnce()
	if err := <-deleteResult; err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestConcurrentRouteDeleteFailureKeepsOtherTombstone(t *testing.T) {
	db, owner, client, route, service := newRouteInvalidationTest(t, "concurrent-route-delete")
	firstStarted := make(chan struct{})
	allowFirst := make(chan struct{})
	allowFirstOnce := sync.OnceFunc(func() { close(allowFirst) })
	t.Cleanup(allowFirstOnce)
	db.PublishedRoute.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op() != ent.OpDelete {
				return next.Mutate(ctx, mutation)
			}
			switch ctx.Value(routeDeleteTestKey{}) {
			case "first":
				close(firstStarted)
				select {
				case <-allowFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return next.Mutate(ctx, mutation)
			case "second":
				return nil, context.Canceled
			default:
				return next.Mutate(ctx, mutation)
			}
		})
	})
	firstResult := make(chan error, 1)
	firstCtx := context.WithValue(t.Context(), routeDeleteTestKey{}, "first")
	go func() { firstResult <- service.Delete(firstCtx, owner.ID, route.ID) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first route delete did not reach durable barrier")
	}
	secondCtx := context.WithValue(t.Context(), routeDeleteTestKey{}, "second")
	if err := service.Delete(secondCtx, owner.ID, route.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Delete error = %v, want context canceled", err)
	}
	if _, release, ok := service.acquire(t.Context(), owner.ID, client.ID, route.ID, "192.0.2.21"); ok {
		release()
		t.Fatal("failed concurrent route delete cleared the first delete tombstone")
	}
	allowFirstOnce()
	if err := <-firstResult; err != nil {
		t.Fatalf("first Delete: %v", err)
	}
}

type routeDeleteTestKey struct{}

func newRouteInvalidationTest(t *testing.T, databaseName string) (*ent.Client, *ent.User, *ent.TailClient, *ent.PublishedRoute, *Service) {
	t.Helper()
	db := enttest.Open(t, "sqlite3", registryTestDatabaseDSN(databaseName))
	owner := db.User.Create().SetIssuer("test").SetSubject("owner").SaveX(t.Context())
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(t.Context())
	route := db.PublishedRoute.Create().SetUserID(owner.ID).SetClientID(client.ID).SetName("route").SetSlug("route-a").SetRemotePort(8080).SetAccess(publishedroute.AccessPublic).SaveX(t.Context())
	return db, owner, client, route, newRegistryTestService(t, db, newRegistryTestDialer())
}

func registryTestDatabaseDSN(name string) string {
	return fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", name, registryTestDatabaseID.Add(1))
}

func newRegistryTestService(t *testing.T, db *ent.Client, dialer PortDialer) *Service {
	t.Helper()
	managementURL, _ := url.Parse("https://manage.example.test")
	publishURL, _ := url.Parse("https://publish.example.test")
	service, err := NewService(db, dialer, managementURL, publishURL, []byte("01234567890123456789012345678901"), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://server.tailcat/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type registryTestDialer struct {
	mu     sync.Mutex
	dials  map[registryDialKey]int
	closed map[registryDialKey]int
	wake   chan struct{}
}

func newRegistryTestDialer() *registryTestDialer {
	return &registryTestDialer{dials: make(map[registryDialKey]int), closed: make(map[registryDialKey]int), wake: make(chan struct{}, 16)}
}

func (d *registryTestDialer) DialPort(_ context.Context, ownerID, clientID string, port uint16) (net.Conn, error) {
	key := registryDialKey{ownerID: ownerID, clientID: clientID, port: port}
	d.mu.Lock()
	d.dials[key]++
	d.mu.Unlock()

	client, server := net.Pipe()
	go func() {
		serveRegistryTestConnection(server)
		d.mu.Lock()
		d.closed[key]++
		d.mu.Unlock()
		d.wake <- struct{}{}
	}()
	return client, nil
}

func (d *registryTestDialer) Dials(key RouteTransportKey) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials[registryDialKey{ownerID: key.OwnerID, clientID: key.ClientID, port: key.Port}]
}

func (d *registryTestDialer) TotalDials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for count := range maps.Values(d.dials) {
		total += count
	}
	return total
}

func (d *registryTestDialer) WaitClosed(t *testing.T, key RouteTransportKey, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	dialKey := registryDialKey{ownerID: key.OwnerID, clientID: key.ClientID, port: key.Port}
	for {
		d.mu.Lock()
		got := d.closed[dialKey]
		d.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-d.wake:
		case <-ctx.Done():
			t.Fatalf("closed connections = %d, want %d", got, want)
		}
	}
}

type registryDialKey struct {
	ownerID  string
	clientID string
	port     uint16
}

type registryUpgradeDialer struct {
	upgraded chan struct{}
	exited   chan struct{}
}

func (d *registryUpgradeDialer) DialPort(context.Context, string, string, uint16) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer close(d.exited)
		defer server.Close()
		request, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		_ = request.Body.Close()
		_, err = fmt.Fprint(server, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if err != nil {
			return
		}
		close(d.upgraded)
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

func serveRegistryTestConnection(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		_ = request.Body.Close()
		_, err = fmt.Fprint(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		if err != nil {
			return
		}
	}
}
