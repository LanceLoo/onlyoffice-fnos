package file

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrFileNotFound     = errors.New("file not found")
	ErrInvalidPath      = errors.New("invalid file path")
	ErrPermissionDenied = errors.New("permission denied")
	ErrSaveFailed       = errors.New("failed to save file")
	ErrFileTooLarge     = errors.New("file size exceeds limit")
	// ErrFileAlreadyExists indicates that an atomic no-replace save found an
	// existing target file.
	ErrFileAlreadyExists = errors.New("file already exists")
	// ErrNoAvailableName indicates that none of the supplied candidate paths
	// could be published without replacing an existing file.
	ErrNoAvailableName = errors.New("no available file name")
	// ErrNoReplaceUnsupported indicates that the current platform cannot
	// atomically publish a file without replacing an existing target.
	ErrNoReplaceUnsupported = errors.New("atomic no-replace save is unsupported on this platform")
)

// FileInfo represents information about a file
type FileInfo struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Extension string    `json:"extension"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"modTime"`
}

// Service handles file operations for fnOS file system
type Service struct {
	// basePath is the root path for file operations (optional, for security)
	basePath string
	// maxFileSize is the maximum allowed file size in bytes (0 = no limit)
	maxFileSize int64
}

// NewService creates a new FileService
func NewService(basePath string, maxFileSize int64) *Service {
	return &Service{
		basePath:    basePath,
		maxFileSize: maxFileSize,
	}
}

// GetFileInfo returns information about a file
func (s *Service) GetFileInfo(path string) (*FileInfo, error) {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		if os.IsPermission(err) {
			return nil, ErrPermissionDenied
		}
		return nil, err
	}

	if stat.IsDir() {
		return nil, ErrInvalidPath
	}

	ext := filepath.Ext(stat.Name())
	if ext != "" {
		ext = strings.ToLower(ext[1:]) // Remove leading dot and lowercase
	}

	return &FileInfo{
		Path:      path,
		Name:      stat.Name(),
		Extension: ext,
		Size:      stat.Size(),
		ModTime:   stat.ModTime(),
	}, nil
}

// GetFileContent returns a reader for the file content
func (s *Service) GetFileContent(path string) (io.ReadCloser, error) {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		if os.IsPermission(err) {
			return nil, ErrPermissionDenied
		}
		return nil, err
	}

	return file, nil
}

// Exists returns true if the file exists at the given path
func (s *Service) Exists(path string) (bool, error) {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SaveFile saves content to a file
func (s *Service) SaveFile(path string, content io.Reader) error {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ErrSaveFailed
	}

	// Create temporary file in the same directory
	tempFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return ErrSaveFailed
	}
	tempPath := tempFile.Name()
	defer func() {
		tempFile.Close()
		os.Remove(tempPath) // Clean up temp file on error
	}()

	// Copy content to temp file with size limit check.
	if err := copyWithLimit(tempFile, content, s.maxFileSize); err != nil {
		return err
	}

	// Close temp file before rename
	if err := tempFile.Close(); err != nil {
		return ErrSaveFailed
	}

	// Atomic rename
	if err := os.Rename(tempPath, fullPath); err != nil {
		return ErrSaveFailed
	}

	return nil
}

// CanonicalPath resolves a logical path using the same base-path and fnOS
// volume rules enforced by all file operations.
func (s *Service) CanonicalPath(path string) (string, error) {
	return s.resolvePath(path)
}

// copyWithLimit copies content while enforcing maxFileSize. math.MaxInt64 is
// already the largest representable file size, so adding one to probe for an
// overflow would itself overflow; in that case an unrestricted copy preserves
// the effective limit semantics.
func copyWithLimit(dst io.Writer, content io.Reader, maxFileSize int64) error {
	if maxFileSize <= 0 || maxFileSize == math.MaxInt64 {
		if _, err := io.Copy(dst, content); err != nil {
			return ErrSaveFailed
		}
		return nil
	}

	written, err := io.CopyN(dst, content, maxFileSize+1)
	if written > maxFileSize {
		return ErrFileTooLarge
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrSaveFailed
	}
	return nil
}

// SaveFileNoReplace saves content to path only when path does not already
// exist. On Linux, publication is atomic: concurrent writers cannot replace
// each other's result.
func (s *Service) SaveFileNoReplace(path string, content io.Reader) error {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return err
	}

	return s.saveFileNoReplace(fullPath, content)
}

// SaveFileNoReplaceWithFallback atomically publishes content without replacing
// path when possible. If the atomic primitive is unavailable at publication
// time, allowUnsafeFallback permits the explicitly non-atomic compatibility
// fallback. Content is staged only once, so fallback does not re-read it.
func (s *Service) SaveFileNoReplaceWithFallback(path string, content io.Reader, allowUnsafeFallback bool) error {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return err
	}

	tempPath, cleanup, err := s.stageFile(filepath.Dir(fullPath), content)
	if err != nil {
		return err
	}
	defer cleanup()

	err = renameNoReplace(tempPath, fullPath)
	if errors.Is(err, ErrNoReplaceUnsupported) && allowUnsafeFallback {
		return renameIfUnoccupied(tempPath, fullPath)
	}
	return err
}

