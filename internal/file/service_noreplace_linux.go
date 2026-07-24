//go:build linux

package file

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	renameNoReplaceFlag = 1
	atFDCWD             = ^uintptr(99) // uintptr(-100)
)

// AtomicNoReplaceSupported reports whether this build can issue renameat2 with
// RENAME_NOREPLACE. A supported build can still discover an old kernel or
// filesystem at publication time; that is reported as ErrNoReplaceUnsupported.
func AtomicNoReplaceSupported() bool {
	_, ok := renameat2SyscallNumber()
	return ok
}

// renameNoReplace invokes Linux renameat2 with RENAME_NOREPLACE. The syscall
// package is used here rather than a check-then-rename sequence so competing
// conversions can never overwrite one another. Supported production Linux
// architectures provide their respective renameat2 syscall number separately.
func platformRenameNoReplace(oldPath, newPath string) error {
	oldPathPtr, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return ErrSaveFailed
	}
	newPathPtr, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return ErrSaveFailed
	}

	number, ok := renameat2SyscallNumber()
	if !ok {
		return ErrNoReplaceUnsupported
	}
	_, _, errno := syscall.Syscall6(number,
		atFDCWD, uintptr(unsafe.Pointer(oldPathPtr)),
		atFDCWD, uintptr(unsafe.Pointer(newPathPtr)),
		uintptr(renameNoReplaceFlag), 0)
	if errno == 0 {
		return nil
	}
	if errors.Is(errno, syscall.EEXIST) {
		return ErrFileAlreadyExists
	}
	if errors.Is(errno, syscall.ENOSYS) ||
		errors.Is(errno, syscall.EINVAL) ||
		errors.Is(errno, syscall.EOPNOTSUPP) {
		return ErrNoReplaceUnsupported
	}
	return ErrSaveFailed
}
