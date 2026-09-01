//go:build !windows

package transfer

func isPlatformUnsupportedDirectorySync(error) bool { return false }
