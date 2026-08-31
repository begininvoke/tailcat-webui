//go:build windows

package transfer

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	windowsOwnerSecurityInformation = 0x00000001
	windowsDACLInformation          = 0x00000004
	windowsSEFileObject             = 1
	windowsAccessAllowedACE         = 0
	windowsAccessDeniedACE          = 1
	windowsLocalSystemSID           = 22
	windowsBuiltinAdministratorsSID = 26
	windowsMaxSIDSize               = 68
)

var (
	windowsAdvapi32           = syscall.NewLazyDLL("advapi32.dll")
	windowsGetSecurityInfo    = windowsAdvapi32.NewProc("GetSecurityInfo")
	windowsGetAce             = windowsAdvapi32.NewProc("GetAce")
	windowsEqualSid           = windowsAdvapi32.NewProc("EqualSid")
	windowsCreateWellKnownSid = windowsAdvapi32.NewProc("CreateWellKnownSid")
	windowsKernel32           = syscall.NewLazyDLL("kernel32.dll")
	windowsLocalFree          = windowsKernel32.NewProc("LocalFree")
)

type windowsACLHeader struct {
	Revision byte
	Sbz1     byte
	Size     uint16
	ACECount uint16
	Sbz2     uint16
}

type windowsACEHeader struct {
	Type  byte
	Flags byte
	Size  uint16
}

type windowsAllowedACE struct {
	Header   windowsACEHeader
	Mask     uint32
	SIDStart uint32
}

func fileLinkCount(os.FileInfo) (uint64, bool) {
	return 0, false
}

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
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return 0, false, ErrSymlink
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return 0, false, ErrNotRegular
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
	if uint64(info.NumberOfLinks) != expectedLinks {
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
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var owner, dacl, descriptor unsafe.Pointer
	var callErr error
	if err := raw.Control(func(handle uintptr) {
		result, _, _ := windowsGetSecurityInfo.Call(
			handle,
			windowsSEFileObject,
			windowsOwnerSecurityInformation|windowsDACLInformation,
			uintptr(unsafe.Pointer(&owner)),
			0,
			uintptr(unsafe.Pointer(&dacl)),
			0,
			uintptr(unsafe.Pointer(&descriptor)),
		)
		if result != 0 {
			callErr = syscall.Errno(result)
		}
	}); err != nil {
		return err
	}
	if callErr != nil {
		return callErr
	}
	if descriptor != nil {
		defer windowsLocalFree.Call(uintptr(descriptor))
	}
	if owner == nil || dacl == nil {
		return ErrPermissions
	}
	systemBuffer, systemSID, err := windowsWellKnownSID(windowsLocalSystemSID)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(systemBuffer)
	administratorsBuffer, administratorsSID, err := windowsWellKnownSID(windowsBuiltinAdministratorsSID)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(administratorsBuffer)
	header := (*windowsACLHeader)(dacl)
	for index := uint32(0); index < uint32(header.ACECount); index++ {
		var acePointer unsafe.Pointer
		result, _, callError := windowsGetAce.Call(uintptr(dacl), uintptr(index), uintptr(unsafe.Pointer(&acePointer)))
		if result == 0 {
			if callError != syscall.Errno(0) {
				return callError
			}
			return ErrPermissions
		}
		ace := (*windowsAllowedACE)(acePointer)
		switch ace.Header.Type {
		case windowsAccessDeniedACE:
			continue
		case windowsAccessAllowedACE:
			sid := unsafe.Pointer(&ace.SIDStart)
			if windowsSIDsEqual(sid, owner) || windowsSIDsEqual(sid, systemSID) || windowsSIDsEqual(sid, administratorsSID) {
				continue
			}
			return ErrPermissions
		default:
			return ErrPermissions
		}
	}
	return nil
}

func windowsWellKnownSID(kind uintptr) ([]byte, unsafe.Pointer, error) {
	buffer := make([]byte, windowsMaxSIDSize)
	size := uint32(len(buffer))
	result, _, callErr := windowsCreateWellKnownSid.Call(
		kind,
		0,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return nil, nil, callErr
		}
		return nil, nil, ErrPermissions
	}
	return buffer, unsafe.Pointer(&buffer[0]), nil
}

func windowsSIDsEqual(first, second unsafe.Pointer) bool {
	if first == nil || second == nil {
		return false
	}
	result, _, _ := windowsEqualSid.Call(uintptr(first), uintptr(second))
	return result != 0
}
