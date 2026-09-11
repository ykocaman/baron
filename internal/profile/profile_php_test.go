package profile

import "testing"

func TestPHPProfileSkipOptional(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)

	p := PHP(dir)
	if p.Name != "php" {
		t.Errorf("Name = %q, want %q", p.Name, "php")
	}
	// No formatter/analyzer/test config: only install remains.
	if len(p.Steps) != 1 || p.Steps[0].Name != "install" {
		t.Fatalf("Steps = %+v, want just [install]", p.Steps)
	}
}

func TestPHPProfileFullTooling(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)
	writeFile(t, dir, ".php-cs-fixer.php", "<?php\n")
	writeFile(t, dir, "phpstan.neon", "")
	writeFile(t, dir, "phpunit.xml", "<phpunit></phpunit>\n")

	p := PHP(dir)
	want := []struct{ name, cmd string }{
		{"install", "composer"},
		{"format", "php-cs-fixer"},
		{"lint", "phpstan"},
		{"test", "phpunit"},
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

// TestPHPProfilePsalmFallback: psalm is used when phpstan isn't configured.
func TestPHPProfilePsalmFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)
	writeFile(t, dir, "psalm.xml", "<psalm></psalm>\n")

	p := PHP(dir)
	var lint *GateStep
	for i := range p.Steps {
		if p.Steps[i].Name == "lint" {
			lint = &p.Steps[i]
		}
	}
	if lint == nil || lint.Command != "psalm" {
		t.Errorf("lint step = %+v, want psalm", lint)
	}
}

func TestDetectPHP(t *testing.T) {
	dir := t.TempDir()
	if detectPHP(dir) {
		t.Error("detectPHP(empty dir) = true, want false")
	}
	writeFile(t, dir, "composer.json", `{"name": "vendor/test"}`)
	if !detectPHP(dir) {
		t.Error("detectPHP(with composer.json) = false, want true")
	}
}
