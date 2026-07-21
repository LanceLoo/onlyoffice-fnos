package file

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSaveFileNoReplacePreservesExistingTarget(t *testing.T) {
	if !noReplaceSupportedForTest() {
		t.Skip("atomic no-replace publication is only supported on Linux")
	}

	dir := t.TempDir()
	service := NewService(dir, 0)
	target := filepath.Join(dir, "result.docx")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	err := service.SaveFileNoReplace(target, bytes.NewBufferString("replacement"))
	if !errors.Is(err, ErrFileAlreadyExists) {
		t.Fatalf("SaveFileNoReplace() error = %v, want ErrFileAlreadyExists", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("target content = %q, want original content", got)
	}
}

func TestSaveFileNoReplaceConcurrent(t *testing.T) {
	if !noReplaceSupportedForTest() {
		t.Skip("atomic no-replace publication is only supported on Linux")
	}

	dir := t.TempDir()
	service := NewService(dir, 0)
	target := filepath.Join(dir, "result.docx")
	const writers = 16
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- service.SaveFileNoReplace(target, bytes.NewBufferString("result"))
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrFileAlreadyExists):
			conflicts++
		default:
			t.Fatalf("SaveFileNoReplace() error = %v", err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("successes = %d, conflicts = %d; want 1 and %d", successes, conflicts, writers-1)
	}
}

func TestSaveFileNoReplacePublishesStagedContent(t *testing.T) {
	if !noReplaceSupportedForTest() {
		t.Skip("atomic no-replace publication is only supported on Linux")
	}

	dir := t.TempDir()
	service := NewService(dir, 0)
	target := filepath.Join(dir, "result.docx")
	if err := service.SaveFileNoReplace(target, bytes.NewBufferString("result")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "result" {
		t.Fatalf("target content = %q, want result content", got)
	}
}

func TestSaveFileFirstAvailable(t *testing.T) {
	if !noReplaceSupportedForTest() {
		t.Skip("atomic no-replace publication is only supported on Linux")
	}

	dir := t.TempDir()
	service := NewService(dir, 0)
	first := filepath.Join(dir, "result.docx")
	second := filepath.Join(dir, "result (1).docx")
	if err := os.WriteFile(first, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := service.SaveFileFirstAvailable([]string{first, second}, bytes.NewBufferString("result"))
	if err != nil {
		t.Fatal(err)
	}
	if got != second {
		t.Fatalf("selected path = %q, want %q", got, second)
	}
	content, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "result" {
		t.Fatalf("selected content = %q, want result content", content)
	}
}

func TestSaveFileMaxInt64Limit(t *testing.T) {
	var output bytes.Buffer
	if err := copyWithLimit(&output, bytes.NewBufferString("result"), math.MaxInt64); err != nil {
		t.Fatalf("copyWithLimit() error = %v", err)
	}
	if output.String() != "result" {
		t.Fatalf("copied content = %q, want result content", output.String())
	}
}
