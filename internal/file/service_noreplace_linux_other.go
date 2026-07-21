//go:build linux && !amd64 && !arm64

package file

func renameat2SyscallNumber() (uintptr, bool) {
	return 0, false
}
