package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates a file under dir, creating parent directories.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDetectProfileGo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.23\n")
	if p := Detect(dir); p.Name != "go" {
		t.Errorf("DetectProfile = %q, want %q", p.Name, "go")
	}
}

func TestDetectProfileReactTS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name": "web"}`)
	if p := Detect(dir); p.Name != "react-ts" {
		t.Errorf("DetectProfile = %q, want %q", p.Name, "react-ts")
	}
}

func TestDetectProfileRust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
	if p := Detect(dir); p.Name != "rust" {
		t.Errorf("DetectProfile = %q, want %q", p.Name, "rust")
	}
}

func TestDetectProfilePHP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)
	if p := Detect(dir); p.Name != "php" {
		t.Errorf("DetectProfile = %q, want %q", p.Name, "php")
	}
}

func TestDetectProfilePython(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"test\"\n")
	if p := Detect(dir); p.Name != "python" {
		t.Errorf("DetectProfile = %q, want %q", p.Name, "python")
	}
}

func TestDetectProfileDefault(t *testing.T) {
	if p := Detect(t.TempDir()); p.Name != "go" {
		t.Errorf("DetectProfile = %q, want default %q", p.Name, "go")
	}
}

// TestDetectProfileOrderGoWinsOverRust: a directory matching more than one
// detector (e.g. a Go service vendoring a Rust build tool) resolves to the
// earlier registry entry, not the last one checked.
func TestDetectProfileOrderGoWinsOverRust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.23\n")
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
	if p := Detect(dir); p.Name != "go" {
		t.Errorf("DetectProfile = %q, want %q (registry order)", p.Name, "go")
	}
}

func TestByNameKnownLanguage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
	if p := ByName("rust", dir); p.Name != "rust" {
		t.Errorf("ByName(rust) = %q, want %q", p.Name, "rust")
	}
}

// TestByNameUnknownFallsBackToDetect: an unrecognized profile name (e.g. a
// typo, or a name with no [profiles.<name>] override either) still gets a
// working pipeline for what's actually in dir instead of silently
// defaulting to Go.
func TestByNameUnknownFallsBackToDetect(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)
	if p := ByName("nonexistent", dir); p.Name != "php" {
		t.Errorf("ByName(nonexistent) = %q, want the detected %q", p.Name, "php")
	}
}
