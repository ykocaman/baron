package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNoBeadsDBPathBlocksRealBD exercises the real bd binary end to end
// (skipped when bd isn't installed). It proves two things against a
// throwaway bd project rooted at t.TempDir() (never the real baron repo):
//
//  1. The naive design this file's doc comment warns against — a
//     nonexistent file INSIDE the worktree, e.g.
//     <worktree>/.baron/no-beads-db — does NOT isolate anything: bd's
//     upward-discovery walk starts from BEADS_DB's directory, climbs past
//     the missing leaf, finds the project's real .beads/, and reads/writes
//     it. This was caught by hand against this very repo before the design
//     was corrected (a `bd create` with the naive path landed a real issue
//     in baron's live database).
//  2. NoBeadsDBPath's os.TempDir()-rooted path actually blocks both read
//     and write: `bd list` returns empty and `bd create` errors with
//     "database not initialized", never touching the seeded project issue.
func TestNoBeadsDBPathBlocksRealBD(t *testing.T) {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not installed")
	}

	repo := t.TempDir()
	// run returns stdout and stderr separately: bd writes its discovery
	// warnings ("no beads configuration found...") to stderr and only the
	// actual --json payload to stdout, so combining them would corrupt JSON
	// parsing.
	run := func(dir string, env map[string]string, args ...string) (stdout, stderr string, err error) {
		cmd := exec.CommandContext(t.Context(), bdPath, args...)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		err = cmd.Run()
		return outBuf.String(), errBuf.String(), err
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, errOut, err := run(repo, nil, "init", "--quiet", "--prefix", "bdiso"); err != nil {
		t.Fatalf("bd init: %v\n%s%s", err, out, errOut)
	}
	if out, errOut, err := run(repo, nil, "create", "--title=real project issue", "--type=task", "--priority=4"); err != nil {
		t.Fatalf("bd create (seed): %v\n%s%s", err, out, errOut)
	}

	worktree := filepath.Join(repo, ".baron", "worktrees", "bdiso-1")
	if err := os.MkdirAll(worktree, 0o750); err != nil {
		t.Fatal(err)
	}

	listIssues := func(dir string, env map[string]string) []map[string]any {
		t.Helper()
		out, errOut, err := run(dir, env, "list", "--json")
		if err != nil {
			t.Fatalf("bd list: %v\n%s%s", err, out, errOut)
		}
		var issues []map[string]any
		if err := json.Unmarshal([]byte(out), &issues); err != nil {
			t.Fatalf("bd list output not JSON: %v\nstdout=%s\nstderr=%s", err, out, errOut)
		}
		return issues
	}

	// Naive design (rejected): a nonexistent file INSIDE the worktree.
	naive := filepath.Join(worktree, ".baron", "no-beads-db")
	if issues := listIssues(worktree, map[string]string{"BEADS_DB": naive}); len(issues) == 0 {
		t.Fatal("naive BEADS_DB path unexpectedly did NOT leak the real project's issues — if bd's discovery behavior changed, NoBeadsDBPath's doc comment needs re-verifying, not this test relaxed")
	}

	// NoBeadsDBPath's actual design.
	safe := NoBeadsDBPath(worktree)
	if issues := listIssues(worktree, map[string]string{"BEADS_DB": safe}); len(issues) != 0 {
		t.Fatalf("safe BEADS_DB path leaked real project issues: %v", issues)
	}
	if out, errOut, err := run(worktree, map[string]string{"BEADS_DB": safe}, "create", "--title=should not reach real db", "--type=task", "--priority=4"); err == nil {
		t.Fatalf("bd create (safe) unexpectedly succeeded, want an error (no project initialized at the isolated path):\n%s%s", out, errOut)
	}

	// The real project's database must be untouched by either safe-path attempt.
	if issues := listIssues(repo, nil); len(issues) != 1 {
		t.Fatalf("real project issue count = %d, want 1 (only the seed issue)", len(issues))
	}
}
