package file

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolvePathWithinBasePath(t *testing.T) {
	base := t.TempDir()
	service := NewService(base, 0)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "relative path",
			input: "result.txt",
			want:  filepath.Join(base, "result.txt"),
		},
		{
			name:  "absolute path within base",
			input: filepath.Join(base, "nested", "result.txt"),
			want:  filepath.Join(base, "nested", "result.txt"),
		},
		{
			name:  "cleaned path within base",
			input: "nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + "result.txt",
			want:  filepath.Join(base, "result.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.resolvePath(tt.input)
			if err != nil {
				t.Fatalf("resolvePath(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("resolvePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolvePathRejectsPathsOutsideBasePath(t *testing.T) {
	base := t.TempDir()
	service := NewService(base, 0)
	parent := filepath.Dir(base)
	sibling := filepath.Join(parent, filepath.Base(base)+"-sibling", "result.txt")

	for _, input := range []string{
		".." + string(filepath.Separator) + "result.txt",
		filepath.Join(parent, "outside", "result.txt"),
		sibling,
	} {
		if _, err := service.resolvePath(input); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("resolvePath(%q) error = %v, want ErrInvalidPath", input, err)
		}
	}
}

func TestCanonicalPathUsesFileServiceRules(t *testing.T) {
	base := t.TempDir()
	service := NewService(base, 0)
	relative, err := service.CanonicalPath(filepath.Join("nested", "..", "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := service.CanonicalPath(filepath.Join(base, "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if relative != absolute {
		t.Fatalf("equivalent paths differ: %q != %q", relative, absolute)
	}
	if _, err := service.CanonicalPath(filepath.Join(base, "..", "outside.txt")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("outside path error = %v, want ErrInvalidPath", err)
	}
}
