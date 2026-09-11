package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectContextMissingFile(t *testing.T) {
	if got := ProjectContext(t.TempDir()); got != "" {
		t.Errorf("ProjectContext() = %q, want empty when .baron/prompt.md doesn't exist", got)
	}
}

func TestProjectContextReadsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".baron"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".baron", "prompt.md"), []byte("Always use tabs, never spaces."), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ProjectContext(dir)
	if got != "Always use tabs, never spaces.\n\n" {
		t.Errorf("ProjectContext() = %q, want the file's content plus a blank-line separator", got)
	}
}

func TestProjectContextEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".baron"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".baron", "prompt.md"), []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProjectContext(dir); got != "" {
		t.Errorf("ProjectContext() = %q, want empty for a whitespace-only file", got)
	}
}
