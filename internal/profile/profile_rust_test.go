package profile

import "testing"

func TestRustProfileSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")

	p := Rust(dir)
	if p.Name != "rust" {
		t.Errorf("Name = %q, want %q", p.Name, "rust")
	}
	want := []struct{ name, cmd string }{
		{"format", "cargo"},
		{"lint", "cargo"},
		{"test", "cargo"},
		{"build", "cargo"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name || p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d] = %s %s, want %s %s", i, p.Steps[i].Name, p.Steps[i].Command, w.name, w.cmd)
		}
	}
	if got := p.Steps[0].Args; len(got) != 2 || got[0] != "fmt" || got[1] != "--check" {
		t.Errorf("format args = %v, want [fmt --check]", got)
	}
}

func TestDetectRust(t *testing.T) {
	dir := t.TempDir()
	if detectRust(dir) {
		t.Error("detectRust(empty dir) = true, want false")
	}
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
	if !detectRust(dir) {
		t.Error("detectRust(with Cargo.toml) = false, want true")
	}
}
