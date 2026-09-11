package domain

import (
	"os"
	"path/filepath"
)

// NoBeadsDBPath returns a path bd will never find a real database at, for
// use as a task-agent's BEADS_DB override (see TaskAgentEnv).
//
// This must live OUTSIDE the repo tree, not merely be a nonexistent file
// inside it. Measured directly against the real bd binary: when BEADS_DB
// names a nonexistent path whose ancestry still climbs into a directory
// that actually has a .beads/ in it (e.g. <worktree>/.baron/no-beads-db,
// since BARON's worktrees live under <repo>/.baron/worktrees), bd's
// upward-discovery walk starts from BEADS_DB's *directory* instead of the
// caller's cwd, climbs past the nonexistent leaf, finds the repo's real
// .beads/, and happily reads/writes it — confirmed by an actual `bd create`
// landing in the project's live database. Pointing BEADS_DB at a path under
// os.TempDir() instead — an ancestry that never crosses a real .beads/ —
// reproduces the intended failure: `bd list` returns an empty result and
// `bd create` errors with "database not initialized", never touching the
// real project. See docs/PRD/harness-hardening.md §3.2 for the full
// before/after.
func NoBeadsDBPath(worktreeDir string) string {
	return filepath.Join(os.TempDir(), "baron-no-bd", filepath.Base(worktreeDir), "no-beads-db")
}

// TaskAgentEnv returns the environment overrides applied to every
// task-agent subprocess, direct or tmux-backed — an agent coding inside an
// isolated worktree has no legitimate reason to read or write bead state
// (agent-contract.md §3: "Prompt'ta durum komutu yoktur"). Crew Mode
// personas (docs/PRD/crew-mode.md) are a deliberately separate, more
// trusted launch path and must NOT use this — they run in the main repo
// checkout with real bd access by design.
func TaskAgentEnv(worktreeDir string) map[string]string {
	return map[string]string{"BEADS_DB": NoBeadsDBPath(worktreeDir)}
}
