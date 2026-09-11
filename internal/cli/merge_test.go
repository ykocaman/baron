package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/persona"
	"github.com/baron-cli/baron/internal/tool"
)

// mergeTestRunner scripts the subprocess calls `baron merge` makes.
type mergeTestRunner struct {
	beadJSON     string // served for `bd list --json`; defaults to a mergable task bead
	preflightErr bool   // true = git merge-tree reports a conflict (exit 1)
	mergeErr     bool
	mergeCalls   [][]string // records `git merge` invocations
	removeCalls  [][]string
	statusCalls  []string
	comments     []string

	// mergeCloseOut is served for the closer gate's own model launch
	// ("claude", the closer persona's own Backend.Launch — same
	// funnel as every other subprocess call here, see newTestApp's
	// a.backend = domain.NewBackend(runner)); "" defaults to "CLOSE: OK".
	// mergeCloseCalls counts those launches specifically, separate from
	// mergeCalls (the actual `git merge` invocation).
	mergeCloseOut   string
	mergeCloseCalls int
}

func (r *mergeTestRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	switch {
	case name == "bd" && len(args) > 0 && args[0] == "list":
		beadJSON := r.beadJSON
		if beadJSON == "" {
			beadJSON = mergeBeadJSON
		}
		return tool.Result{Stdout: beadJSON}, nil
	case name == "git" && len(args) > 0 && args[0] == "rev-parse":
		// tool.HeadSHA — must be non-empty or mergeCloseGate's own
		// beforeSHA=="" guard skips the gate entirely before it ever
		// reaches a persona lookup.
		return tool.Result{Stdout: "beforesha0123456789"}, nil
	case name == "git" && len(args) > 0 && args[0] == "diff":
		return tool.Result{Stdout: ""}, nil
	case name == "git" && len(args) > 0 && args[0] == "merge":
		r.mergeCalls = append(r.mergeCalls, args)
		if r.mergeErr {
			return tool.Result{ExitCode: 1, Stderr: "not mergeable"}, errors.New("not mergeable")
		}
		return tool.Result{}, nil
	case name == "git" && len(args) > 0 && args[0] == "merge-tree":
		if r.preflightErr {
			return tool.Result{ExitCode: 1, Stdout: "CONFLICT: a.go"}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	case name == "git" && len(args) > 0 && args[0] == "worktree" && args[1] == "remove":
		r.removeCalls = append(r.removeCalls, args)
		return tool.Result{}, nil
	case name == "bd" && len(args) > 0 && args[0] == "update":
		r.statusCalls = append(r.statusCalls, args[len(args)-1])
		return tool.Result{}, nil
	case name == "bd" && len(args) > 0 && args[0] == "comment":
		r.comments = append(r.comments, args[len(args)-1])
		return tool.Result{}, nil
	case name == "claude" && len(args) > 0 && strings.Contains(args[len(args)-1], "CLOSE: OK\nor\nCLOSE: REOPEN"):
		r.mergeCloseCalls++
		out := r.mergeCloseOut
		if out == "" {
			out = "CLOSE: OK"
		}
		return tool.Result{Stdout: out}, nil
	}
	return tool.Result{}, nil
}

// enabledMergeCloser is the "closer" persona (persona.MergeCloserID),
// enabled and pointed straight at the "claude" test fixture agent —
// mergeCloseGate skips entirely (mergeCloseCalls stays 0) unless a test
// opts in via this, matching newTestApp's own loadPersonas default (nil,
// nil).
func enabledMergeCloser() []persona.Persona {
	return []persona.Persona{{
		ID: persona.MergeCloserID, Name: "Closer",
		Prompt:  "Check this merge.",
		Model:   persona.Model{Agent: "claude"},
		Enabled: true,
	}}
}

const mergeBeadJSON = `[{"id":"baron-a1b2c3","title":"t","status":"mergable","priority":2,"issue_type":"task"}]`

const mergeBeadOpenJSON = `[{"id":"baron-a1b2c3","title":"t","status":"open","priority":2,"issue_type":"task"}]`

func TestMergeRejectsWithoutConfirmMergeNonTTY(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	err := a.runMerge(context.Background(), "baron-a1b2c3", false)
	if !isSilentError(err) {
		t.Fatalf("runMerge() error = %v, want ExitMerge; stderr: %s", err, stderr(t, a))
	}
	if len(r.mergeCalls) != 0 {
		t.Error("git merge ran despite missing --confirm-merge")
	}
}

func TestMergeRejectsWithoutYesNonTTY(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	err := a.runMerge(context.Background(), "baron-a1b2c3", true)
	if !isSilentError(err) {
		t.Fatalf("runMerge() error = %v, want ExitMerge", err)
	}
	if len(r.mergeCalls) != 0 {
		t.Error("git merge ran despite missing --yes")
	}
}

func TestMergeByBRNClosesBeadAndCleansWorktree(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if len(r.mergeCalls) != 1 {
		t.Fatalf("git merge calls = %d, want 1", len(r.mergeCalls))
	}
	// merged is a resting terminal state (a registered bd custom status),
	// not a hop through to closed — the bead stays distinguishably
	// "merged" instead of collapsing into bd's own "closed".
	if len(r.statusCalls) != 1 || r.statusCalls[0] != "merged" {
		t.Errorf("statusCalls = %v, want a single merged status update", r.statusCalls)
	}
	if len(r.removeCalls) != 1 {
		t.Errorf("removeCalls = %d, want 1 (worktree cleaned up)", len(r.removeCalls))
	}
	if !hasAuditDetail(auditEvents(t, a), "merged") {
		t.Errorf("audit events = %+v, want the merge recorded", auditEvents(t, a))
	}
}

func TestMergeConflictAbortsWithoutMerging(t *testing.T) {
	r := &mergeTestRunner{preflightErr: true}
	a := newTestApp(t, r)
	a.yes = true
	err := a.runMerge(context.Background(), "baron-a1b2c3", true)
	if !isSilentError(err) {
		t.Fatalf("runMerge() error = %v, want ExitMerge", err)
	}
	if len(r.mergeCalls) != 0 {
		t.Error("git merge ran despite a preflight conflict")
	}
	if !strings.Contains(stdout(t, a), "conflict") {
		t.Errorf("stdout = %q, want a conflict message", stdout(t, a))
	}
}

func TestMergeFailureSurfaces(t *testing.T) {
	r := &mergeTestRunner{mergeErr: true}
	a := newTestApp(t, r)
	a.yes = true
	err := a.runMerge(context.Background(), "baron-a1b2c3", true)
	if err == nil || !strings.Contains(err.Error(), "not mergeable") {
		t.Errorf("error = %v, want git merge's failure surfaced", err)
	}
}

func TestMergeNotReadyForBRN(t *testing.T) {
	r := &mergeTestRunner{beadJSON: mergeBeadOpenJSON}
	a := newTestApp(t, r)
	a.yes = true
	err := a.runMerge(context.Background(), "baron-a1b2c3", true)
	if err == nil || !strings.Contains(err.Error(), "not ready to merge") {
		t.Errorf("error = %v, want a not-ready-to-merge error", err)
	}
}

func TestMergeConfirmTTYDecline(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.in = strings.NewReader("n\n")
	a.inTTY = func() bool { return true }
	err := a.runMerge(context.Background(), "baron-a1b2c3", false)
	if !isSilentError(err) {
		t.Fatalf("runMerge() error = %v, want ExitMerge; stderr: %s", err, stderr(t, a))
	}
	if len(r.mergeCalls) != 0 {
		t.Error("git merge ran despite TTY decline")
	}
}

func TestMergeConfirmTTYAccept(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.in = strings.NewReader("y\n")
	a.inTTY = func() bool { return true }
	if err := a.runMerge(context.Background(), "baron-a1b2c3", false); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if len(r.mergeCalls) != 1 {
		t.Fatalf("git merge calls = %d, want 1", len(r.mergeCalls))
	}
}

// TestMergeCloserSkippedWhenNotConfigured: newTestApp's own loadPersonas
// default (nil, nil, see enabledMergeCloser's own doc comment) — the merge
// closer must never fire on a project that never configured/enabled it,
// same as reviewGate's own default-off behavior.
func TestMergeCloserSkippedWhenNotConfigured(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if r.mergeCloseCalls != 0 {
		t.Errorf("mergeCloseCalls = %d, want 0 — no closer persona configured", r.mergeCloseCalls)
	}
	if len(r.statusCalls) != 1 || r.statusCalls[0] != "merged" {
		t.Errorf("statusCalls = %v, want just [merged] — nothing should close it", r.statusCalls)
	}
}

// TestMergeCloserSkippedWhenDisabled mirrors TestReviewGateSkippedWhenDisabled.
func TestMergeCloserSkippedWhenDisabled(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) {
		p := enabledMergeCloser()
		p[0].Enabled = false
		return p, nil
	}
	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if r.mergeCloseCalls != 0 {
		t.Errorf("mergeCloseCalls = %d, want 0 — the closer persona is disabled", r.mergeCloseCalls)
	}
}

