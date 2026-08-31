//go:build !unix && !windows

package transfer

import "os"

func fileLinkCount(os.FileInfo) (uint64, bool) {
	// The portable FileInfo representation on these platforms does not expose a
	// trustworthy link count, so callers retain the other rooted identity checks.
	return 0, false
}

func rootedFileLinkCount(*os.Root, string, os.FileInfo) (uint64, bool, error) {
	return 0, false, nil
}

func validatePlatformFileInfo(os.FileInfo) error { return nil }

func validateOpenedRegularFile(*os.File, uint64) error { return nil }

func validateOpenedDirectory(*os.File) error { return nil }
