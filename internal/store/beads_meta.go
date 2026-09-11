package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/baron-cli/baron/internal/tool"
)

// Version returns the installed bd CLI's own version string (`bd --version`).
func (s *BeadStore) Version(ctx context.Context) (string, error) {
	res, err := s.runner.Run(ctx, "bd", []string{"--version"}, tool.Options{Dir: s.dir})
	if err != nil {
		return "", fmt.Errorf("bd --version: %w", err)
	}
	v, err := parseVersion(res.Stdout)
	if err != nil {
		return "", err
	}
	return v, nil
}

// CheckDrift compares the installed bd version against pinned. A mismatch in
// major or minor is a drift error; patch-level differences are tolerated.
func (s *BeadStore) CheckDrift(ctx context.Context, pinned string) error {
	version, err := s.Version(ctx)
	if err != nil {
		return err
	}
	installed, err := parseSemver(version)
	if err != nil {
		return err
	}
	want, err := parseSemver(pinned)
	if err != nil {
		return fmt.Errorf("pinned version %q: %w", pinned, err)
	}
	if installed.major != want.major || installed.minor != want.minor {
		return fmt.Errorf("bd version drift: installed %s, pinned %s", version, pinned)
	}
	return nil
}

// listJSON runs a bd list-style command and parses the bead JSON output.
func (s *BeadStore) listJSON(ctx context.Context, args []string) ([]Bead, error) {
	data, err := s.runJSON(ctx, args)
	if err != nil {
		return nil, err
	}
	beads, err := parseBeads(data, s.brnPrefix)
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w", strings.Join(args, " "), err)
	}
	return beads, nil
}

// runJSON runs a bd subcommand and returns its stdout. Every caller reads a
// JSON response, so envelope detection (isEnvelope) happens here once
// rather than being repeated at each call site.
func (s *BeadStore) runJSON(ctx context.Context, args []string) ([]byte, error) {
	opts := tool.Options{Dir: s.dir}
	if s.envelope.Load() {
		opts.Env = map[string]string{"BD_JSON_ENVELOPE": "1"}
	}
	res, err := s.runner.Run(ctx, "bd", args, opts)
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w", strings.Join(args, " "), err)
	}
	data := []byte(res.Stdout)
	s.envelope.Store(isEnvelope(data))
	return data, nil
}

// runSimple runs a bd subcommand, returning stderr in the error on failure.
func (s *BeadStore) runSimple(ctx context.Context, args ...string) error {
	res, err := s.runner.Run(ctx, "bd", args, tool.Options{Dir: s.dir})
	if err != nil {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		return fmt.Errorf("bd %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}
