//go:build windows

package transfer

import (
	"errors"
	"syscall"
	"testing"
)

func TestWindowsHandleInformationRejectsReparseAndMultipleLinks(t *testing.T) {
	regular := syscall.ByHandleFileInformation{NumberOfLinks: 1}
	if err := validateWindowsHandleInformation(regular, 1, false); err != nil {
		t.Fatalf("regular file: %v", err)
	}
	reparse := regular
	reparse.FileAttributes = syscall.FILE_ATTRIBUTE_REPARSE_POINT
	if err := validateWindowsHandleInformation(reparse, 1, false); !errors.Is(err, ErrSymlink) {
		t.Fatalf("reparse error = %v, want ErrSymlink", err)
	}
	multiple := regular
	multiple.NumberOfLinks = 2
	if err := validateWindowsHandleInformation(multiple, 1, false); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("multiple-link error = %v, want ErrMultipleLinks", err)
	}
	directory := syscall.ByHandleFileInformation{FileAttributes: syscall.FILE_ATTRIBUTE_DIRECTORY, NumberOfLinks: 1}
	if err := validateWindowsHandleInformation(directory, 0, true); err != nil {
		t.Fatalf("directory: %v", err)
	}
	directory.NumberOfLinks = 2
	if err := validateWindowsHandleInformation(directory, 0, true); !errors.Is(err, ErrMultipleLinks) {
		t.Fatalf("multiple-link directory error = %v, want ErrMultipleLinks", err)
	}
	if err := validateWindowsHandleInformation(regular, 0, true); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("non-directory error = %v, want ErrInvalidPath", err)
	}
}
