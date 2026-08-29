package tailnet

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/tailscale/tailcat"
)

// ProxyConnections keeps the unstable upstream Tailcat API inside this
// adapter package while preserving TCP half-close behavior.
func ProxyConnections(first, second net.Conn) {
	tailcat.ProxyConns(first, second)
}

// ProxyConnectionsUntilClosed treats either stream ending as the end of the
// browser tunnel. Unlike Tailcat's half-close proxy, it closes both sides and
// joins both copy goroutines before returning.
func ProxyConnectionsUntilClosed(ctx context.Context, first, second net.Conn) {
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var copies sync.WaitGroup
	copies.Go(func() {
		_, _ = io.Copy(first, second)
		cancel()
	})
	copies.Go(func() {
		_, _ = io.Copy(second, first)
		cancel()
	})
	<-proxyCtx.Done()
	_ = first.Close()
	_ = second.Close()
	copies.Wait()
}

// proxyConnectionsContext preserves Tailcat's TCP half-close behavior while
// ensuring that both copy directions stop when their owning runtime shuts
// down. The watcher is joined before this function returns so it cannot
// outlive the proxy handler.
func proxyConnectionsContext(ctx context.Context, first, second net.Conn) {
	done := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-ctx.Done():
			_ = first.Close()
			_ = second.Close()
		case <-done:
		}
	})
	tailcat.ProxyConns(first, second)
	close(done)
	watcher.Wait()
}