// TestMergeCloserClosesWhenOK: an enabled closer that returns
// "CLOSE: OK" (mergeTestRunner's own default) closes the bead out —
// merged -> closed, actor:system, right after the ordinary merge -> merged
// transition.
func TestMergeCloserClosesWhenOK(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledMergeCloser(), nil }

	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if r.mergeCloseCalls != 1 {
		t.Fatalf("mergeCloseCalls = %d, want 1", r.mergeCloseCalls)
	}
	if len(r.statusCalls) != 2 || r.statusCalls[0] != "merged" || r.statusCalls[1] != "closed" {
		t.Errorf("statusCalls = %v, want [merged closed]", r.statusCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "closed: merge holds") {
		t.Errorf("audit events = %+v, want the close recorded", auditEvents(t, a))
	}
}

// TestMergeCloserReopensOnRejection: a "CLOSE: REOPEN: <reason>" verdict
// sends the bead back to open (not closed) with the persona's own reason
// recorded — mergeCloseGate never calls `bd reopen`/`bd close` itself
// (empty Authority, see MergeCloserID's own doc comment); this is BARON's
// own trusted transitionStatus acting on the parsed verdict.
func TestMergeCloserReopensOnRejection(t *testing.T) {
	r := &mergeTestRunner{mergeCloseOut: "CLOSE: REOPEN: the acceptance criteria aren't actually met"}
	a := newTestApp(t, r)
	a.yes = true
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledMergeCloser(), nil }

	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if len(r.statusCalls) != 2 || r.statusCalls[0] != "merged" || r.statusCalls[1] != "open" {
		t.Errorf("statusCalls = %v, want [merged open]", r.statusCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "reopened: the acceptance criteria aren't actually met") {
		t.Errorf("audit events = %+v, want the reopen reason recorded", auditEvents(t, a))
	}
}

