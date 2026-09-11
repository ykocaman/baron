package profile

import "path/filepath"

// Go returns the Go gate profile for dir.
func Go(dir string) Profile {
	return Profile{
		Name: "go",
		Steps: buildSteps([][]stepAlt{
			{step("format", "gofmt", []string{"-l", "."}, nil)},
			{
				step("lint", "golangci-lint", []string{"run"}, func() bool { return exists(filepath.Join(dir, ".golangci.yml")) }),
				step("lint", "go", []string{"vet", "./..."}, nil), // fallback: always matches
			},
			{step("tidy", "go", []string{"mod", "tidy", "-diff"}, nil)},
			{step("test", "go", []string{"test", "-count=1", "./..."}, nil)},
			{step("build", "go", []string{"build", "./..."}, nil)},
		}),
	}
}
