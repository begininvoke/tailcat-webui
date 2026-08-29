package tailnet

import (
	"testing"

	"github.com/tailscale/tailcat"
	"tailscale.com/tstest/integration"
	"tailscale.com/types/logger"
)

func TestMultipleTailcatServersInOneProcess(t *testing.T) {
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	region := derpMap.Regions[1]
	first := &tailcat.Server{Region: region, Logf: logger.Discard}
	second := &tailcat.Server{Region: region, Logf: logger.Discard}
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.ConnBlob() == second.ConnBlob() {
		t.Fatal("independent servers reused a connection token")
	}

	if first.Status() == nil || second.Status() == nil {
		t.Fatal("concurrent servers did not expose runtime status")
	}
}
