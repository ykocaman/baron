package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

func testDeps() Deps {
	return Deps{
		NoColor:    true,
		Theme:      "dark",
		Version:    "test",
		BaseBranch: "main",
		Beads:      testBeadStore(),
		Models: func() ([]string, error) {
			return []string{"opencode", "claude", "gemini"}, nil
		},
		// CreateBead/ChangeStatus/Assign/CloseBead/Comment default to a
		// canned "ran: ..." response so a test that doesn't care about one
		// of these actions (most of them) doesn't panic on a nil func call
		// the moment some other code path happens to trigger it (e.g. a
		// hunk comment landing while the test is really about something
		// else). A test that cares about the real args/response overrides
		// the specific field it needs.
		CreateBead: func(title, description, accept, priority, issueType, parent, tier string) (string, error) {
			return fmt.Sprintf("ran: work create %s --tier %s", title, tier), nil
		},
		ChangeStatus: func(brn, target string) (string, error) {
			return fmt.Sprintf("ran: work status %s %s", brn, target), nil
		},
		Assign: func(brn, agentName, modelID, effort string) (string, error) {
			return fmt.Sprintf("ran: work assign %s %s %s %s", brn, agentName, modelID, effort), nil
		},
		CloseBead: func(brn string) (string, error) {
			return "ran: work close " + brn, nil
		},
		Comment: func(brn, text string) (string, error) {
			return fmt.Sprintf("ran: work comment %s %s", brn, text), nil
		},
		EditBead: func(brn, title, description string) (string, error) {
			return fmt.Sprintf("ran: work edit %s --title %s", brn, title), nil
		},
		Merge: func(brn string) (string, error) {
			return "ran: merge " + brn, nil
		},
		Resume: func(brn string) (string, error) {
			return "ran: run " + brn + " --resume", nil
		},
	}
}

// testBeadStore returns a BeadStore whose runner serves the bd calls the TUI
// makes: an empty bead list and one canned comment.
func testBeadStore() *store.BeadStore {
	runner := store.RunnerFunc(func(_ context.Context, _ string, args []string, _ tool.Options) (tool.Result, error) {
		switch args[0] {
		case "list":
			return tool.Result{Stdout: "[]"}, nil
		case "comments":
			return tool.Result{Stdout: `[{"id":"c1","issue_id":"a","author":"alice","text":"looks good","created_at":"2026-08-10T11:34:57Z"}]`}, nil
		}
		return tool.Result{}, fmt.Errorf("unexpected bd args: %v", args)
	})
	s := store.NewBeadStore(runner, ".")
	s.SetBRNPrefix("baron")
	return s
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "ctrl+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "shift+esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "shift+left":
		return tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	case "shift+right":
		return tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case " ":
		// KeyPressMsg.String() resolves Code before falling back to Text,
		// so a bare {Text: " "} stringifies as a NUL byte, not " " — the
		// real terminal library sends Code: tea.KeySpace for an actual
		// spacebar press, so this synthesizes that instead of the
		// Text-only default below.
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	default:
		return tea.KeyPressMsg{Text: s}
	}
}

// pumpModels executes the picker's async model-load command and feeds the
// result back in, landing the picker in its loaded state. The command may be
// a batch (e.g. 'r' on an unassigned bead pairs the load with a notice), so
// a possible tea.BatchMsg wrapper is unwrapped.
// flattenBatch fully unwraps msg into the flat sequence of leaf (non-batch)
// tea.Msg values it resolves to — recursing into every nested tea.BatchMsg
// by calling each of its commands — so tests never need their own bespoke
// recursive walk to find one message type inside a possibly-nested batch.
// A non-batch msg flattens to itself as the only element.
func flattenBatch(msg tea.Msg) []tea.Msg {
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c != nil {
			out = append(out, flattenBatch(c())...)
		}
	}
	return out
}

