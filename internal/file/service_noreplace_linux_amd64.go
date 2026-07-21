//go:build linux && amd64

package file

func renameat2SyscallNumber() (uintptr, bool) {
	return 316, true // __NR_renameat2
}
