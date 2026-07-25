//go:build !linux

package file

// AtomicNoReplaceSupported reports whether this build can issue an atomic
// no-replace publication primitive.
func AtomicNoReplaceSupported() bool { return false }

func platformRenameNoReplace(oldPath, newPath string) error {
	return ErrNoReplaceUnsupported
}
