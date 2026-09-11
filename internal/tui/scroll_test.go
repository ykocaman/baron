package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/store"
)

// pipedSession attaches a live session of kind for baron-a backed by a real
// pipe, so whatever BARON writes toward the child can be read back.
func pipedSession(t *testing.T, m *Model, kind termKind, tmuxWindow string) (*agentTerminal, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	cols, rows := m.termDims()
	term := &agentTerminal{
		brn: "baron-a", kind: kind, emu: vt.NewSafeEmulator(cols, rows), pty: w,
		tmuxWindow: tmuxWindow, cols: cols, rows: rows,
	}
	m.sessionsFor(kind)[term.brn] = term
	return term, r
}

// readSoon reads whatever is available on r within a short window. The pipe is
// never closed here, so a plain Read would block forever when the code under
// test (correctly or not) wrote nothing — hence the deadline.
func readSoon(t *testing.T, r *os.File) string {
	t.Helper()
	_ = r.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil && n == 0 && !os.IsTimeout(err) {
		t.Fatalf("read from pty pipe: %v", err)
	}
	return string(buf[:n])
}

// scrollBaseModel returns a sized dashboard Model on the given detail tab,
// with host wired in when one is supplied (nil means tmux is unavailable).
func scrollBaseModel(t *testing.T, tab int, host *fakeAgentHost) Model {
	t.Helper()
	d := testDeps()
	if host != nil {
		d.AgentHost = host
	}
	m := New(context.Background(), d)
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusWorking}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = tab
	m.detail = beads[0]
	m.liveBRN = "baron-a"
	return m
}

// tmuxScrollModel returns a Model whose Terminal-tab session is tmux-backed
// and whose host serves history as the pane's scrollback.
func tmuxScrollModel(t *testing.T, history []string) (Model, *agentTerminal, *fakeAgentHost) {
	t.Helper()
	host := &fakeAgentHost{aliveWindows: map[string]bool{}, captured: strings.Join(history, "\n")}
	m := scrollBaseModel(t, 1, host)
	cols, rows := m.termDims()
	term := &agentTerminal{
		brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(cols, rows),
		tmuxWindow: "baron-a", cols: cols, rows: rows,
	}
	m.sessions["baron-a"] = term
	return m, term, host
}

// numberedLines returns 300 numbered fixture lines, the fixed scrollback
// size every test in this file scrolls through.
func numberedLines() []string {
	const n = 300
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("HISTLINE_%03d", i))
	}
	return out
}

// drainCmd runs cmd and feeds its message back into the model, as the runtime
// would.
func drainCmd(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	next, _ := m.Update(cmd())
	return asModel(next)
}

// TestTmuxScrollReadsThePaneHistory is the fix for the scroll report that
// survived two wrong attempts.
//
// opencode --mini does not paint its own screen — it prints, like a normal
// program — so there is nothing inside it to scroll, and forwarding a scroll
// key to it does nothing (measured: PageUp, ctrl+alt+u, shift+up, ctrl+u and
// the mouse wheel all left its screen byte-identical). Its backlog is the tmux
// pane's scrollback, which tmux does keep: 20 of 90 printed lines were on
// screen and capture-pane returned all 90.
func TestTmuxScrollReadsThePaneHistory(t *testing.T) {
	m, term, host := tmuxScrollModel(t, numberedLines())

	next, cmd := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 80, Y: 10})
	m = asModel(next)
	if cmd == nil {
		t.Fatal("scrolling asked the host for nothing — there is no other source for a tmux session's backlog")
	}
	m = drainCmd(m, cmd)

	host.mu.Lock()
	calls := len(host.captureCalls)
	host.mu.Unlock()
	if calls != 1 {
		t.Fatalf("host captured %d times, want 1", calls)
	}
	if len(term.history) != 300 {
		t.Fatalf("captured %d history lines, want 300", len(term.history))
	}
	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), m.panes["baron-a"].offset)
	if !strings.Contains(out, "HISTLINE_") {
		t.Errorf("scrolled pane rendered no history:\n%s", out)
	}
	if !strings.Contains(out, "scrollback") {
		t.Errorf("scrolled pane gave no sign it has stopped following the agent:\n%s", out)
	}
}

// TestTmuxScrollReachesTheOldestLines: paging up repeatedly has to arrive at
// the top of the backlog, not stall partway.
func TestTmuxScrollReachesTheOldestLines(t *testing.T) {
	m, term, _ := tmuxScrollModel(t, numberedLines())
	for range 40 {
		next, cmd := m.Update(key("pgup"))
		m = drainCmd(asModel(next), cmd)
	}
	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), m.panes["baron-a"].offset)
	if !strings.Contains(out, "HISTLINE_000") {
		t.Errorf("40 page-ups never reached the oldest line; showing:\n%s", out)
	}
}

