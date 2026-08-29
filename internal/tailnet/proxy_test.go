package tailnet

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestProxyConnectionsContextStopsSilentPeersOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	firstProxy, firstPeer := net.Pipe()
	secondProxy, secondPeer := net.Pipe()
	t.Cleanup(func() {
		_ = firstPeer.Close()
		_ = secondPeer.Close()
	})

	done := make(chan struct{})
	go func() {
		proxyConnectionsContext(ctx, firstProxy, secondProxy)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop after runtime cancellation")
	}
}

func TestProxyConnectionsUntilClosedStopsWhenBrowserDisconnects(t *testing.T) {
	browserProxy, browserPeer := net.Pipe()
	remoteProxy, remotePeer := net.Pipe()
	t.Cleanup(func() {
		_ = browserPeer.Close()
		_ = remotePeer.Close()
	})

	done := make(chan struct{})
	go func() {
		ProxyConnectionsUntilClosed(t.Context(), browserProxy, remoteProxy)
		close(done)
	}()
	_ = browserPeer.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("browser disconnect left the silent remote tunnel running")
	}
}
