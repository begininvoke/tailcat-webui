//go:build windows

package transfer

import (
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDirectorySyncTreatsWindowsAccessDeniedAsUnsupported(t *testing.T) {
	err := fmt.Errorf("sync directory: %w", windows.ERROR_ACCESS_DENIED)
	if !isUnsupportedDirectorySync(err) {
		t.Fatalf("isUnsupportedDirectorySync(%v) = false, want true", err)
	}
}
