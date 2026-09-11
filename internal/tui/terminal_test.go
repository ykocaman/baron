package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// TestTermKeyBytes: the bubbletea->pty byte mapping. Claude and opencode need
// printable text, enter, esc, arrows and ctrl+c/d to be driven at all.
func TestTermKeyBytes(t *testing.T) {
	cases := []struct {
		msg  tea.KeyPressMsg
		want string
	}{
		{key("a"), "a"},
		{key("hello"), "hello"},
		{key("enter"), "\r"},
		{key("tab"), "\t"},
		{key("esc"), "\x1b"},
		{key("up"), "\x1b[A"},
		{key("down"), "\x1b[B"},
		{tea.KeyPressMsg{Code: tea.KeyRight}, "\x1b[C"},
		{tea.KeyPressMsg{Code: tea.KeyLeft}, "\x1b[D"},
		{tea.KeyPressMsg{Code: tea.KeyBackspace}, "\x7f"},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "\x03"},
		{tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "\x04"},
	}
	for _, c := range cases {
		if got := string(termKeyBytes(c.msg)); got != c.want {
			t.Errorf("termKeyBytes(%s) = %q, want %q", c.msg, got, c.want)
		}
	}
}

// TestTerminalFocusForwardsAndEscapes: 't' focuses the embedded terminal —
// keystrokes (including a bare esc) go to the pty, not the TUI's
// navigation, until shift+esc gives focus back. A bare esc must reach the
// agent unconditionally: opencode (and most agent TUIs) use Esc themselves
// (closing a menu, canceling input), and esc-esc specifically is
// opencode's own gesture to interrupt a running turn — BARON intercepting
// it would bounce the user out of focus while leaving the agent running,
// exactly the opposite of what they wanted.
func TestTerminalFocusForwardsAndEscapes(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", pty: w, emu: vt.NewSafeEmulator(10, 5)}

	next, _ = m.Update(key("t"))
	m = asModel(next)
	if !m.termFocus {
		t.Fatal("t must focus the terminal")
	}

	// 'j' while focused goes to the pty, not the list.
	next, _ = m.Update(key("j"))
	m = asModel(next)
	if m.listCursor != 0 {
		t.Fatalf("listCursor = %d, want 0 (focused j must not move the list)", m.listCursor)
	}
	buf := make([]byte, 4)
	n, _ := r.Read(buf)
	if string(buf[:n]) != "j" {
		t.Fatalf("pty received %q, want the forwarded 'j'", buf[:n])
	}

	// A bare esc (even twice in a row — opencode's own interrupt gesture)
	// must always forward and never unfocus.
	for i := range 2 {
		next, _ = m.Update(key("esc"))
		m = asModel(next)
		if !m.termFocus {
			t.Fatalf("esc #%d must not unfocus the terminal", i+1)
		}
		n, _ = r.Read(buf)
		if string(buf[:n]) != "\x1b" {
			t.Fatalf("pty received %q, want the forwarded esc byte", buf[:n])
		}
	}

	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.termFocus {
		t.Fatal("shift+esc must unfocus the terminal")
	}
	if m.statusMsg != "terminal unfocused" {
		t.Fatalf("statusMsg = %q, want the unfocus notice", m.statusMsg)
	}
	next, _ = m.Update(key("j"))
	m = asModel(next)
	if m.listCursor == 0 {
		t.Fatal("j must move the list after unfocus")
	}
}

// TestTerminalZoomToggles: 'z' zooms the embedded terminal fullscreen (the
// header/footer disappear) and focuses it, like 't' would — a zoomed frame
// with no way to type into it is a dead end. Restoring the split goes
// through shift+esc (see TestShiftEscInZoomExitsBothZoomAndFocus): a second
// 'z' can no longer reach the toggle once focused, since every key forwards
// to the agent's pty instead.
func TestTerminalZoomToggles(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]
	m.width, m.height = 120, 36
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(10, 5)}

	next, _ = m.Update(key("z"))
	m = asModel(next)
	if !m.termZoom {
		t.Fatal("z must zoom the terminal")
	}
	if !m.termFocus {
		t.Fatal("z must also focus the terminal, like 't' would")
	}
	if v := m.View().Content; strings.Contains(v, "BARON │") {
		t.Errorf("zoomed view must hide the header, got %q", v)
	}

	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.termZoom {
		t.Fatal("shift+esc must restore the split")
	}
	if m.termFocus {
		t.Fatal("shift+esc must also unfocus")
	}
}