// SaveFileNoReplaceUnsafe saves content only when path was unoccupied at the
// time it was checked. It stages content before publication, but the existence
// check and os.Rename are necessarily non-atomic. A concurrent writer can
// create path after the check and have its file replaced by os.Rename.
// Callers must obtain explicit user consent before using this compatibility
// fallback.
func (s *Service) SaveFileNoReplaceUnsafe(path string, content io.Reader) error {
	fullPath, err := s.resolvePath(path)
	if err != nil {
		return err
	}

	tempPath, cleanup, err := s.stageFile(filepath.Dir(fullPath), content)
	if err != nil {
		return err
	}
	defer cleanup()

	return renameIfUnoccupied(tempPath, fullPath)
}

// SaveFileFirstAvailable saves content to the first candidate that can be
// atomically published without replacing an existing file. It returns the
// candidate path (as supplied) that was selected.
func (s *Service) SaveFileFirstAvailable(candidates []string, content io.Reader) (string, error) {
	fullPaths, err := s.resolveCandidates(candidates)
	if err != nil {
		return "", err
	}

	tempPath, cleanup, err := s.stageFile(filepath.Dir(fullPaths[0]), content)
	if err != nil {
		return "", err
	}
	defer cleanup()

	for i, candidate := range candidates {
		err = renameNoReplace(tempPath, fullPaths[i])
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, ErrFileAlreadyExists) {
			continue
		}
		return "", err
	}

	return "", ErrNoAvailableName
}

// SaveFileFirstAvailableWithFallback atomically publishes content to the first
// available candidate when possible. If atomic publication is unavailable at
// runtime, allowUnsafeFallback permits compatibility publication using the
// same staged file rather than reading content again.
func (s *Service) SaveFileFirstAvailableWithFallback(candidates []string, content io.Reader, allowUnsafeFallback bool) (string, error) {
	fullPaths, err := s.resolveCandidates(candidates)
	if err != nil {
		return "", err
	}

	tempPath, cleanup, err := s.stageFile(filepath.Dir(fullPaths[0]), content)
	if err != nil {
		return "", err
	}
	defer cleanup()

	for i, candidate := range candidates {
		err = renameNoReplace(tempPath, fullPaths[i])
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, ErrFileAlreadyExists) {
			continue
		}
		if errors.Is(err, ErrNoReplaceUnsupported) && allowUnsafeFallback {
			return renameFirstUnoccupied(tempPath, candidates, fullPaths)
		}
		return "", err
	}

	return "", ErrNoAvailableName
}

// SaveFileFirstAvailableUnsafe saves content to the first candidate that was
// unoccupied when checked. As with SaveFileNoReplaceUnsafe, there is a
// check-to-rename race: os.Rename can replace a candidate concurrently created
// after its existence check. Callers must obtain explicit user consent before
// using this compatibility fallback.
func (s *Service) SaveFileFirstAvailableUnsafe(candidates []string, content io.Reader) (string, error) {
	fullPaths, err := s.resolveCandidates(candidates)
	if err != nil {
		return "", err
	}

	tempPath, cleanup, err := s.stageFile(filepath.Dir(fullPaths[0]), content)
	if err != nil {
		return "", err
	}
	defer cleanup()

	return renameFirstUnoccupied(tempPath, candidates, fullPaths)
}

func (s *Service) resolveCandidates(candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, ErrNoAvailableName
	}

	fullPaths := make([]string, len(candidates))
	for i, candidate := range candidates {
		fullPath, err := s.resolvePath(candidate)
		if err != nil {
			return nil, err
		}
		if i > 0 && filepath.Dir(fullPath) != filepath.Dir(fullPaths[0]) {
			return nil, ErrInvalidPath
		}
		fullPaths[i] = fullPath
	}
	return fullPaths, nil
}

func renameFirstUnoccupied(tempPath string, candidates, fullPaths []string) (string, error) {
	for i, candidate := range candidates {
		err := renameIfUnoccupied(tempPath, fullPaths[i])
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, ErrFileAlreadyExists) {
			continue
		}
		return "", err
	}
	return "", ErrNoAvailableName
}

func renameIfUnoccupied(oldPath, newPath string) error {
	// Lstat treats a dangling symlink as occupied too; os.Rename would replace
	// that directory entry even though Stat reports it as not existing.
	_, err := os.Lstat(newPath)
	if err == nil {
		return ErrFileAlreadyExists
	}
	if !os.IsNotExist(err) {
		return ErrSaveFailed
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		if errors.Is(err, os.ErrExist) || os.IsExist(err) {
			return ErrFileAlreadyExists
		}
		return ErrSaveFailed
	}
	return nil
}

// resolvePath resolves and validates the file path
func (s *Service) resolvePath(path string) (string, error) {
	if path == "" {
		return "", ErrInvalidPath
	}

	if s.basePath != "" {
		candidate := path
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(s.basePath, candidate)
		}

		absPath, err := filepath.Abs(filepath.Clean(candidate))
		if err != nil {
			return "", ErrInvalidPath
		}

		absBase, err := filepath.Abs(filepath.Clean(s.basePath))
		if err != nil {
			return "", ErrInvalidPath
		}

		rel, err := filepath.Rel(absBase, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", ErrInvalidPath
		}

		return absPath, nil
	}

	// Preserve fnOS compatibility: paths without a leading slash are logical
	// volume paths such as vol2/... and therefore refer to /vol2/....
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return filepath.Clean(path), nil
}

// GetBasePath returns the base path for file operations
func (s *Service) GetBasePath() string {
	return s.basePath
}