func pumpModels(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd = nil, want the async model-load command")
	}
	var load modelsLoadedMsg
	var found bool
	for _, msg := range flattenBatch(cmd()) {
		if load, found = msg.(modelsLoadedMsg); found {
			break
		}
	}
	if !found {
		t.Fatal("cmd(), want modelsLoadedMsg somewhere in the batch")
	}
	next, _ := m.Update(load)
	return asModel(next)
}

func TestModelLoadsBeadsOnInit(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "First", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	got := asModel(next)
	if len(got.beads) != 1 || got.beads[0].BRN != "baron-a" {
		t.Errorf("beads = %+v, want the loaded bead", got.beads)
	}
	if !strings.Contains(got.View().Content, "First") {
		t.Errorf("View() = %q, want it to show the bead title", got.View().Content)
	}
}

// TestModelEnterStaysOnDashboard: enter no longer opens a full-screen detail
// window — the selected bead's detail is already in the right pane, so enter
// is a no-op on the single-screen dashboard.
func TestModelEnterStaysOnDashboard(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen, Description: "desc text"}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, _ = m.Update(key("enter"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard (no detail-screen jump)", m.screen)
	}
	if m.detail.BRN != "baron-a" {
		t.Fatalf("detail = %+v, want baron-a selected", m.detail)
	}
	if !strings.Contains(m.View().Content, "desc text") {
		t.Errorf("View() = %q, want the bead description in the right pane", m.View().Content)
	}
}

// TestModelEscGoesBack: esc always goes back one screen — the only back
// key; q is reserved for quit, never context-sensitive.
// TestModelEscGoesBack: esc always goes back one screen — the only back
// key; q is reserved for quit, never context-sensitive.
func TestModelEscGoesBack(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("?"))
	m = asModel(next)
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want screenHelp", m.screen)
	}

	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back to screenDashboard after esc", m.screen)
	}
	if m.quitting {
		t.Fatal("quitting = true, want esc from a sub-screen to go back, not quit")
	}

	// esc at the dashboard (nowhere left to go back to) is a no-op, not quit.
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	if m.screen != screenDashboard || m.confirming != "" {
		t.Fatalf("screen = %v confirming = %q, want esc at the dashboard to no-op", m.screen, m.confirming)
	}
}

// TestModelQAlwaysQuits: q always opens the quit confirmation, from any
// screen, never "back".
// TestModelQAlwaysQuits: q always opens the quit confirmation, from any
// screen, never "back".
func TestModelQAlwaysQuits(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, cmd := m.Update(key("q"))
	m = asModel(next)
	if m.confirming != "quit" {
		t.Fatalf("confirming = %q, want the quit confirmation dialog after q", m.confirming)
	}
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want q to leave the screen alone (only the confirm overlay opens)", m.screen)
	}
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil (dialog waits for confirmation)", cmd)
	}

	next, cmd = m.Update(key("y"))
	m = asModel(next)
	if !m.quitting || cmd == nil {
		t.Fatal("y on the quit confirmation should quit")
	}
}

func TestModelHelpToggle(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("?"))
	m = asModel(next)
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want screenHelp", m.screen)
	}
	if !strings.Contains(m.View().Content, "command bar") {
		t.Errorf("View() = %q, want help text", m.View().Content)
	}
	next, _ = m.Update(key("?"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back to dashboard", m.screen)
	}
}

func TestModelCommandBarRunsCommand(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key(":"))
	m = asModel(next)
	if !m.cmdMode {
		t.Fatal("cmdMode = false, want true after :")
	}
	for _, r := range "echo hi" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	if !m.cmdMode {
		t.Fatal("cmdMode = false, want true after enter (stays in cmd mode)")
	}
	if m.cmdInput.Value() != "" {
		t.Fatalf("cmdInput = %q, want empty after enter", m.cmdInput.Value())
	}
	if cmd == nil {
		t.Fatal("expected a command to run the typed command line")
	}
	msg := cmd()
	ran, ok := msg.(shellRanMsg)
	if !ok {
		t.Fatalf("msg = %T, want shellRanMsg", msg)
	}
	// "echo hi" is executed as a bash shell command now.
	if !strings.Contains(strings.Join(ran.lines, "\n"), "hi") {
		t.Errorf("output = %q, want the bash output", strings.Join(ran.lines, "\n"))
	}
}

