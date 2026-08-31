//go:build !unix

package transfer

import "os"

func fileLinkCount(os.FileInfo) (uint64, bool) {
	// The portable FileInfo representation on these platforms does not expose a
	// trustworthy link count, so callers retain the other rooted identity checks.
	return 0, false
}
