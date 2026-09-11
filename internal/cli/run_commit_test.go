package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// commitRunner scripts the git state the commit half of the pipeline reads:
// whether the index differs from HEAD (staged), and how many commits the
// branch is ahead of its base (ahead). It records the ordered command log so
// step ordering can be asserted rather than assumed.
type commitRunner struct {
	*runGateRunner
	staged    bool   // `git diff --cached --quiet` exits 1 (something to commit)
	ahead     string // `git rev-list --count` output; "" = base ref unresolvable
	commitMsg string // captured `git commit -m` message
	calls     []string
}

func (r *commitRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	if name == "git" {
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	}
	switch {
	case name == "git" && args[0] == "diff" && slices.Contains(args, "--cached") && slices.Contains(args, "--quiet"):
		if r.staged {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	case name == "git" && args[0] == "commit":
		r.commitMsg = args[len(args)-1]
		return tool.Result{}, nil
	case name == "git" && args[0] == "rev-parse" && slices.Contains(args, "HEAD"):
		return tool.Result{Stdout: "1234567890abcdef1234567890abcdef12345678\n"}, nil
	case name == "git" && args[0] == "rev-parse" && slices.Contains(args, "main"):
		if r.ahead == "" {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1") // base ref unknown
		}
		return tool.Result{}, nil
	case name == "git" && args[0] == "rev-list":
		return tool.Result{Stdout: r.ahead + "\n"}, nil
	}
	return r.runGateRunner.Run(ctx, name, args, opts)
}

// indexOf returns the position of the first recorded call containing substr,
// or -1. Used to assert ordering between pipeline steps.
func (r *commitRunner) indexOf(substr string) int {
	for i, c := range r.calls {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

func newCommitApp(t *testing.T, r *commitRunner) *app {
	t.Helper()
	a := newTestApp(t, r)
	writeAgentRegistry(t, testClaude())
	return a
}

// TestRunCommitsStagedWorkBeforeReadyForMerge is the regression test for
// baron-jth: the gate must stage and commit the agent's changes before the
// bead is marked ready to merge — a bead with nothing committed has nothing
// for `baron merge` to merge.
func TestRunCommitsStagedWorkBeforeReadyForMerge(t *testing.T) {
	r := &commitRunner{
		runGateRunner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"},
		staged:        true,
		ahead:         "1",
	}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitOK, stderr(t, a))
	}
	if r.indexOf("git commit") < 0 {
		t.Fatalf("no git commit in %v", r.calls)
	}
	if !strings.Contains(r.commitMsg, "Implement auth") || !strings.Contains(r.commitMsg, "Bead: baron-a1b2c3") {
		t.Errorf("commit message = %q, want the bead title and a Bead: trailer", r.commitMsg)
	}
	if !hasAuditDetail(auditEvents(t, a), "committed 1234567") {
		t.Errorf("audit events = %+v, want the commit recorded", auditEvents(t, a))
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the bead marked ready to merge after the commit", stdout(t, a))
	}
}

// TestRunSkipsCommitWhenAgentAlreadyCommitted: an agent that commits its own
// work (docs/PRD/agent-contract.md §7) leaves nothing staged. That is not a
// failure — its own commits are what the bead is marked ready to merge from.
func TestRunSkipsCommitWhenAgentAlreadyCommitted(t *testing.T) {
	r := &commitRunner{
		runGateRunner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"},
		staged:        false,
		ahead:         "2",
	}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitOK, stderr(t, a))
	}
	if i := r.indexOf("git commit"); i >= 0 {
		t.Errorf("git commit ran with an empty index; calls: %v", r.calls)
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the bead marked ready to merge for an agent's own commits", stdout(t, a))
	}
}

// TestRunGatesCommittedWorkWithCleanTree is the regression test for
// baron-4wf: a committing agent leaves a clean worktree, which the pipeline
// read as "nothing changed" — skipping the gate, the secret scan, and
// marking the bead ready — and stranding it in "working" with exit 0.
func TestRunGatesCommittedWorkWithCleanTree(t *testing.T) {
	r := &commitRunner{
		runGateRunner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", noChanges: true},
		ahead:         "1",
	}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitOK, stderr(t, a))
	}
	if strings.Contains(stdout(t, a), "gates skipped") {
		t.Fatalf("stdout = %q, want the gate to run for a branch with commits ahead of base", stdout(t, a))
	}
	if !strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want the bead marked ready to merge for committed work", stdout(t, a))
	}
}

// TestRunSkipsGateWhenBranchUnchanged keeps the no-op shortcut: a clean tree
// with no commits ahead really is nothing to validate.
func TestRunSkipsGateWhenBranchUnchanged(t *testing.T) {
	r := &commitRunner{
		runGateRunner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", noChanges: true},
		ahead:         "0",
	}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitOK, stderr(t, a))
	}
	if !strings.Contains(stdout(t, a), "gates skipped") {
		t.Errorf("stdout = %q, want the gate skipped for an unchanged branch", stdout(t, a))
	}
	if strings.Contains(stdout(t, a), "ready to merge") {
		t.Errorf("stdout = %q, want no ready-to-merge for an unchanged branch", stdout(t, a))
	}
}

// TestRunWarnsWhenBaseRefUnresolvable: a base ref that doesn't resolve
// (project mid-setup, base branch not created yet) must not be read as "no
// commits ahead" in silence — the uncommitted-changes signal still carries
// the run, but the user is told the count is unknown.
func TestRunWarnsWhenBaseRefUnresolvable(t *testing.T) {
	r := &commitRunner{
		runGateRunner: &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude"},
		staged:        true,
		ahead:         "", // main does not resolve
	}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr: %s", code, ExitOK, stderr(t, a))
	}
	if !strings.Contains(stderr(t, a), "cannot resolve main") {
		t.Errorf("stderr = %q, want a warning naming the unresolvable base ref", stderr(t, a))
	}
}

func TestCommitMessageTypePrefix(t *testing.T) {
	for _, tc := range []struct{ issueType, want string }{
		{"feature", "feat: "},
		{"bug", "fix: "},
		{"task", "chore: "},
		{"", "chore: "},
		{"something-custom", "chore: "},
	} {
		if got := commitType(tc.issueType); !strings.HasPrefix(tc.want, got+": ") {
			t.Errorf("commitType(%q) = %q, want the prefix of %q", tc.issueType, got, tc.want)
		}
	}
}

// TestRunReportsWhyItRetried is the regression test for the silent retry
// loop: the reason a launch failed was written to the audit trail only, so a
// run that burned its whole budget printed "retry budget exhausted" and
// nothing a user could act on.
func TestRunReportsWhyItRetried(t *testing.T) {
	inner := &runGateRunner{beadJSON: runAssignedBeadJSON, modelCmd: "claude", gateExit: 1, gateFailAttempts: 99}
	r := &commitRunner{runGateRunner: inner, staged: true, ahead: "1"}
	a := newCommitApp(t, r)

	if code := runBRN(t, a); code != ExitGateFail {
		t.Fatalf("exit code = %d, want %d (ExitGateFail)", code, ExitGateFail)
	}
	out := stdout(t, a)
	if !strings.Contains(out, "retry 1/3: gate failed") {
		t.Errorf("stdout = %q, want each retry to name its reason", out)
	}
	if !strings.Contains(out, "retry budget exhausted") || !strings.Contains(out, "gate failed") {
		t.Errorf("stdout = %q, want the exhaustion line to carry the reason", out)
	}
}