// TestTmuxScrollBackToBottomFollowsTheAgentAgain: scrolling down to the end
// must hand the pane back to the live view, or it stays frozen on history
// while the agent keeps working.
func TestTmuxScrollBackToBottomFollowsTheAgentAgain(t *testing.T) {
	m, term, _ := tmuxScrollModel(t, numberedLines())
	term.feed([]byte("LIVE_AGENT_OUTPUT\r\n"))

	for range 10 {
		next, cmd := m.Update(key("pgup"))
		m = drainCmd(asModel(next), cmd)
	}
	if m.panes["baron-a"].offset == 0 {
		t.Fatal("fixture never scrolled away from the bottom")
	}
	for range 40 {
		next, cmd := m.Update(key("pgdown"))
		m = drainCmd(asModel(next), cmd)
	}
	if off := m.panes["baron-a"].offset; off != 0 {
		t.Fatalf("pane offset = %d after scrolling all the way down, want 0", off)
	}
	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), 0)
	if !strings.Contains(out, "LIVE_AGENT_OUTPUT") {
		t.Errorf("back at the bottom the pane is not showing the live agent:\n%s", out)
	}
}

// TestTmuxScrollDoesNotRecaptureOnEveryKey: holding a scroll key must not
// shell out to tmux per repeat.
func TestTmuxScrollDoesNotRecaptureOnEveryKey(t *testing.T) {
	m, _, host := tmuxScrollModel(t, numberedLines())
	for range 20 {
		next, cmd := m.Update(key("pgup"))
		m = drainCmd(asModel(next), cmd)
	}
	host.mu.Lock()
	calls := len(host.captureCalls)
	host.mu.Unlock()
	if calls > 2 {
		t.Errorf("20 scroll keys caused %d captures, want at most 2 — each one shells out to tmux", calls)
	}
}

// TestTmuxBackedSessionNeverScrollsItsOwnMirror: a tmux-backed session's
// emulator is a mirror of the current pane, so any scrollback it picks up is
// incidental; paging through it would show fragments while the real backlog
// sits in tmux.
func TestTmuxBackedSessionNeverScrollsItsOwnMirror(t *testing.T) {
	m, term, _ := tmuxScrollModel(t, numberedLines())
	term.emu.SetScrollbackSize(1000)
	for i := range 200 {
		term.feed(fmt.Appendf(nil, "mirrored line %d\r\n", i))
	}

	next, cmd := m.Update(key("pgup"))
	m = drainCmd(asModel(next), cmd)

	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), m.panes["baron-a"].offset)
	if strings.Contains(out, "mirrored line") {
		t.Errorf("the pane scrolled BARON's own mirror instead of the pane's real history:\n%s", out)
	}
	if !strings.Contains(out, "HISTLINE_") {
		t.Errorf("the pane is not showing the captured history:\n%s", out)
	}
}

// TestTmuxSessionWithNoBacklogForwardsToTheChild: a pane that has not scrolled
// yet, or whose child paints its own screen, has no history to page through.
// The gesture goes to the child, which may be able to scroll itself.
func TestTmuxSessionWithNoBacklogForwardsToTheChild(t *testing.T) {
	host := &fakeAgentHost{aliveWindows: map[string]bool{}, captured: "one\ntwo\n"}
	m := scrollBaseModel(t, 1, host)
	term, r := pipedSession(t, &m, kindAgent, "baron-a")

	// The first scroll probes the host and finds nothing to page through.
	next, cmd := m.Update(key("pgup"))
	m = drainCmd(asModel(next), cmd)
	if !term.historyUnavailable {
		t.Fatal("a pane with no backlog was not recognised as such")
	}
	next, _ = m.Update(key("pgup"))
	m = asModel(next)
	if got := readSoon(t, r); !strings.Contains(got, "\x1b[5~") {
		t.Errorf("child received %q, want Page Up forwarded to it", got)
	}
}

// TestHistoryUnavailableIsNotPermanent: an agent that has printed nothing yet
// will have printed plenty later. Treating "no backlog" as a permanent verdict
// means a scroll attempted a second too early leaves scrolling dead for the
// rest of the session. Measured on a real run: BARON's opencode pane reported
// history_size=0 while the agent was still thinking.
func TestHistoryUnavailableIsNotPermanent(t *testing.T) {
	host := &fakeAgentHost{aliveWindows: map[string]bool{}}
	m := scrollBaseModel(t, 1, host)
	term, _ := pipedSession(t, &m, kindAgent, "baron-a")

	next, cmd := m.Update(key("pgup"))
	m = drainCmd(asModel(next), cmd)
	if !term.historyUnavailable {
		t.Fatal("fixture did not reach the no-backlog state this test is about")
	}

	// The agent gets going and the pane starts scrolling.
	host.mu.Lock()
	host.captured = strings.Join(numberedLines(), "\n")
	host.mu.Unlock()
	term.capturedAt = time.Now().Add(-time.Minute) // let the rate limit expire

	next, cmd = m.Update(key("pgup"))
	m = asModel(next)
	if cmd == nil {
		t.Fatal("no re-check after the agent started producing output — scrolling stays dead forever")
	}
	m = drainCmd(m, cmd)
	if term.historyUnavailable {
		t.Error("still reporting no backlog after a capture returned 300 lines")
	}
	next, cmd = m.Update(key("pgup"))
	m = drainCmd(asModel(next), cmd)
	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), m.panes["baron-a"].offset)
	if !strings.Contains(out, "HISTLINE_") {
		t.Errorf("scrolling never recovered once history existed:\n%s", out)
	}
}

