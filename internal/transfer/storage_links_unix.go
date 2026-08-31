//go:build unix

package transfer

import (
	"os"
	"syscall"
)

func fileLinkCount(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}

func rootedFileLinkCount(_ *os.Root, _ string, info os.FileInfo) (uint64, bool, error) {
	links, ok := fileLinkCount(info)
	return links, ok, nil
}

func validatePlatformFileInfo(os.FileInfo) error { return nil }

func validateOpenedRegularFile(file *os.File, expectedLinks uint64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	links, ok := fileLinkCount(info)
	if ok && links != expectedLinks {
		return ErrMultipleLinks
	}
	return nil
}

func validateOpenedDirectory(*os.File) error { return nil }
