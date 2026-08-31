package publish

import (
	"container/list"
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

const maxPublishedTransports = 512

type RouteTransportKey struct {
	RouteID  string
	OwnerID  string
	ClientID string
	Port     uint16
}

type transportRegistry struct {
	mu         sync.Mutex
	dialer     PortDialer
	capacity   int
	entries    map[string]*transportEntry
	transports map[*http.Transport]*transportEntry
	lru        *list.List
	closed     bool
}

type transportEntry struct {
	key       RouteTransportKey
	transport *http.Transport
	active    int
	element   *list.Element
	retired   bool
}

func newTransportRegistry(dialer PortDialer, capacity int) *transportRegistry {
	return &transportRegistry{
		dialer:     dialer,
		capacity:   max(1, capacity),
		entries:    make(map[string]*transportEntry),
		transports: make(map[*http.Transport]*transportEntry),
		lru:        list.New(),
	}
}

func (r *transportRegistry) Get(key RouteTransportKey) *http.Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || key.RouteID == "" {
		return nil
	}
	if entry := r.entries[key.RouteID]; entry != nil {
		if entry.key == key {
			entry.active++
			r.lru.MoveToFront(entry.element)
			return entry.transport
		}
		r.retireLocked(entry)
	}
	for len(r.transports) >= r.capacity {
		if !r.evictIdleLocked() {
			return nil
		}
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, err := r.dialer.DialPort(ctx, key.OwnerID, key.ClientID, key.Port)
			if err != nil {
				return nil, err
			}
			return newActivityConn(connection, publishedConnectionIdleTimeout), nil
		},
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	entry := &transportEntry{key: key, transport: transport, active: 1}
	entry.element = r.lru.PushFront(entry)
	r.entries[key.RouteID] = entry
	r.transports[transport] = entry
	return transport
}

func (r *transportRegistry) Release(transport *http.Transport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.transports[transport]
	if entry == nil || entry.active == 0 {
		return
	}
	entry.active--
	if entry.retired && entry.active == 0 {
		entry.transport.CloseIdleConnections()
		delete(r.transports, entry.transport)
	}
}

func (r *transportRegistry) InvalidateRoute(routeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.entries[routeID]; entry != nil {
		r.retireLocked(entry)
	}
}

func (r *transportRegistry) InvalidateClient(ownerID, clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.entries {
		if entry.key.OwnerID == ownerID && entry.key.ClientID == clientID {
			r.retireLocked(entry)
		}
	}
}

func (r *transportRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, entry := range r.entries {
		r.retireLocked(entry)
	}
}

func (r *transportRegistry) evictIdleLocked() bool {
	for element := r.lru.Back(); element != nil; element = element.Prev() {
		entry := element.Value.(*transportEntry)
		if entry.active == 0 {
			r.retireLocked(entry)
			return true
		}
	}
	return false
}

func (r *transportRegistry) retireLocked(entry *transportEntry) {
	if entry.retired {
		return
	}
	entry.retired = true
	delete(r.entries, entry.key.RouteID)
	if entry.element != nil {
		r.lru.Remove(entry.element)
		entry.element = nil
	}
	entry.transport.CloseIdleConnections()
	if entry.active == 0 {
		delete(r.transports, entry.transport)
	}
}
