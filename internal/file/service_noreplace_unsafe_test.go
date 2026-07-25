package file

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveFileNoReplaceUnsafePublishesStagedContent(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)
	target := "result.txt"

	if err := service.SaveFileNoReplaceUnsafe(target, bytes.NewBufferString("result")); err != nil {
		t.Fatalf("SaveFileNoReplaceUnsafe() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, target))
	if err != nil || string(content) != "result" {
		t.Fatalf("published content = %q, %v", content, err)
	}
}

func TestSaveFileNoReplaceUnsafeRejectsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)
	target := "result.txt"
	if err := os.WriteFile(filepath.Join(dir, target), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := service.SaveFileNoReplaceUnsafe(target, bytes.NewBufferString("replacement"))
	if !errors.Is(err, ErrFileAlreadyExists) {
		t.Fatalf("SaveFileNoReplaceUnsafe() error = %v, want ErrFileAlreadyExists", err)
	}
	content, readErr := os.ReadFile(filepath.Join(dir, target))
	if readErr != nil || string(content) != "existing" {
		t.Fatalf("existing content = %q, %v", content, readErr)
	}
}

func TestSaveFileFirstAvailableUnsafeUsesFirstUnoccupiedCandidate(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)
	first := "result.txt"
	second := "result (converted).txt"
	if err := os.WriteFile(filepath.Join(dir, first), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	selected, err := service.SaveFileFirstAvailableUnsafe([]string{first, second}, bytes.NewBufferString("result"))
	if err != nil {
		t.Fatalf("SaveFileFirstAvailableUnsafe() error = %v", err)
	}
	if selected != second {
		t.Fatalf("selected path = %q, want %q", selected, second)
	}
	content, err := os.ReadFile(filepath.Join(dir, second))
	if err != nil || string(content) != "result" {
		t.Fatalf("published content = %q, %v", content, err)
	}
}

func TestSaveFileNoReplaceWithFallbackAttemptsAtomicBeforeCompatibility(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)

	originalRename := renameNoReplace
	attempts := 0
	renameNoReplace = func(string, string) error {
		attempts++
		return ErrNoReplaceUnsupported
	}
	t.Cleanup(func() { renameNoReplace = originalRename })

	if err := service.SaveFileNoReplaceWithFallback("result.txt", bytes.NewBufferString("result"), true); err != nil {
		t.Fatalf("SaveFileNoReplaceWithFallback() error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("atomic publication attempts = %d, want 1", attempts)
	}
	content, err := os.ReadFile(filepath.Join(dir, "result.txt"))
	if err != nil || string(content) != "result" {
		t.Fatalf("fallback content = %q, %v", content, err)
	}
}

func TestSaveFileNoReplaceWithFallbackUsesAtomicWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)

	originalRename := renameNoReplace
	atomicCalls := 0
	renameNoReplace = func(oldPath, newPath string) error {
		atomicCalls++
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { renameNoReplace = originalRename })

	if err := service.SaveFileNoReplaceWithFallback("result.txt", bytes.NewBufferString("result"), true); err != nil {
		t.Fatalf("SaveFileNoReplaceWithFallback() error = %v", err)
	}
	if atomicCalls != 1 {
		t.Fatalf("atomic publication calls = %d, want 1", atomicCalls)
	}
}

func TestSaveFileNoReplaceWithFallbackRequiresConsent(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)

	originalRename := renameNoReplace
	renameNoReplace = func(string, string) error { return ErrNoReplaceUnsupported }
	t.Cleanup(func() { renameNoReplace = originalRename })

	err := service.SaveFileNoReplaceWithFallback("result.txt", bytes.NewBufferString("result"), false)
	if !errors.Is(err, ErrNoReplaceUnsupported) {
		t.Fatalf("SaveFileNoReplaceWithFallback() error = %v, want ErrNoReplaceUnsupported", err)
	}
}

func TestSaveFileFirstAvailableWithFallbackUsesCompatibilitySelection(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, 0)
	first := "result.txt"
	second := "result (converted).txt"
	if err := os.WriteFile(filepath.Join(dir, first), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	originalRename := renameNoReplace
	renameNoReplace = func(string, string) error { return ErrNoReplaceUnsupported }
	t.Cleanup(func() { renameNoReplace = originalRename })

	selected, err := service.SaveFileFirstAvailableWithFallback([]string{first, second}, bytes.NewBufferString("result"), true)
	if err != nil {
		t.Fatalf("SaveFileFirstAvailableWithFallback() error = %v", err)
	}
	if selected != second {
		t.Fatalf("selected path = %q, want %q", selected, second)
	}
	content, err := os.ReadFile(filepath.Join(dir, second))
	if err != nil || string(content) != "result" {
		t.Fatalf("fallback content = %q, %v", content, err)
	}
}
