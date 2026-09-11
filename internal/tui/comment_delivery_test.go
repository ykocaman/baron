package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/hunk"
	"github.com/baron-cli/baron/internal/store"
)

// deliveryModel returns a sized dashboard Model for bead baron-a, plus a
// recorder of every CLI action the TUI runs (so "was it recorded on the bead"
// is checkable) — with no agent session yet.
func deliveryModel(t *testing.T) (Model, *[]string) {
	t.Helper()
	var ran []string
	d := testDeps()
	d.Comment = func(brn, text string) (string, error) {
		ran = append(ran, "work comment "+brn+" "+text)
		return "", nil
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking, Assignee: "opencode"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detail = beads[0]
	m.liveBRN = "baron-a"
	return m, &ran
}

// livePipeSession attaches a live agent session for the fixed test bead
// "baron-a" backed by a real pipe, returning the read end so what BARON
// writes toward the agent is observable.
func livePipeSession(t *testing.T, m *Model) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, pty: w, emu: vt.NewSafeEmulator(80, 24)}
	return r
}

// runAll executes cmd and everything a tea.Batch inside it fans out to, the
// way the bubbletea runtime would. Calling a batch's own func only yields the
// list of commands — it does not run them — so a test that stops there sees no
// effects at all.
func runAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runAll(c)
		}
	}
}

func readWithin(t *testing.T, r *os.File, d time.Duration) string {
	t.Helper()
	_ = r.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

// TestCommentSteersRunningAgent: a comment on a bead whose agent is running
// must reach that agent, not just the bead's record. A review that the agent
// never sees only lands if a human relays it by hand, which is the gap this
// closes.
func TestCommentSteersRunningAgent(t *testing.T) {
	m, _ := deliveryModel(t)
	r := livePipeSession(t, &m)

	cmd := m.deliverCommentCmd("baron-a", "please also handle the empty case")
	if cmd == nil {
		t.Fatal("no delivery command for a running agent")
	}
	cmd()

	got := readWithin(t, r, time.Second)
	if !strings.Contains(got, "please also handle the empty case") {
		t.Fatalf("agent received %q, want the comment text", got)
	}
}

// TestCommentSteeringUsesBracketedPaste: comments are prose a human typed, so
// they contain newlines, and an agent's input box submits on the first one.
// Sent raw, a three-line note becomes three turns with a fragment first.
func TestCommentSteeringUsesBracketedPaste(t *testing.T) {
	m, _ := deliveryModel(t)
	r := livePipeSession(t, &m)

	m.deliverCommentCmd("baron-a", "first line\nsecond line\nthird line")()

	got := readWithin(t, r, time.Second)
	if !strings.Contains(got, "\x1b[200~") || !strings.Contains(got, "\x1b[201~") {
		t.Errorf("agent received %q, want it wrapped in bracketed paste so the newlines don't submit early", got)
	}
	start := strings.Index(got, "\x1b[200~")
	end := strings.Index(got, "\x1b[201~")
	if start < 0 || end < start {
		t.Fatalf("malformed paste framing in %q", got)
	}
	if body := got[start+len("\x1b[200~") : end]; body != "first line\nsecond line\nthird line" {
		t.Errorf("pasted body = %q, want all three lines inside the brackets", body)
	}
	if !strings.HasSuffix(got, "\r") {
		t.Errorf("agent received %q, want a trailing CR to submit it", got)
	}
}

// TestCommentRestartsStoppedAgent is the second half of the requirement — "or
// if it has stopped, as a new prompt, so the agent can continue". A comment
// that silently does nothing because the agent finished is the case the user
// hit; delivery has to cover both states or it is a coin flip.
func TestCommentRestartsStoppedAgent(t *testing.T) {
	m, _ := deliveryModel(t)
	// A session that has exited, exactly like an agent that finished its turn.
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(80, 24), done: true}

	cmd := m.deliverCommentCmd("baron-a", "one more thing")
	if cmd == nil {
		t.Fatal("a stopped agent got no delivery command at all — the comment goes nowhere")
	}
	// The batch carries the restart spawn; running it reaches the real
	// spawn path, which fails on this fake store — what matters here is
	// that a spawn was attempted rather than the comment being dropped.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want a batch carrying the notice and the restart", cmd())
	}
	var sawSpawnAttempt bool
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, ok := c().(agentSpawnedMsg); ok {
			sawSpawnAttempt = true
		}
	}
	if !sawSpawnAttempt {
		t.Error("no spawn attempt for a stopped agent — the comment would never be acted on")
	}
}