func TestModelCommandBarEscCancels(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key(":"))
	m = asModel(next)
	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.cmdMode {
		t.Fatal("cmdMode = true, want false after shift+esc")
	}
}

func TestModelQuitConfirmation(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("q"))
	m = asModel(next)
	if m.huhForm == nil {
		t.Fatalf("huhForm = nil, want quit confirm form")
	}
	if !strings.Contains(m.View().Content, "Quit BARON?") {
		t.Errorf("View() = %q, want the quit confirm prompt", m.View().Content)
	}

	next, cmd := m.Update(key("y"))
	m = asModel(next)
	// Accept advances the single-field group via an async NextField cmd
	// that only a real tea.Program would pump back in, so form.State never
	// reaches StateCompleted here — a non-nil cmd is treated as the
	// synchronous completion signal instead (same convention as
	// updateStatusMenuKey/updateModelPickerKey), and the confirmed action
	// runs immediately rather than waiting on a later round trip.
	if !m.quitting || cmd == nil {
		t.Fatal("y on the quit confirmation should quit immediately")
	}
	if m.huhForm != nil || m.confirming != "" {
		t.Errorf("huhForm/confirming not cleared after y: huhForm=%v confirming=%q", m.huhForm, m.confirming)
	}
}

// TestSplitPaneMKeyNoOpenPRShowsStatus: 'm' on a bead not yet ready to merge
// must not try to run a nonexistent command — a branch becomes ready
// automatically once a run's gate passes; there's no manual "ready" path.
// Discovered via live tmux QA: the old code (copied from bead-detail's
// pre-existing 'p' handler) ran a nonexistent command and dumped its cobra
// usage-error text onto the command-output screen.
func TestSplitPaneMKeyNoOpenPRShowsStatus(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, cmd := m.Update(key("m"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want to stay on the dashboard (no command-output bounce)", m.screen)
	}
	if !strings.Contains(m.statusMsg, "no branch ready to merge yet") {
		t.Errorf("statusMsg = %q, want a friendly not-ready-yet notice", m.statusMsg)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want the status auto-dismiss timer")
	}
}

// TestSplitPaneAKeyOpensModelPicker: 'a' is the single, consistent
// assign-model key everywhere a bead action applies (replaces human queue's
// old 'r').
func TestSplitPaneAKeyOpensModelPicker(t *testing.T) {
	deps := testDeps()
	deps.Models = func() ([]string, error) { return []string{"claude"}, nil }
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelForm == nil {
		t.Fatal("modelForm = nil, want the huh form open after a")
	}
}

// TestSplitPaneXKeyStopsAgentAndQueuesForHuman: x is one gesture regardless
// of which detail tab is focused or whether an agent happens to be running
// — stop the agent if there is one, then send the bead to human_queue
// (the "Needs You" column). A state that can't reach human_queue at all
// (e.g. "open" — no agent to stop, nothing for a human to be summoned
// about) gets a notice instead of a confirm prompt.
func TestSplitPaneXKeyStopsAgentAndQueuesForHuman(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "" {
		t.Fatalf("confirming = %q, want no confirm prompt from open (can't reach human_queue)", m.confirming)
	}
	if !strings.Contains(m.statusMsg, "can't be sent to Needs You") {
		t.Errorf("statusMsg = %q, want an explanation notice", m.statusMsg)
	}

	// working -> human_queue is a valid transition; x prompts for it whether
	// or not a session happens to be running. A fresh model, not a reload
	// of the one above: switching the same BRN's status across a reload
	// can move it to a different board tab than the cursor is currently
	// on, which is its own concern (cursor-follows-selection on reload),
	// not what this test is about.
	m = New(context.Background(), testDeps())
	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}})
	m = asModel(next)
	m.detailTab = 1
	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "stop" {
		t.Fatalf("confirming = %q, want stop with no running session", m.confirming)
	}

	m.confirming = ""
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5)}
	next, _ = m.Update(key("x"))
	m = asModel(next)
	if m.confirming != "stop" {
		t.Fatalf("confirming = %q, want stop with a running session too", m.confirming)
	}

	// Confirming kills the session (SIGHUP via pty close; nil pty here) and
	// moves the bead to human_queue.
	next, cmd := m.Update(key("y"))
	m = asModel(next)
	if m.confirming != "" {
		t.Fatalf("confirming = %q, want cleared after confirm", m.confirming)
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			next, _ = m.Update(c())
			m = asModel(next)
		}
	} else {
		next, _ = m.Update(msg)
		m = asModel(next)
	}
	if !strings.Contains(m.statusMsg, "human_queue") {
		t.Errorf("statusMsg = %q, want it to confirm the human_queue transition", m.statusMsg)
	}
}

