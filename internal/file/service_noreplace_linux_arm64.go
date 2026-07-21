//go:build linux && arm64

package file

func renameat2SyscallNumber() (uintptr, bool) {
	return 276, true // __NR_renameat2
}