// TestCommentOnBeadWithNoAgentStartsNothing: "stopped" means an agent that was
// working and finished, not one that was never started. Spawning an agent as a
// side effect of leaving a comment would launch unasked-for work on any bead
// the user happens to comment on — and yank the view to the Terminal tab while
// doing it.
func TestCommentOnBeadWithNoAgentStartsNothing(t *testing.T) {
	m, _ := deliveryModel(t)
	m.detailTab = 0

	if cmd := m.deliverCommentCmd("baron-a", "just a note for later"); cmd != nil {
		t.Errorf("commenting on a bead with no agent produced %T — it must record only", cmd())
	}
	if m.detailTab != 0 {
		t.Errorf("detailTab = %d, want to stay on Overview where the comment is", m.detailTab)
	}
	if len(m.sessions) != 0 {
		t.Errorf("a session was created for a bead nobody started work on: %v", m.sessions)
	}
}

// TestCommentRestartCarriesTheCommentAsPrompt: restarting is only useful if
// the agent opens on the comment. Restarting it on the bead's original brief
// would invite it to redo work it already finished and ignore the review
// entirely — see agentPrompt.
func TestCommentRestartCarriesTheCommentAsPrompt(t *testing.T) {
	bead := store.Bead{BRN: "baron-a", Title: "Add retries", Description: "the brief", AcceptanceCriteria: "tests pass"}

	got := agentPrompt(bead, t.TempDir(), "the follow-up note")
	if got != "the follow-up note" {
		t.Errorf("prompt = %q, want exactly the follow-up note", got)
	}
	if strings.Contains(got, "Add retries") || strings.Contains(got, "the brief") {
		t.Error("the original brief leaked into the restart prompt; the agent would redo finished work")
	}

	// With no follow-up it must still be the normal opening brief.
	std := agentPrompt(bead, t.TempDir(), "")
	if !strings.Contains(std, "Add retries") {
		t.Errorf("standard prompt = %q, want the bead's brief", std)
	}
}

// TestHunkCommentsBecomeBeadCommentsAndReachTheAgent is the unification the
// user asked for: a note left on a diff line in the Diff tab is a review
// instruction, and it has to end up both on the bead (a durable record, in the
// same list as every other comment) and in front of the agent.
func TestHunkCommentsBecomeBeadCommentsAndReachTheAgent(t *testing.T) {
	m, ran := deliveryModel(t)
	r := livePipeSession(t, &m)
	m.diffSessions["baron-a"] = &agentTerminal{
		brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24),
		startedAt: time.Now().Add(-time.Minute),
	}

	cmd := m.ingestHunkComments(hunkCommentsMsg{brn: "baron-a", comments: []hunk.Comment{
		{ID: "c1", File: "internal/x.go", Line: 42, Summary: "this drops the error", CreatedAt: time.Now()},
	}})
	if cmd == nil {
		t.Fatal("a new hunk review comment produced no action — it stays stranded in the review tool")
	}
	runAll(cmd)

	var recorded bool
	for _, a := range *ran {
		if strings.Contains(a, "work comment baron-a") && strings.Contains(a, "this drops the error") {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("hunk comment was not recorded on the bead; actions run: %v", *ran)
	}
	got := readWithin(t, r, time.Second)
	if !strings.Contains(got, "this drops the error") {
		t.Errorf("agent received %q, want the review note", got)
	}
	if !strings.Contains(got, "internal/x.go:42") {
		t.Errorf("agent received %q, want the file:line anchor so the note is actionable", got)
	}
}

// TestHunkCommentIngestedOnlyOnce: polling repeats every couple of seconds, so
// without a seen-set the same note would be re-delivered to the agent forever.
func TestHunkCommentIngestedOnlyOnce(t *testing.T) {
	m, ran := deliveryModel(t)
	livePipeSession(t, &m)
	m.diffSessions["baron-a"] = &agentTerminal{
		brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24),
		startedAt: time.Now().Add(-time.Minute),
	}
	msg := hunkCommentsMsg{brn: "baron-a", comments: []hunk.Comment{
		{ID: "c1", File: "a.go", Line: 1, Summary: "note", CreatedAt: time.Now()},
	}}

	runAll(m.ingestHunkComments(msg))
	first := len(*ran)
	for range 5 {
		runAll(m.ingestHunkComments(msg))
	}
	if len(*ran) != first {
		t.Errorf("re-polling ran %d more actions, want 0 — the same note is being delivered repeatedly", len(*ran)-first)
	}
}

