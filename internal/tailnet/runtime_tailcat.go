package tailnet

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sync"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"
)

type tailcatRuntimeFactory struct{}

func (tailcatRuntimeFactory) NewServer(_ context.Context, spec ServerSpec) (ServerRuntime, error) {
	handlers := maps.Clone(spec.TCPHandlers)
	reservedHandlers := maps.Clone(spec.ReservedTCPHandlers)
	sshPorts := maps.Clone(spec.NoAuthSSHPorts)
	for port := range reservedHandlers {
		if handlers[port] != nil {
			return nil, fmt.Errorf("Tailcat TCP port %d has both user and reserved handlers", port)
		}
		if _, ok := sshPorts[port]; ok {
			return nil, fmt.Errorf("Tailcat TCP port %d has both SSH and reserved handlers", port)
		}
	}
	for port := range sshPorts {
		if handlers[port] != nil {
			return nil, fmt.Errorf("Tailcat TCP port %d has both user and SSH handlers", port)
		}
	}

	server := &tailcat.Server{
		Key:            spec.Key,
		Logf:           spec.Logf,
		Region:         spec.Region,
		RegionID:       spec.RegionID,
		DERPMapURL:     spec.DERPMapURL,
		AllowedClients: slices.Clone(spec.AllowedClients),
		AllowProxy:     spec.AllowProxy,
	}
	runtime := newTailcatServerRuntime(&upstreamTailcatServer{server: server})
	directSlots := make(chan struct{}, 128)
	server.OnTCP = func(port uint16) func(net.Conn) {
		handler := reservedHandlers[port]
		if handler == nil {
			handler = handlers[port]
		}
		if handler == nil {
			if _, ok := sshPorts[port]; !ok {
				return nil
			}
			handler = func(_ context.Context, connection net.Conn) {
				server.HandleTailscaleSSHConn(connection)
			}
		}
		return runtime.wrapHandler(directSlots, handler)
	}
	if spec.ForwardTCPHandler != nil {
		forwardSlots := make(chan struct{}, 64)
		server.OnTCPForward = func(target netip.AddrPort) func(net.Conn) {
			handler := spec.ForwardTCPHandler(target)
			if handler == nil {
				return nil
			}
			return runtime.wrapHandler(forwardSlots, handler)
		}
	}
	ports := make(map[uint16]struct{}, len(handlers)+len(reservedHandlers)+len(sshPorts))
	for port := range handlers {
		ports[port] = struct{}{}
	}
	for port := range reservedHandlers {
		ports[port] = struct{}{}
	}
	for port := range sshPorts {
		ports[port] = struct{}{}
	}
	for _, port := range slices.Sorted(maps.Keys(ports)) {
		server.ServedTCPPorts = append(server.ServedTCPPorts, filter.PortRange{First: port, Last: port})
	}
	return runtime, nil
}

func (tailcatRuntimeFactory) NewClient(_ context.Context, spec ClientSpec) (ClientRuntime, error) {
	return &tailcatClientRuntime{client: &tailcat.Client{
		Server:     tailcat.ConnBlob(spec.ConnectionToken),
		Key:        spec.Key,
		Logf:       spec.Logf,
		DERPMapURL: spec.DERPMapURL,
	}}, nil
}

type tailcatServerRuntime struct {
	server      tailcatServerEngine
	ctx         context.Context
	cancel      context.CancelFunc
	admissionMu sync.Mutex
	stopping    bool
	handlers    sync.WaitGroup
	connections map[net.Conn]struct{}
}

type tailcatServerEngine interface {
	Start() error
	Close() error
	DrainTCP(context.Context) error
	ConnectionToken() string
	PublicKey() string
	AddAllowedClient(key.NodePublic)
}

type upstreamTailcatServer struct {
	server *tailcat.Server
}

func (s *upstreamTailcatServer) Start() error { return s.server.Start() }

func (s *upstreamTailcatServer) Close() error { return s.server.Close() }

func (s *upstreamTailcatServer) DrainTCP(ctx context.Context) error {
	return s.server.DrainTCP(ctx)
}

func (s *upstreamTailcatServer) ConnectionToken() string {
	return string(s.server.ConnBlob())
}

func (s *upstreamTailcatServer) PublicKey() string {
	info, err := tailcat.ParseConnBlob(s.server.ConnBlob())
	if err != nil {
		return ""
	}
	return info.ServerPublic.String()
}

func (s *upstreamTailcatServer) AddAllowedClient(public key.NodePublic) {
	s.server.AddAllowedClient(public)
}

func newTailcatServerRuntime(server tailcatServerEngine) *tailcatServerRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &tailcatServerRuntime{
		server:      server,
		ctx:         ctx,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
}

func (r *tailcatServerRuntime) Start() error { return r.server.Start() }

func (r *tailcatServerRuntime) Close() error {
	r.beginShutdown()
	return r.server.Close()
}

func (r *tailcatServerRuntime) DrainTCP(ctx context.Context) error {
	r.beginShutdown()
	done := make(chan struct{})
	go func() {
		r.handlers.Wait()
		close(done)
	}()
	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	return errors.Join(waitErr, r.server.DrainTCP(ctx))
}

func (r *tailcatServerRuntime) ConnectionToken() string {
	return r.server.ConnectionToken()
}

func (r *tailcatServerRuntime) PublicKey() string {
	return r.server.PublicKey()
}

func (r *tailcatServerRuntime) AddAllowedClient(public key.NodePublic) {
	r.server.AddAllowedClient(public)
}

func (r *tailcatServerRuntime) wrapHandler(slots chan struct{}, handler TCPHandler) func(net.Conn) {
	return func(connection net.Conn) {
		r.admissionMu.Lock()
		if r.stopping {
			r.admissionMu.Unlock()
			_ = connection.Close()
			return
		}
		r.handlers.Add(1)
		r.connections[connection] = struct{}{}
		r.admissionMu.Unlock()
		defer func() {
			r.admissionMu.Lock()
			delete(r.connections, connection)
			r.admissionMu.Unlock()
			r.handlers.Done()
		}()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			handler(r.ctx, connection)
		default:
			_ = connection.Close()
		}
	}
}

func (r *tailcatServerRuntime) beginShutdown() {
	r.admissionMu.Lock()
	if r.stopping {
		r.admissionMu.Unlock()
		return
	}
	r.stopping = true
	r.cancel()
	connections := slices.Collect(maps.Keys(r.connections))
	r.admissionMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

type tailcatClientRuntime struct {
	client *tailcat.Client
}

func (r *tailcatClientRuntime) Close() error { return r.client.Close() }

func (r *tailcatClientRuntime) PublicKey() string { return r.client.PublicKey().String() }

func (r *tailcatClientRuntime) DiscoPing(ctx context.Context) (PingResult, error) {
	result, err := r.client.DiscoPing(ctx)
	if err != nil {
		return PingResult{}, err
	}
	return PingResult{
		Endpoint:       result.Endpoint,
		PeerRelay:      result.PeerRelay,
		LatencySeconds: result.LatencySeconds,
	}, nil
}

func (r *tailcatClientRuntime) DialTCPPort(ctx context.Context, port uint16) (net.Conn, error) {
	return r.client.DialTCPPort(ctx, port)
}

func (r *tailcatClientRuntime) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return r.client.Dial(ctx, network, address)
}
