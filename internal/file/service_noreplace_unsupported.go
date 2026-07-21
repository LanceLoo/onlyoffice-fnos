//go:build !linux

package file

func renameNoReplace(oldPath, newPath string) error {
	return ErrNoReplaceUnsupported
}
