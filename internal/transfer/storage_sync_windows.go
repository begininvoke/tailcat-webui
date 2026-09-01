//go:build windows

package transfer

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isPlatformUnsupportedDirectorySync(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