// TestHunkCommentsPredatingTheSessionAreSkipped: opening the Diff tab against
// a hunk session that already existed (an external one the user had open)
// would otherwise dump its whole comment history into the agent at once.
func TestHunkCommentsPredatingTheSessionAreSkipped(t *testing.T) {
	m, ran := deliveryModel(t)
	livePipeSession(t, &m)
	start := time.Now()
	m.diffSessions["baron-a"] = &agentTerminal{
		brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24), startedAt: start,
	}

	cmd := m.ingestHunkComments(hunkCommentsMsg{brn: "baron-a", comments: []hunk.Comment{
		{ID: "old", File: "a.go", Line: 1, Summary: "from before", CreatedAt: start.Add(-time.Hour)},
		{ID: "new", File: "a.go", Line: 2, Summary: "just now", CreatedAt: start.Add(time.Second)},
	}})
	runAll(cmd)
	joined := strings.Join(*ran, "\n")
	if strings.Contains(joined, "from before") {
		t.Error("a comment predating the diff session was ingested; a pre-existing session would flood the agent")
	}
	if !strings.Contains(joined, "just now") {
		t.Errorf("the new comment was not ingested; actions run: %v", *ran)
	}
}

// TestHunkPollingIsRateLimitedAndOffByDefault: the poll rides the 42fps frame
// tick, so without rate limiting it would shell out to the hunk CLI 24 times a
// second — and with no hunk installed it must not run at all.
func TestHunkPollingIsRateLimitedAndOffByDefault(t *testing.T) {
	m, _ := deliveryModel(t)
	m.diffSessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24)}

	if cmd := m.maybePollHunkCmd(); cmd != nil {
		t.Fatal("polled with no HunkComments wired — hunk isn't installed, there is nothing to poll")
	}

	polls := 0
	m.deps.HunkComments = func(string) ([]hunk.Comment, error) {
		polls++
		return nil, nil
	}
	for range 50 {
		if cmd := m.maybePollHunkCmd(); cmd != nil {
			cmd()
		}
	}
	if polls != 1 {
		t.Errorf("50 frame ticks produced %d polls, want 1 — the rate limit is not holding", polls)
	}
}

// TestHunkPollingNeverOverlapsItself: a poll that hasn't returned yet must
// not be dispatched again even once hunkPollInterval has elapsed since it
// started — lastHunkPoll alone can't tell "still running" from "long done",
// since it's stamped at dispatch time, not completion. Without
// hunkPollInFlight, a poll slower than the interval (the hunk CLI under
// load) would let the next frame tick fire a second, concurrent poll on top
// of the first.
func TestHunkPollingNeverOverlapsItself(t *testing.T) {
	m, _ := deliveryModel(t)
	m.diffSessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(80, 24)}
	polls := 0
	m.deps.HunkComments = func(string) ([]hunk.Comment, error) {
		polls++
		return nil, nil
	}

	cmd1 := m.maybePollHunkCmd()
	if cmd1 == nil {
		t.Fatal("first poll: cmd = nil, want a dispatch")
	}
	if !m.hunkPollInFlight {
		t.Fatal("hunkPollInFlight = false after dispatch, want true")
	}
	// cmd1 is deliberately not executed yet, standing in for a poll that's
	// still running when the interval next elapses.
	m.lastHunkPoll = time.Now().Add(-2 * hunkPollInterval)

	if cmd2 := m.maybePollHunkCmd(); cmd2 != nil {
		t.Fatal("polled again while the first poll was still in flight")
	}
	if polls != 0 {
		t.Fatalf("polls = %d, want 0 before cmd1 has even run", polls)
	}

	// The still-outstanding first dispatch, once it actually runs and its
	// result is routed back through Update, both proves the mechanism
	// really does call HunkComments (not just never) and releases the guard.
	next, _ := m.Update(cmd1())
	m = asModel(next)
	if polls != 1 {
		t.Fatalf("polls = %d, want 1 after the in-flight poll actually completes", polls)
	}
	if m.hunkPollInFlight {
		t.Fatal("hunkPollInFlight = true after hunkCommentsMsg, want false")
	}
}

// TestHunkPollingStopsWithoutADiffSession: no Diff tab open means no session
// to read comments from, so nothing should be shelling out.
func TestHunkPollingStopsWithoutADiffSession(t *testing.T) {
	m, _ := deliveryModel(t)
	m.deps.HunkComments = func(string) ([]hunk.Comment, error) { return nil, nil }

	if cmd := m.maybePollHunkCmd(); cmd != nil {
		t.Error("polled with no live Diff session")
	}
	m.diffSessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindDiff, done: true, emu: vt.NewSafeEmulator(80, 24)}
	if cmd := m.maybePollHunkCmd(); cmd != nil {
		t.Error("polled a finished Diff session")
	}
}
