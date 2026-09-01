//go:build !windows

package privatefs

import "os"

func SecureDataDirectory(string) error { return nil }

func ValidatePrivateFile(string) error { return nil }

func ValidatePrivateHandle(*os.File) error { return nil }
