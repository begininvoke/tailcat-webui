//go:build windows

package privatefs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ErrPermissions = errors.New("private filesystem object has unsafe permissions")
	ErrReparse     = errors.New("private filesystem object is a reparse point")
)

func SecureDataDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("data path is not a directory")
	}
	if err := validateFileAttributes(info, true); err != nil {
		return err
	}
	owner, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{
		privateAccess(owner, windows.TRUSTEE_IS_USER),
		privateAccess(system, windows.TRUSTEE_IS_GROUP),
		privateAccess(administrators, windows.TRUSTEE_IS_GROUP),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("secure data directory DACL: %w", err)
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return ValidatePrivateHandle(directory)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("private path is not a regular file")
	}
	if err := validateFileAttributes(info, false); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return ValidatePrivateHandle(file)
}

func ValidatePrivateHandle(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var validationErr error
	if err := raw.Control(func(handle uintptr) {
		validationErr = validateHandle(windows.Handle(handle))
	}); err != nil {
		return err
	}
	return validationErr
}

func validateHandle(handle windows.Handle) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(handle), &info); err != nil {
		return err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrReparse
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if descriptor == nil {
		return ErrPermissions
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.Join(ErrPermissions, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.Join(ErrPermissions, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	currentUser, err := currentUserSID()
	if err != nil {
		return err
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if sid.Equals(owner) || sid.Equals(currentUser) || sid.Equals(system) || sid.Equals(administrators) {
				continue
			}
		}
		return ErrPermissions
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func privateAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func validateFileAttributes(info os.FileInfo, directory bool) error {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return fmt.Errorf("windows file attributes unavailable")
	}
	if data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrReparse
	}
	if directory != info.IsDir() {
		return fmt.Errorf("private filesystem object type mismatch")
	}
	return nil
}
