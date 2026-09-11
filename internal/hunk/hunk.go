// Package hunk reads review comments out of a live Hunk session.
//
// Hunk (https://github.com/hunk-review/hunk, installed as the `hunk` CLI) is
// the diff reviewer BARON embeds in the Diff tab. Its daemon tracks a session
// per repository working tree and exposes the comments attached to it — both
// the ones a human leaves in the TUI and the ones an agent posts via `hunk
// session comment add`, which is what BARON's spawned agents are told to do
// when they make a non-obvious change (see domain.Prompt's steering section).
//
// BARON reads those comments so a review note left against a diff line does
// not stay stranded in a review tool the agent has already stopped looking at:
// it becomes a bead comment (a durable record) and gets delivered back to the
// agent as steering. That round trip is the whole reason this package exists.
package hunk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/baron-cli/baron/internal/tool"
)

// hunkOpts strips the caller's environment for the same reason tmux does:
// BARON may be running inside a session whose env would otherwise steer the
// child at a different daemon or terminal.
var hunkOpts = tool.Options{CleanEnv: true}

// Comment is one review note on a diff line in a live Hunk session. Field tags
// match the CLI's `--json` output.
type Comment struct {
	ID   string `json:"commentId"`
	File string `json:"filePath"`
	// HunkIndex is 0-based in the JSON, though the CLI's human-readable
	// output prints it 1-based.
	HunkIndex int `json:"hunkIndex"`
	// Side is "new" or "old" — which side of the diff Line refers to.
	Side      string    `json:"side"`
	Line      int       `json:"line"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
}

// Location renders the comment's anchor as "file:line" ("file" when the
// comment is not line-anchored), for prefixing the text of the bead comment it
// becomes.
func (c Comment) Location() string {
	if c.Line <= 0 {
		return c.File
	}
	return fmt.Sprintf("%s:%d", c.File, c.Line)
}

// Available reports whether the hunk CLI is installed (`hunk --version` exits
// 0). Every other call in this package is gated on it, and so is the steering
// section BARON appends to an agent's prompt — an agent must never be told to
// run a command that does not exist.
func Available(ctx context.Context, runner tool.Runner) bool {
	_, err := runner.Run(ctx, "hunk", []string{"--version"}, hunkOpts)
	return err == nil
}

// commentList is the shape of `hunk session comment list --json`.
type commentList struct {
	Comments []Comment `json:"comments"`
}

// Comments returns the review comments on the live session for repo, oldest
// first. No session for that repo is not an error — it is the normal state
// whenever the Diff tab is closed — and yields no comments.
func Comments(ctx context.Context, runner tool.Runner, repo string) ([]Comment, error) {
	res, err := runner.Run(ctx, "hunk",
		[]string{"session", "comment", "list", "--repo", repo, "--json"}, hunkOpts)
	if err != nil {
		if noSession(res) {
			return nil, nil
		}
		return nil, fmt.Errorf("hunk session comment list: %w", err)
	}
	// The CLI reports "no active session" on stdout with a zero exit in some
	// versions, so the text check is not only an error path.
	if noSession(res) {
		return nil, nil
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return nil, nil
	}
	var list commentList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("hunk session comment list: parse %q: %w", truncate(out, 120), err)
	}
	return list.Comments, nil
}

// noSession reports whether a hunk invocation failed only because no
// session is open for the requested repo — matched against hunk's own
// message ("No active session matches repoRoot ..."), not the bare phrase
// "no active session": a genuine review comment whose text happens to
// contain that shorter phrase must not be silently swallowed as if the
// session were missing.
func noSession(res tool.Result) bool {
	both := strings.ToLower(res.Stdout + res.Stderr)
	return strings.Contains(both, "no active session matches")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
