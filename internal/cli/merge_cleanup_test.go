package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// tmuxMergeRunner is mergeTestRunner plus a record of the tmux invocations,
// so what the merge reaps can be asserted.
type tmuxMergeRunner struct {
	*mergeTestRunner
	tmuxCalls [][]string
}

func (r *tmuxMergeRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	if name == "tmux" {
		r.tmuxCalls = append(r.tmuxCalls, args)
		if len(args) > 0 && args[0] == "-V" {
			return tool.Result{Stdout: "tmux 3.4\n"}, nil // installed
		}
		return tool.Result{}, nil
	}
	return r.mergeTestRunner.Run(ctx, name, args, opts)
}

// killedWindow returns the window name passed to `tmux kill-window -t
// <session>:<window>`, or "" when no window was killed.
func (r *tmuxMergeRunner) killedWindow() string {
	for _, c := range r.tmuxCalls {
		if len(c) > 0 && c[0] == "kill-window" {
			target := c[len(c)-1]
			if _, window, ok := strings.Cut(target, ":"); ok {
				return window
			}
			return target
		}
	}
	return ""
}

// TestMergeReapsTheBeadsTmuxWindow is the regression test for the leaked
// windows: only `baron work close` reaped them, so a bead that ended at
// "merged" — a resting terminal state a human may never close on top of —
// left its window behind holding a live shell. They accumulated without
// bound (105 of them on the development machine).
func TestMergeReapsTheBeadsTmuxWindow(t *testing.T) {
	r := &tmuxMergeRunner{mergeTestRunner: &mergeTestRunner{}}
	a := newTestApp(t, r)
	a.yes = true

	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if len(r.removeCalls) == 0 {
		t.Error("the worktree was not removed")
	}
	if got := r.killedWindow(); got != "baron-a1b2c3" {
		t.Errorf("killed window = %q, want the bead's window; tmux calls: %v", got, r.tmuxCalls)
	}
}

// TestMergeReapsDottedChildWindow covers the second half of the same bug:
// the kill used the raw BRN while the launch used tmux.WindowName, so a
// dotted child ("baron-a1b2.1", running in window "baron-a1b2-1") matched
// nothing and survived.
func TestMergeReapsDottedChildWindow(t *testing.T) {
	const childBeadJSON = `[{"id":"baron-a1b2.1","title":"t","status":"mergable","priority":2,"issue_type":"task"}]`
	r := &tmuxMergeRunner{mergeTestRunner: &mergeTestRunner{beadJSON: childBeadJSON}}
	a := newTestApp(t, r)
	a.yes = true

	if err := a.runMerge(context.Background(), "baron-a1b2.1", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if got := r.killedWindow(); got != "baron-a1b2-1" {
		t.Errorf("killed window = %q, want the tmux-safe name baron-a1b2-1; tmux calls: %v", got, r.tmuxCalls)
	}
}