// TestRemovedKeysAreNoOps: r/S/M/H no longer do anything on the split-pane
// dashboard — freed by the keybinding redesign (r/m unified into p, S
// unified into x, M moved to the command bar, H removed since Human Queue
// is a list tab, not a screen).
// TestRemovedKeysAreNoOps: R/S/M/H/b no longer do anything on the dashboard —
// freed by the keybinding redesign (r is now run, uppercase R is gone, S
// unified into x, M moved to the command bar, H removed since Human Queue
// is a list tab, and b's kanban board is gone).
func TestRemovedKeysAreNoOps(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	for _, k := range []string{"R", "S", "M", "H", "b"} {
		next, cmd := m.Update(key(k))
		got := asModel(next)
		if got.screen != screenDashboard || cmd != nil {
			t.Errorf("key %q: screen = %v, cmd = %v, want no-op (screenDashboard, nil cmd)", k, got.screen, cmd)
		}
	}
}

// TestConfirmDialogPlainEnterConfirms: plain enter must confirm like e/y,
// not silently no-op (the "silent-enter bug").
// TestConfirmDialogPlainEnterConfirms: plain "enter" is huh's Confirm Submit
// key, which moves the field along but never flips the bound value — the
// dialog defaults to false (n), so bare enter must decline, not confirm.
// Only the explicit y/Y (Accept) key confirms.
func TestConfirmDialogPlainEnterConfirms(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("q"))
	m = asModel(next)
	if m.huhForm == nil {
		t.Fatalf("huhForm = nil, want quit confirm form")
	}
	next, _ = m.Update(key("enter"))
	m = asModel(next)
	if m.quitting {
		t.Fatal("quitting = true after plain enter, want the default (no) to decline")
	}
	if m.huhForm != nil || m.confirming != "" {
		t.Errorf("huhForm/confirming not cleared after enter: huhForm=%v confirming=%q", m.huhForm, m.confirming)
	}

	// Reopen and use "y" (Accept), which sets value=true and confirms.
	next, _ = m.Update(key("q"))
	m = asModel(next)
	next, cmd := m.Update(key("y"))
	m = asModel(next)
	if !m.quitting || cmd == nil {
		t.Fatal("y (Accept) should confirm and quit")
	}
}

// TestModelQuitConfirmationDeclined: a stray key must leave the quit dialog
// up (modal) — only n/esc/q decline. The old "any key dismisses" behavior
// let a stray 'c' silently drop the dialog so a later enter closed a bead
// the user never meant to touch.
func TestModelQuitConfirmationDeclined(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(key("q"))
	m = asModel(next)
	next, _ = m.Update(key("h")) // stray key: ignored, dialog stays up
	m = asModel(next)
	if m.quitting {
		t.Fatal("quitting = true, want the stray key to leave the dialog up")
	}
	if m.huhForm == nil {
		t.Fatalf("huhForm = nil, want quit confirm form (modal: stray keys must not dismiss)")
	}
	next, _ = m.Update(key("n")) // the real decline key
	m = asModel(next)
	if m.quitting {
		t.Fatal("quitting = true after n, want declined")
	}
	// n declines synchronously (same convention as y confirming — see
	// TestModelQuitConfirmation) and closes the dialog immediately.
	if m.huhForm != nil || m.confirming != "" {
		t.Errorf("huhForm/confirming not cleared after n: huhForm=%v confirming=%q", m.huhForm, m.confirming)
	}
}

