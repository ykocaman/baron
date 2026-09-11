package cli

import (
	"context"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// ctxCapturingRunner records the context passed to the run whose args[0] is
// captureArg, for tests that need to inspect it (a deadline, e.g.) — the
// package's usual fakeRunner ignores ctx entirely.
type ctxCapturingRunner struct {
	captureArg string
	gotCtx     context.Context
}

func (r *ctxCapturingRunner) Run(ctx context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	if len(args) > 0 && args[0] == r.captureArg {
		r.gotCtx = ctx
	}
	return tool.Result{Stdout: `{"comments":[]}`}, nil
}

// TestHunkDepsCommentsBoundsContext: a hung `hunk` process (the comment
// reader, not just the availability probe) must not leave
// Model.hunkPollInFlight (internal/tui) stuck true forever — the closure
// hunkDeps hands back has to bound its own context with a deadline, since
// tui.Deps.HunkComments takes none of its own for the TUI to supply one.
func TestHunkDepsCommentsBoundsContext(t *testing.T) {
	r := &ctxCapturingRunner{captureArg: "session"}
	a := newTestApp(t, r)
	comments := a.hunkDeps(context.Background())
	if comments == nil {
		t.Fatal("hunkDeps: hunk not detected as available against a runner that always succeeds")
	}
	if _, err := comments(a.dir); err != nil {
		t.Fatalf("comments() error: %v", err)
	}
	if r.gotCtx == nil {
		t.Fatal("hunk.Comments never ran (or ran a different subcommand) — nothing to check")
	}
	if _, ok := r.gotCtx.Deadline(); !ok {
		t.Fatal("hunk.Comments' ctx had no deadline — a hung `hunk` process could block hunkPollInFlight forever")
	}
}
