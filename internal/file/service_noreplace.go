package file

import (
	"io"
	"os"
	"path/filepath"
)

// renameNoReplace is a seam around the platform implementation. Keeping the
// staging and fallback decision in Service makes a runtime lack of renameat2
// support distinct from a compile-time lack of support.
var renameNoReplace = platformRenameNoReplace

// saveFileNoReplace stages content in the target directory before platform
// specific code publishes it. Keeping staging here guarantees the same size
// limit behavior as SaveFile and avoids partially-written target files.
func (s *Service) saveFileNoReplace(fullPath string, content io.Reader) error {
	dir := filepath.Dir(fullPath)
	tempPath, cleanup, err := s.stageFile(dir, content)
	if err != nil {
		return err
	}
	defer cleanup()

	return renameNoReplace(tempPath, fullPath)
}

// stageFile returns a closed staged file and transfers cleanup ownership to the
// caller. The file lives in a private directory below the target directory, so
// its name cannot be substituted between staging and publication.
func (s *Service) stageFile(dir string, content io.Reader) (string, func(), error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", nil, ErrSaveFailed
	}

	tempDir, err := os.MkdirTemp(dir, ".tmp-*")
	if err != nil {
		return "", nil, ErrSaveFailed
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	tempFile, err := os.CreateTemp(tempDir, "content-*")
	if err != nil {
		cleanup()
		return "", nil, ErrSaveFailed
	}
	tempPath := tempFile.Name()
	closed := false
	success := false
	defer func() {
		if !closed {
			_ = tempFile.Close()
		}
		if !success {
			cleanup()
		}
	}()

	if err := copyWithLimit(tempFile, content, s.maxFileSize); err != nil {
		return "", nil, err
	}

	if err := tempFile.Close(); err != nil {
		return "", nil, ErrSaveFailed
	}
	closed = true
	success = true

	return tempPath, cleanup, nil
}
