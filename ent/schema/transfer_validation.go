package schema

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
	"uuid"
)

const (
	maxTransferStorageNameBytes = 128
	maxTransferVirtualPathBytes = 1024
	maxTransferVirtualPathDepth = 32
)

var transferStorageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{15,127}$`)

func validateSHA256Hash(hash []byte) error {
	if len(hash) != sha256.Size {
		return fmt.Errorf("SHA-256 hash must be %d bytes", sha256.Size)
	}
	return nil
}

func validateUUIDv7(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || parsed[6]>>4 != 7 || parsed[8]&0xc0 != 0x80 {
		return fmt.Errorf("must be a canonical UUIDv7")
	}
	return nil
}

func validateStorageName(name string) error {
	if len(name) > maxTransferStorageNameBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\:") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("storage name must be a safe opaque basename")
	}
	if name == "." || name == ".." || isWindowsDeviceName(name) || !transferStorageNamePattern.MatchString(name) {
		return fmt.Errorf("storage name must be a random lowercase opaque basename")
	}
	return nil
}

func validateVirtualPath(path string) error {
	if path == "" || len(path) > maxTransferVirtualPathBytes || !utf8.ValidString(path) || strings.ContainsAny(path, "\\:") || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") {
		return fmt.Errorf("virtual path must be a canonical relative slash path")
	}
	segments := strings.Split(path, "/")
	if len(segments) > maxTransferVirtualPathDepth {
		return fmt.Errorf("virtual path exceeds depth limit")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || isWindowsDeviceName(segment) {
			return fmt.Errorf("virtual path contains a non-canonical segment")
		}
	}
	return nil
}

func isWindowsDeviceName(name string) bool {
	base := strings.ToUpper(name)
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}
