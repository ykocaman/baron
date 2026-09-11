package profile

import "path/filepath"

// Rust returns the Rust gate profile for dir. Unlike Go's linter or
// React-TS's package manager, cargo's own toolchain covers format/lint/
// test/build uniformly, so there's nothing project-specific to detect —
// every slot has exactly one, unconditional alternative.
func Rust(dir string) Profile {
	return Profile{
		Name: "rust",
		Steps: buildSteps([][]stepAlt{
			{step("format", "cargo", []string{"fmt", "--check"}, nil)},
			{step("lint", "cargo", []string{"clippy", "--all-targets", "--", "-D", "warnings"}, nil)},
			{step("test", "cargo", []string{"test"}, nil)},
			{step("build", "cargo", []string{"build"}, nil)},
		}),
	}
}

// detectRust reports whether dir is a Rust project (a Cargo.toml manifest).
func detectRust(dir string) bool {
	return exists(filepath.Join(dir, "Cargo.toml"))
}