func TestModelHumanQueueFilter(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusHumanQueue},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if len(m.humanQueue) != 1 || m.humanQueue[0].BRN != "baron-b" {
		t.Errorf("humanQueue = %+v, want only baron-b", m.humanQueue)
	}
}

// TestOverviewShowsReadyToMergeBranch: a mergable bead's Overview tab shows
// its branch inline (derived locally, see mergeTargetForBead) — the
// shown inline once the bead is ready to merge.
func TestOverviewShowsReadyToMergeBranch(t *testing.T) {
	m := New(context.Background(), testDeps())
	bead := store.Bead{BRN: "baron-a", ID: "a", IssueType: "task", Title: "A", Status: store.BeadStatusMergable}
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{bead}})
	m = asModel(next)
	m.width, m.height = 100, 30
	wantBranch := domain.BranchName(bead.IssueType, bead.ID)
	if !strings.Contains(m.View().Content, "Ready to Merge") || !strings.Contains(m.View().Content, wantBranch) {
		t.Errorf("View() = %q, want it to show the ready-to-merge branch %q", m.View().Content, wantBranch)
	}
}

// TestOverviewShowsRetryReason: a retry bead's Overview tab shows why it
// needs one (the last audit event's detail) and the retry/cancel hint.
func TestOverviewShowsRetryReason(t *testing.T) {
	m := New(context.Background(), testDeps())
	bead := store.Bead{BRN: "baron-a", ID: "a", IssueType: "task", Title: "A", Status: store.BeadStatusRetry}
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{bead}})
	m = asModel(next)
	m.width, m.height = 100, 30
	m.auditEvents = []store.AuditEvent{
		{Time: time.Now(), Action: "retry", Target: "baron-a", Detail: "retry 1/3: agent failed [claude: API Error: 529 Overloaded]"},
	}
	view := m.View().Content
	if !strings.Contains(view, "Needs a Retry") {
		t.Errorf("View() = %q, want a Needs a Retry section", view)
	}
	if !strings.Contains(view, "API Error") {
		t.Errorf("View() = %q, want the failure reason shown", view)
	}
	if !strings.Contains(view, "r retry now") {
		t.Errorf("View() = %q, want the retry/cancel hint", view)
	}
}

// TestOverviewShowsHumanQueueReason: a human_queue bead's Overview tab shows
// why it landed there and the reassign/status hint.
func TestOverviewShowsHumanQueueReason(t *testing.T) {
	m := New(context.Background(), testDeps())
	bead := store.Bead{BRN: "baron-a", ID: "a", IssueType: "task", Title: "A", Status: store.BeadStatusHumanQueue}
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{bead}})
	m = asModel(next)
	m.width, m.height = 100, 30
	m.auditEvents = []store.AuditEvent{
		{Time: time.Now(), Action: "retry", Target: "baron-a", Detail: "retry budget (3) exhausted: parked in human queue (silent death: no output for 5m0s)"},
	}
	view := m.View().Content
	if !strings.Contains(view, "Needs You") {
		t.Errorf("View() = %q, want a Needs You section", view)
	}
	if !strings.Contains(view, "budget (3) exhausted") {
		t.Errorf("View() = %q, want the failure reason shown", view)
	}
	if !strings.Contains(view, "r reassign and retry") {
		t.Errorf("View() = %q, want the reassign/status hint", view)
	}
}

// TestTabBarActiveCount: the Active tab's own count is the board's single
// source of truth for "how many beads are Active" — the header used to
// duplicate this number separately (and could disagree with it), but now
// only the tab bar shows it at all.
func TestTabBarActiveCount(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusWorking},
		{BRN: "baron-c", Title: "C", Status: store.BeadStatusValidating},
		{BRN: "baron-d", Title: "D", Status: store.BeadStatusClosed},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 100, 30
	if !strings.Contains(m.View().Content, "Active 2") {
		t.Errorf("View() = %q, want the Active tab to show 2", m.View().Content)
	}
	if strings.Contains(m.View().Content, "Active:") {
		t.Errorf("View() = %q, want the header to no longer duplicate the Active count", m.View().Content)
	}
}

