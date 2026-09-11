// Package main is the entrypoint for the baron CLI.
package main

import (
	"os"

	"github.com/baron-cli/baron/internal/cli"
)

// Set via ldflags
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Run(version, commit, date))
}
