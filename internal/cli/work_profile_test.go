package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baron-cli/baron/internal/store"
)

// writeGoMod creates a minimal go.mod under dir so profile.Detect resolves
// the Go profile (its auto-detection reads the filesystem, so tests of
// config-layered profiles still need a real project to layer onto).
func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestProfileFromStoreOverridesOneStepKeepsRest: customizing just `test` in
// [profiles.go] must not silently drop steps the flat schema has no field
// for (Go's "tidy") or steps left unset (format, lint, build stay
// auto-detected) — the wholesale-replace bug harness-hardening.md §2
// documents fixing.
func TestProfileFromStoreOverridesOneStepKeepsRest(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)

	p := profileFromStore("go", store.Profile{Test: "gotestsum ./..."}, dir)

	steps := map[string][]string{}
	for _, s := range p.Steps {
		steps[s.Name] = append([]string{s.Command}, s.Args...)
	}
	if _, ok := steps["tidy"]; !ok {
		t.Errorf("Steps = %+v, want \"tidy\" preserved from auto-detection", p.Steps)
	}
	if _, ok := steps["format"]; !ok {
		t.Errorf("Steps = %+v, want \"format\" preserved from auto-detection", p.Steps)
	}
	got := steps["test"]
	want := []string{"gotestsum", "./..."}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("test step = %v, want %v (the override)", got, want)
	}
}

// TestProfileFromStoreAppendsUnknownStep: a field naming a step
// auto-detection didn't produce at all (typecheck, for a Go project) is
// appended rather than silently dropped.
func TestProfileFromStoreAppendsUnknownStep(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)

	p := profileFromStore("go", store.Profile{Typecheck: "some-go-typechecker ./..."}, dir)

	found := false
	for _, s := range p.Steps {
		if s.Name == "typecheck" {
			found = true
			if s.Command != "some-go-typechecker" {
				t.Errorf("typecheck command = %q, want %q", s.Command, "some-go-typechecker")
			}
		}
	}
	if !found {
		t.Errorf("Steps = %+v, want an appended \"typecheck\" step", p.Steps)
	}
}

// TestProfileFromStoreStepsReplacesWholesale: [[profiles.<name>.steps]]
// (a fully custom pipeline) replaces auto-detection and the flat fields
// entirely — the "no Go code needed for a new language" escape hatch.
func TestProfileFromStoreStepsReplacesWholesale(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir) // present but must be ignored once Steps is set

	p := profileFromStore("python", store.Profile{
		Formatter: "should-be-ignored",
		Steps: []store.GateStepConfig{
			{Name: "lint", Command: "ruff", Args: []string{"check", "."}},
			{Name: "test", Command: "pytest", Timeout: 300},
		},
	}, dir)

	if len(p.Steps) != 2 {
		t.Fatalf("Steps = %+v, want exactly the 2 configured steps", p.Steps)
	}
	if p.Steps[0].Name != "lint" || p.Steps[0].Command != "ruff" {
		t.Errorf("Steps[0] = %+v, want the configured lint step", p.Steps[0])
	}
	if p.Steps[1].Name != "test" || p.Steps[1].Command != "pytest" || p.Steps[1].Timeout != 300 {
		t.Errorf("Steps[1] = %+v, want the configured test step with timeout 300", p.Steps[1])
	}
}

// TestProfileByNameFallsBackToBuiltinWhenDefault: a config carrying only
// the shipped placeholder values (what `baron init` writes) must not
// shadow the richer built-in profile (Go's "tidy" step, react-ts's
// auto-detected linter/test-runner).
func TestProfileByNameFallsBackToBuiltinWhenDefault(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	cfg := store.DefaultConfig() // ships the placeholder [profiles.go]/[profiles.react-ts]

	p := profileByName(cfg, "go", dir)

	found := false
	for _, s := range p.Steps {
		if s.Name == "tidy" {
			found = true
		}
	}
	if !found {
		t.Errorf("Steps = %+v, want the built-in Go profile (with \"tidy\"), not the placeholder default", p.Steps)
	}
}

// TestProfileByNameRustNoConfigUsesBuiltinRust: a profile name with no
// [profiles.<name>] section at all (not even a placeholder) must resolve
// through the built-in language registry, not silently default to Go — the
// bug this fixes would have run `go build` against a Rust project.
func TestProfileByNameRustNoConfigUsesBuiltinRust(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := store.DefaultConfig()

	p := profileByName(cfg, "rust", dir)

	if p.Name != "rust" {
		t.Errorf("profileByName(rust) = %q, want %q", p.Name, "rust")
	}
}

// TestProfileByNameUsesInitExportedFile: end to end — after Init exports
// go.toml into .baron/profiles/, a fresh config load must resolve "go"
// through that file (the wholesale-replace Steps path), not by re-running
// profile.Go's live detection, since the whole point of exporting is that
// the file becomes the authoritative, user-editable pipeline from then on.
func TestProfileByNameUsesInitExportedFile(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}

	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	p := profileByName(cfg, "go", a.dir)
	if len(p.Steps) == 0 {
		t.Fatal("Steps empty, want the exported go.toml's steps")
	}
	if p.Steps[0].Command != "gofmt" {
		t.Errorf("Steps[0].Command = %q, want gofmt (from the exported file)", p.Steps[0].Command)
	}
}
