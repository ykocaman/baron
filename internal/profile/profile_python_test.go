package profile

import "testing"

func TestPythonProfileMinimal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "requests==2.31.0\n")

	p := Python(dir)
	if p.Name != "python" {
		t.Errorf("Name = %q, want %q", p.Name, "python")
	}
	// No pyproject.toml (no formatter/lint signal): install + test only.
	want := []struct{ name, cmd string }{
		{"install", "pip"},
		{"test", "pytest"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name || p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d] = %s %s, want %s %s", i, p.Steps[i].Name, p.Steps[i].Command, w.name, w.cmd)
		}
	}
}

func TestPythonProfileRuff(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.ruff]\nline-length = 100\n")
	writeFile(t, dir, "poetry.lock", "")

	p := Python(dir)
	want := []struct{ name, cmd string }{
		{"install", "poetry"},
		{"format", "ruff"},
		{"lint", "ruff"},
		{"test", "pytest"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name || p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d] = %s %s, want %s %s", i, p.Steps[i].Name, p.Steps[i].Command, w.name, w.cmd)
		}
	}
	if got := p.Steps[1].Args; len(got) != 3 || got[0] != "format" || got[1] != "--check" {
		t.Errorf("format args = %v, want [format --check .]", got)
	}
}

func TestPythonProfileBlackFlake8Mypy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.mypy]\nstrict = true\n")
	writeFile(t, dir, ".flake8", "[flake8]\nmax-line-length = 100\n")

	p := Python(dir)
	want := []struct{ name, cmd string }{
		{"format", "black"},
		{"lint", "flake8"},
		{"typecheck", "mypy"},
		{"test", "pytest"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name || p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d] = %s %s, want %s %s", i, p.Steps[i].Name, p.Steps[i].Command, w.name, w.cmd)
		}
	}
}

func TestPythonProfileInstaller(t *testing.T) {
	tests := []struct {
		file    string
		wantCmd string
	}{
		{"poetry.lock", "poetry"},
		{"Pipfile", "pipenv"},
		{"uv.lock", "uv"},
		{"requirements.txt", "pip"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tt.file, "")
			install := Python(dir).Steps[0]
			if install.Name != "install" || install.Command != tt.wantCmd {
				t.Errorf("install step = %+v, want command %q", install, tt.wantCmd)
			}
		})
	}
}

// TestPythonProfileInstallerPriority: with several manifests present at
// once, poetry.lock wins over a bare requirements.txt.
func TestPythonProfileInstallerPriority(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "poetry.lock", "")
	writeFile(t, dir, "requirements.txt", "")
	install := Python(dir).Steps[0]
	if install.Command != "poetry" {
		t.Errorf("install command = %q, want poetry (priority order)", install.Command)
	}
}

func TestDetectPython(t *testing.T) {
	dir := t.TempDir()
	if detectPython(dir) {
		t.Error("detectPython(empty dir) = true, want false")
	}
	writeFile(t, dir, "setup.py", "")
	if !detectPython(dir) {
		t.Error("detectPython(with setup.py) = false, want true")
	}
}
