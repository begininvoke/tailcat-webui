//go:build windows

package privatefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureDataDirectoryProtectsChildren(t *testing.T) {
	directory := t.TempDir()
	if err := SecureDataDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(path); err != nil {
		t.Fatalf("inherited private file DACL: %v", err)
	}
}