func TestModelNoColorProducesNoANSI(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if strings.Contains(m.View().Content, "\x1b[") {
		t.Errorf("View() contains ANSI escapes with NoColor set: %q", m.View().Content)
	}
}

func TestModelWindowSize(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = asModel(next)
	if m.width != 100 || m.height != 40 {
		t.Errorf("size = (%d,%d), want (100,40)", m.width, m.height)
	}
}

func TestDetailLoadsCommentsOnOpen(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, cmd := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if cmd == nil {
		t.Fatal("expected a loadComments command when the first bead is auto-selected")
	}
	var gotComments bool
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if _, ok := c().(commentsLoadedMsg); ok {
				gotComments = true
			}
		}
	case commentsLoadedMsg:
		gotComments = true
	}
	if !gotComments {
		t.Fatal("bead auto-selection did not load comments")
	}
	next, _ = m.Update(commentsLoadedMsg{comments: []store.Comment{{Author: "alice", Text: "looks good", CreatedAt: time.Date(2026, 8, 10, 11, 34, 57, 0, time.UTC)}}})
	m = asModel(next)
	view := m.View().Content
	if !strings.Contains(view, "alice") || !strings.Contains(view, "looks good") {
		t.Errorf("View() = %q, want the comment author and text in the right pane", view)
	}
}

func TestCommentSubmitStaysOnDetailAndRefreshes(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want screenDashboard", m.screen)
	}

	next, _ = m.Update(key("c"))
	m = asModel(next)
	if m.screen != screenForm {
		t.Fatalf("screen = %v, want screenForm after c", m.screen)
	}
	for _, r := range "nice work" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want back on the dashboard after submit", m.screen)
	}
	if m.detailTab != 0 {
		t.Fatalf("detailTab = %d, want 0 (Overview tab with comments)", m.detailTab)
	}
	if !m.commentsExpanded {
		t.Fatalf("commentsExpanded = false, want true after submitting a comment")
	}
	ad := firstActionDone(t, cmd)
	if !strings.Contains(ad.msg, "work comment baron-a nice work") {
		t.Errorf("action msg = %q, want the comment command echoed", ad.msg)
	}

	next, batchCmd := m.Update(ad)
	m = asModel(next)
	if !strings.Contains(m.statusMsg, "work comment baron-a nice work") {
		t.Errorf("statusMsg = %q, want the action result", m.statusMsg)
	}
	batch, ok := batchCmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg of reload commands", batchCmd())
	}
	var gotBeads, gotComments bool
	for _, c := range batch {
		switch msg := c().(type) {
		case beadsLoadedMsg:
			gotBeads = true
			next, _ = m.Update(msg)
			m = asModel(next)
		case commentsLoadedMsg:
			gotComments = true
			next, _ = m.Update(msg)
			m = asModel(next)
		}
	}
	if !gotBeads || !gotComments {
		t.Errorf("reload batch = beads:%v comments:%v, want both reloads", gotBeads, gotComments)
	}
	if len(m.comments) != 1 {
		t.Errorf("comments = %+v, want the refreshed canned comment", m.comments)
	}
}

func TestCommentRendersUTCTime(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	next, _ = m.Update(commentsLoadedMsg{comments: []store.Comment{
		{Author: "bob", Text: "ship it", CreatedAt: time.Date(2026, 8, 10, 11, 34, 57, 0, time.UTC)},
	}})
	m = asModel(next)
	m.detailTab = 0
	view := m.View().Content
	if !strings.Contains(view, "2026-08-10 11:34") {
		t.Errorf("View() = %q, want the UTC timestamp 2026-08-10 11:34", view)
	}
	if strings.Contains(view, "11:34:57") {
		t.Errorf("View() = %q, want no seconds in the timestamp", view)
	}
}