// TestMergeCloserRetriesOnceThenLeavesMerged: an unparseable reply gets one
// retry (mergeCloseGate's own maxAttempts=2, see its doc comment — "hangs
// or replies unparseably, try again once"), and if that also comes back
// unparseable, the bead is left merged for a human rather than guessed at
// either way.
func TestMergeCloserRetriesOnceThenLeavesMerged(t *testing.T) {
	r := &mergeTestRunner{mergeCloseOut: "not a verdict at all"}
	a := newTestApp(t, r)
	a.yes = true
	writeAgentRegistry(t, testClaude())
	a.loadPersonas = func() ([]persona.Persona, error) { return enabledMergeCloser(), nil }

	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if r.mergeCloseCalls != 2 {
		t.Fatalf("mergeCloseCalls = %d, want 2 (one retry on the unparseable reply)", r.mergeCloseCalls)
	}
	if len(r.statusCalls) != 1 || r.statusCalls[0] != "merged" {
		t.Errorf("statusCalls = %v, want just [merged] — an unparseable verdict must not close or reopen anything", r.statusCalls)
	}
	if !hasAuditDetail(auditEvents(t, a), "no parseable verdict after retries") {
		t.Errorf("audit events = %+v, want the give-up recorded", auditEvents(t, a))
	}
}