// TestTerminalZoomShowsFullHeight: an earlier version of viewEmbeddedTerminal
// windowed every caller to m.detailPaneHeight() (the split-pane's cramped
// budget), which silently clipped the zoomed frame's bottom rows — exactly
// where an agent's own prompt/input box usually sits. Zoomed, the frame must
// render close to the pane's full height instead.
func TestTerminalZoomShowsFullHeight(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]
	m.width, m.height = 100, 36

	emu := vt.NewSafeEmulator(100, 36)
	for i := range 34 {
		_, _ = fmt.Fprintf(emu, "row%d\r\n", i)
	}
	_, _ = emu.WriteString("PROMPT_BOX_AT_BOTTOM")
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: emu}

	next, _ = m.Update(key("z"))
	m = asModel(next)
	if !m.termZoom {
		t.Fatal("z must zoom the terminal")
	}
	view := m.View().Content
	if !strings.Contains(view, "PROMPT_BOX_AT_BOTTOM") {
		t.Errorf("zoomed View() = %q, want the emulator's bottom row (e.g. an agent's own prompt box) visible, not clipped to the split pane's budget", view)
	}
}

// TestFocusDoesNotResizeEmulator: focus used to shrink the emulator by one
// row for a static "terminal focused" banner drawn above the frame. That
// banner is gone — a focus change now surfaces as a one-shot toast notice
// instead (see focusNoticeText) — so the emulator's budget must stay the
// same whether focused or not.
func TestFocusDoesNotResizeEmulator(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]
	m.width, m.height = 100, 36

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	emu := vt.NewSafeEmulator(100, 36)
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", pty: w, emu: emu}
	_, wantRows := m.termDims()

	next, _ = m.Update(key("t"))
	m = asModel(next)
	if !m.termFocus {
		t.Fatal("t must focus the terminal")
	}
	if got := emu.Height(); got != wantRows {
		t.Errorf("emulator height while focused = %d, want %d (no banner row to budget for anymore)", got, wantRows)
	}

	// Unfocus via shift+esc, not a second 't' — while focused, updateKey's
	// termFocus branch forwards every key (including 't') to the agent's
	// pty instead of reaching the dashboard's 't' handler.
	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.termFocus {
		t.Fatal("shift+esc must unfocus")
	}
	if got := emu.Height(); got != wantRows {
		t.Errorf("emulator height after unfocus = %d, want %d (unchanged)", got, wantRows)
	}
}

// TestShiftEscInZoomExitsBothZoomAndFocus: zoomed and focused is a dead end
// otherwise — the split (and its nav, including 'z' itself) is off screen,
// so shift+esc's whole point as an escape hatch back to BARON must clear
// zoom too, not leave the terminal filling the screen with no visible way
// out.
func TestShiftEscInZoomExitsBothZoomAndFocus(t *testing.T) {
	m := New(context.Background(), testDeps())
	beads := []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.detail = beads[0]
	m.width, m.height = 100, 36
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", emu: vt.NewSafeEmulator(100, 36)}

	next, _ = m.Update(key("z"))
	m = asModel(next)
	next, _ = m.Update(key("t"))
	m = asModel(next)
	if !m.termZoom || !m.termFocus {
		t.Fatalf("termZoom=%v termFocus=%v, want both true after z then t", m.termZoom, m.termFocus)
	}

	next, _ = m.Update(key("shift+esc"))
	m = asModel(next)
	if m.termFocus {
		t.Error("shift+esc must unfocus")
	}
	if m.termZoom {
		t.Error("shift+esc must also exit zoom — otherwise the split (and 'z' itself) stays unreachable")
	}
	if t2 := m.sessions["baron-a"]; t2.zoom || t2.focus {
		t.Errorf("session zoom=%v focus=%v, want both false", t2.zoom, t2.focus)
	}
}