// TestScrollWithoutTmuxSaysSo: with no tmux there is no record of an agent's
// output anywhere — tmux is the only thing that keeps one, and BARON
// deliberately keeps none of its own (that is what the vendored terminal-
// emulator fork existed for, and it is gone). Rather than appear to scroll and
// move nothing, say what would make it work.
func TestScrollWithoutTmuxSaysSo(t *testing.T) {
	m := scrollBaseModel(t, 1, nil) // no AgentHost: tmux unavailable
	_, r := pipedSession(t, &m, kindAgent, "")

	for _, k := range []string{"pgup", "shift+up", "pgdown"} {
		next, cmd := m.Update(key(k))
		m = asModel(next)
		if cmd == nil {
			t.Fatalf("%s produced no response at all", k)
		}
		// Checked straight after Update: notify sets the message and returns
		// its own dismiss timer, so running that cmd would just clear it
		// again (and wait out the timeout doing it).
		if !strings.Contains(m.statusMsg, "tmux") {
			t.Errorf("%s: statusMsg = %q, want it to point at installing tmux", k, m.statusMsg)
		}
	}
	if got := readSoon(t, r); got != "" {
		t.Errorf("child received %q — without tmux nothing should be sent; the notice is the whole response", got)
	}
}

// TestDiffTabScrollsWithoutTmux: the Diff tab is deliberately not tmux-backed
// (a tmux-hosted diff viewer per bead is what accumulated hundreds of
// sessions), but it runs a pager that scrolls itself — so it forwards rather
// than telling the user to install tmux, which would be both wrong and useless
// there. Verified against hunk: PageUp/PageDown both move its content.
func TestDiffTabScrollsWithoutTmux(t *testing.T) {
	m := scrollBaseModel(t, 2, nil) // Diff tab, no AgentHost
	_, r := pipedSession(t, &m, kindDiff, "")

	next, _ := m.Update(key("pgup"))
	m = asModel(next)
	if got := readSoon(t, r); !strings.Contains(got, "\x1b[5~") {
		t.Fatalf("diff pane sent %q, want Page Up forwarded to the pager", got)
	}
	if strings.Contains(m.statusMsg, "tmux") {
		t.Errorf("statusMsg = %q — the Diff tab does not need tmux, so telling the user to install it is wrong", m.statusMsg)
	}

	next, _ = m.Update(key("pgdown"))
	m = asModel(next)
	if got := readSoon(t, r); !strings.Contains(got, "\x1b[6~") {
		t.Errorf("diff pane sent %q, want Page Down", got)
	}
}

// TestScrollForwardingNeedsNoFocus: reading is not typing. Requiring 't' first
// — then shift+esc to get back — to scroll a pane already on screen is the
// friction this avoids.
func TestScrollForwardingNeedsNoFocus(t *testing.T) {
	m := scrollBaseModel(t, 2, nil)
	_, r := pipedSession(t, &m, kindDiff, "")
	if m.termFocus {
		t.Fatal("fixture started focused; this test is about the unfocused case")
	}
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 80, Y: 10})
	m = asModel(next)
	if got := readSoon(t, r); got == "" {
		t.Error("an unfocused pane must still forward scroll — focus is for typing, not reading")
	}
}

// TestScrollbackKeepsItsColour: the captured history carries the text's real
// colours (tmux capture-pane -e), because without them scrolling back turned
// the pane monochrome and scrolling to the bottom brought the colour back —
// the live frame is the emulator's own render, which never lost it.
func TestScrollbackKeepsItsColour(t *testing.T) {
	coloured := make([]string, 0, 300)
	for i := range 300 {
		coloured = append(coloured, fmt.Sprintf("\x1b[32mHISTLINE_%03d\x1b[m", i))
	}
	m, term, _ := tmuxScrollModel(t, coloured)

	next, cmd := m.Update(key("pgup"))
	m = drainCmd(asModel(next), cmd)

	out := m.viewEmbeddedTerminal(term, 80, m.detailPaneHeight(), m.panes["baron-a"].offset)
	if !strings.Contains(out, "HISTLINE_") {
		t.Fatalf("no history rendered:\n%s", out)
	}
	if !strings.Contains(out, "\x1b[32m") {
		t.Errorf("scrolled-back history lost its colour:\n%q", out)
	}
	// And every rendered line must close what it opened, or the colour bleeds
	// into the rest of the frame.
	for _, ln := range strings.Split(out, "\n")[1:] {
		if strings.Contains(ln, "\x1b[32m") && !strings.HasSuffix(ln, "\x1b[m") {
			t.Errorf("history line leaves styling open, which bleeds into the frame: %q", ln)
			break
		}
	}
}
