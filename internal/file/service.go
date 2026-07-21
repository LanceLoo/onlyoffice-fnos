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

// SaveFileFirstAvailable saves content to the first candidate that can be
// atomically published without replacing an existing file. It returns the
// candidate path (as supplied) that was selected.
func (s *Service) SaveFileFirstAvailable(candidates []string, content io.Reader) (string, error) {
	if len(candidates) == 0 {
		return "", ErrNoAvailableName
	}

	fullPaths := make([]string, len(candidates))
	for i, candidate := range candidates {
		fullPath, err := s.resolvePath(candidate)
		if err != nil {
			return "", err
		}
		if i > 0 && filepath.Dir(fullPath) != filepath.Dir(fullPaths[0]) {
			return "", ErrInvalidPath
		}
		fullPaths[i] = fullPath
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

// resolvePath resolves and validates the file path
func (s *Service) resolvePath(path string) (string, error) {
	if path == "" {
		return "", ErrInvalidPath
	}

	// Normalize path: ensure it starts with "/" for consistency
	// This handles the difference between iPad (vol2/...) and desktop (/vol2/...)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// If basePath is set, ensure the path is within it
	if s.basePath != "" {
		// If path is relative, join with basePath
		if !filepath.IsAbs(cleanPath) {
			cleanPath = filepath.Join(s.basePath, cleanPath)
		}

		// Ensure the resolved path is within basePath
		absPath, err := filepath.Abs(cleanPath)
		if err != nil {
			return "", ErrInvalidPath
		}

		absBase, err := filepath.Abs(s.basePath)
		if err != nil {
			return "", ErrInvalidPath
		}

		// Check for path traversal
		if !strings.HasPrefix(absPath, absBase) {
			return "", ErrInvalidPath
		}

		return absPath, nil
	}

	// If no basePath, just return the cleaned path
	return cleanPath, nil
}

// GetBasePath returns the base path for file operations
func (s *Service) GetBasePath() string {
	return s.basePath
}