// TestWaitForQuietReturnsOnceOutputSettles: waitForQuiet must not fire until
// lastActivity has been silent for the quiet window — a fixed delay alone
// raced a slow-drawing agent TUI and silently dropped the whole prompt (see
// promptQuietWindow/promptMaxWait).
func TestWaitForQuietReturnsOnceOutputSettles(t *testing.T) {
	term := &agentTerminal{}
	term.lastActivity.Store(time.Now().UnixNano())

	go func() {
		time.Sleep(120 * time.Millisecond)
		term.lastActivity.Store(time.Now().UnixNano()) // still active
	}()

	start := time.Now()
	term.waitForQuiet(0, 150*time.Millisecond, 5*time.Second)
	elapsed := time.Since(start)

	// Must not return before the second write's own quiet window elapses
	// (120ms + 150ms), but must also not run anywhere near maxWait.
	if elapsed < 250*time.Millisecond {
		t.Errorf("waitForQuiet returned after %v, want it to wait out the quiet window following the last write", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitForQuiet returned after %v, want it to return promptly once quiet, not ride out maxWait", elapsed)
	}
}

// TestWaitForQuietFallsBackToMaxWait: an agent that never goes quiet (e.g. a
// blinking cursor) must not hang typePrompt forever — waitForQuiet gives up
// at maxWait and lets the caller type anyway.
func TestWaitForQuietFallsBackToMaxWait(t *testing.T) {
	term := &agentTerminal{}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				term.lastActivity.Store(time.Now().UnixNano())
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	start := time.Now()
	term.waitForQuiet(0, 100*time.Millisecond, 300*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond || elapsed > 800*time.Millisecond {
		t.Errorf("waitForQuiet returned after %v, want it to fall back at ~maxWait (300ms) when output never quiets", elapsed)
	}
}

// TestTypePromptSkipsAfterRawPtyExit: an agent that exits before typePrompt
// runs must not have its prompt written into the leftover shell (the
// baron-bry bug — "zsh: parse error near ." when the prompt hit the shell).
// Raw-pty path: exitCh is closed by the pump goroutine when the child exits;
// typePrompt must detect this and return without writing anything.
//
// os.Pipe is used instead of pty.Open because PTY master file descriptors do
// not support SetReadDeadline on macOS — the syscall returns success but the
// read still blocks indefinitely. A pipe reader is deadline-aware and lets
// the assertion check for absence of output reliably.
func TestTypePromptSkipsAfterRawPtyExit(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	exitCh := make(chan struct{})
	close(exitCh) // simulate: agent already exited

	term := &agentTerminal{pty: writer, exitCh: exitCh, emu: vt.NewSafeEmulator(10, 5)}
	launch := domain.InteractiveLaunch{PromptKeys: "do the work", ReadyDelay: 0}

	// typePrompt must return without writing.
	done := make(chan struct{})
	go func() {
		term.typePrompt(launch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("typePrompt hung instead of returning early after exit")
	}

	// Nothing should have been written: read with a short deadline.
	if err := reader.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := reader.Read(buf)
	if n > 0 {
		t.Errorf("typePrompt wrote %q to pty after agent exit, want nothing", buf[:n])
	}
}

// TestTypePromptSkipsAfterDoneSentinel: tmux-backed path — when
// scanForDone has already set doneFound, typePrompt must not write the
// prompt into the pane (which is now hosting the wrapper's exec $SHELL).
func TestTypePromptSkipsAfterDoneSentinel(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	term := &agentTerminal{
		pty:        writer,
		exitCh:     make(chan struct{}), // NOT closed (tmux window still up)
		tmuxWindow: "baron-a",
		emu:        vt.NewSafeEmulator(10, 5),
	}
	// Simulate the pump having found DoneSentinel already.
	term.doneFound = true

	launch := domain.InteractiveLaunch{PromptKeys: "do the work", ReadyDelay: 0}
	done := make(chan struct{})
	go func() {
		term.typePrompt(launch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("typePrompt hung instead of returning early after DoneSentinel")
	}

	if err := reader.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := reader.Read(buf)
	if n > 0 {
		t.Errorf("typePrompt wrote %q after DoneSentinel, want nothing", buf[:n])
	}
}

// TestPersistSessionCmdWritesFinalLines: an exiting session's frame is
// persisted plain (ANSI stripped) — the no-session fallback in
// viewSplitDetailPane windows persisted lines with runewidth.Truncate
// directly, which miscounts escape bytes as printable cells.
func TestPersistSessionCmdWritesFinalLines(t *testing.T) {
	emu := vt.NewSafeEmulator(10, 3)
	_, _ = emu.WriteString("\x1b[31mhi\x1b[0m")
	term := &agentTerminal{brn: "baron-a", emu: emu}

	var gotBRN string
	var gotLines []string
	deps := Deps{WriteRunSummary: func(brn string, lines []string) error {
		gotBRN, gotLines = brn, lines
		return nil
	}}
	msg := persistSessionCmd(deps, "baron-a", term)()
	if msg != nil {
		t.Fatalf("persistSessionCmd() msg = %v, want nil", msg)
	}
	if gotBRN != "baron-a" {
		t.Fatalf("WriteRunSummary brn = %q, want baron-a", gotBRN)
	}
	if len(gotLines) == 0 || strings.Contains(strings.Join(gotLines, "\n"), "\x1b") {
		t.Fatalf("WriteRunSummary lines = %q, want ANSI stripped", gotLines)
	}
	if !strings.Contains(gotLines[0], "hi") {
		t.Fatalf("WriteRunSummary lines = %q, want the emulator's rendered text", gotLines)
	}
}

// TestPersistSessionCmdNilSafe: a nil WriteRunSummary or session must not
// panic — the exit handler always calls persistSessionCmd regardless of
// whether persistence is configured.
func TestPersistSessionCmdNilSafe(t *testing.T) {
	if msg := persistSessionCmd(Deps{}, "baron-a", &agentTerminal{emu: vt.NewSafeEmulator(10, 3)})(); msg != nil {
		t.Fatalf("persistSessionCmd() msg = %v, want nil", msg)
	}
	called := false
	deps := Deps{WriteRunSummary: func(string, []string) error { called = true; return nil }}
	if msg := persistSessionCmd(deps, "baron-a", nil)(); msg != nil {
		t.Fatalf("persistSessionCmd() msg = %v, want nil", msg)
	}
	if called {
		t.Fatal("WriteRunSummary must not be called for a nil session")
	}
}

// TestResizeSkipsRedundantSameSizeCall: a live bead ("baron-7xr") lost its
// initial prompt entirely — captured, opencode's input box sat empty with
// the prompt text stranded above it. Root cause: agentSpawnedMsg's
// resizeSessions() unconditionally re-applied the exact size the session
// was already spawned at. Against a raw pty that's an ignorable no-change
// SIGWINCH; against a tmux-attach pty it re-triggers tmux's attach-driven
// window resize a second time, landing inside typePrompt's critical
// window and corrupting the agent's input before the prompt ever reached
// it. resize() must now skip a same-size call outright.
func TestResizeSkipsRedundantSameSizeCall(t *testing.T) {
	pm, ps, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pm.Close() }()
	defer func() { _ = ps.Close() }()
	term := &agentTerminal{pty: pm, emu: vt.NewSafeEmulator(10, 5), cols: 10, rows: 5}

	term.resize(10, 5) // same size as construction — must no-op, not error
	if term.cols != 10 || term.rows != 5 {
		t.Fatalf("cols/rows = %d/%d, want unchanged 10/5 after a same-size resize", term.cols, term.rows)
	}

	term.resize(20, 8) // a real size change must still take effect
	if term.cols != 20 || term.rows != 8 {
		t.Fatalf("cols/rows = %d/%d, want 20/8 after a real size change", term.cols, term.rows)
	}
	got, err := pty.GetsizeFull(pm)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if got.Cols != 20 || got.Rows != 8 {
		t.Fatalf("pty size = %dx%d, want 20x8", got.Cols, got.Rows)
	}
}

// fakeAgentHost is a controllable AgentHost for tests — no real tmux or
// subprocess involved.
type fakeAgentHost struct {
	mu            sync.Mutex
	aliveWindows  map[string]bool
	aliveChecked  []string
	killed        []string
	detached      []string
	ensureCalls   []string
	ensureStarted bool
	ensureErr     error
	attachArgv    []string
	attachErr     error
	captured      string
	captureErr    error
	captureCalls  []string
}
