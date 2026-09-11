package profile

import "testing"

func TestGoProfileSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.23\n")

	p := Go(dir)
	if p.Name != "go" {
		t.Errorf("Name = %q, want %q", p.Name, "go")
	}
	want := []struct{ name, cmd string }{
		{"format", "gofmt"},
		{"lint", "go"}, // no .golangci.yml in temp dir
		{"tidy", "go"},
		{"test", "go"},
		{"build", "go"},
	}
	if len(p.Steps) != len(want) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(p.Steps), len(want), p.Steps)
	}
	for i, w := range want {
		if p.Steps[i].Name != w.name {
			t.Errorf("Steps[%d].Name = %q, want %q", i, p.Steps[i].Name, w.name)
		}
		if p.Steps[i].Command != w.cmd {
			t.Errorf("Steps[%d].Command = %q, want %q", i, p.Steps[i].Command, w.cmd)
		}
	}
	// lint falls back to `go vet ./...` without .golangci.yml.
	lint := p.Steps[1]
	if lint.Command != "go" || len(lint.Args) != 2 || lint.Args[0] != "vet" || lint.Args[1] != "./..." {
		t.Errorf("lint step = %s %v, want go vet ./...", lint.Command, lint.Args)
	}
}

func TestGoProfileGolangCILint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".golangci.yml", "version: \"2\"\n")
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.23\n")

	p := Go(dir)
	lint := p.Steps[1]
	if lint.Command != "golangci-lint" || len(lint.Args) != 1 || lint.Args[0] != "run" {
		t.Errorf("lint step = %s %v, want golangci-lint run", lint.Command, lint.Args)
	}
}
