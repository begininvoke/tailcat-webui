//go:build windows

package transfer

import (
	"errors"
	"os"
	"syscall"

	"github.com/ca-x/tailcat-webui/internal/privatefs"
)

func fileLinkCount(os.FileInfo) (uint64, bool) { return 0, false }

func rootedFileLinkCount(root *os.Root, name string, before os.FileInfo) (uint64, bool, error) {
	if err := validatePlatformFileInfo(before); err != nil {
		return 0, false, err
	}
	file, err := root.Open(name)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	info, err := windowsHandleInformation(file)
	if err != nil {
		return 0, false, err
	}
	if err := validateWindowsHandleInformation(info, 0, false); err != nil && !errors.Is(err, ErrMultipleLinks) {
		return 0, false, err
	}
	if err := validateWindowsPrivateDACL(file); err != nil {
		return 0, false, err
	}
	opened, err := file.Stat()
	if err != nil {
		return 0, false, err
	}
	after, err := root.Lstat(name)
	if err != nil {
		return 0, false, err
	}
	if err := validatePlatformFileInfo(after); err != nil {
		return 0, false, err
	}
	if err := validateStableFileIdentity(before, opened, after); err != nil {
		return 0, false, err
	}
	return uint64(info.NumberOfLinks), true, nil
}

func validatePlatformFileInfo(info os.FileInfo) error {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return errors.Join(ErrInvalidPath, errors.New("windows file attributes unavailable"))
	}
	if data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrSymlink
	}
	return nil
}

func validateOpenedRegularFile(file *os.File, expectedLinks uint64) error {
	info, err := windowsHandleInformation(file)
	if err != nil {
		return err
	}
	if err := validateWindowsHandleInformation(info, expectedLinks, false); err != nil {
		return err
	}
	return validateWindowsPrivateDACL(file)
}

func validateOpenedDirectory(file *os.File) error {
	info, err := windowsHandleInformation(file)
	if err != nil {
		return err
	}
	if err := validateWindowsHandleInformation(info, 0, true); err != nil {
		return err
	}
	return validateWindowsPrivateDACL(file)
}

func validateWindowsHandleInformation(info syscall.ByHandleFileInformation, expectedLinks uint64, directory bool) error {
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrSymlink
	}
	if directory {
		if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return ErrInvalidPath
		}
		if info.NumberOfLinks != 1 {
			return ErrMultipleLinks
		}
		return nil
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return ErrNotRegular
	}
	if expectedLinks != 0 && uint64(info.NumberOfLinks) != expectedLinks {
		return ErrMultipleLinks
	}
	return nil
}

func windowsHandleInformation(file *os.File) (syscall.ByHandleFileInformation, error) {
	var info syscall.ByHandleFileInformation
	raw, err := file.SyscallConn()
	if err != nil {
		return info, err
	}
	var callErr error
	if err := raw.Control(func(handle uintptr) {
		callErr = syscall.GetFileInformationByHandle(syscall.Handle(handle), &info)
	}); err != nil {
		return info, err
	}
	return info, callErr
}

func validateWindowsPrivateDACL(file *os.File) error {
	if err := privatefs.ValidatePrivateHandle(file); err != nil {
		switch {
		case errors.Is(err, privatefs.ErrReparse):
			return ErrSymlink
		case errors.Is(err, privatefs.ErrPermissions):
			return ErrPermissions
		default:
			return err
		}
	}
	return nil
}
