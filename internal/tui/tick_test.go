package tui

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// countTicks runs cmd (recursing into tea.Batch/tea.Sequence fan-out) and
// reports how many independent frame-tick chains it arms. A tea.Tick's cmd is
// not executed — running it would just sleep — so it's identified structurally
// instead: anything that isn't a batch and isn't one of the store-loading cmds
// is inspected by running it only when safe. See tickCmdCount.
func countTicks(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	return tickCmdCount(t, cmd, 0)
}

// tickCmdCount walks a cmd tree counting frame ticks. Batches fan out; a
// tea.Tick is recognised by its message being a tickMsg once run. Running a
// tea.Tick cmd blocks for termFrameInterval, so each is run in its own
// goroutine with the result collected — at 42ms and a handful of ticks this
// stays well inside a test's budget.
func tickCmdCount(t *testing.T, cmd tea.Cmd, depth int) int {
	t.Helper()
	if cmd == nil || depth > 4 {
		return 0
	}
	msg := cmd()
	switch v := msg.(type) {
	case tea.BatchMsg:
		n := 0
		for _, c := range v {
			n += tickCmdCount(t, c, depth+1)
		}
		return n
	case tickMsg:
		return 1
	}
	return 0
}

// tickTestModel is a Model with one live session, sized, on the Terminal tab —
// the state in which the frame tick is running.
func tickTestModel(t *testing.T) Model {
	t.Helper()
	m := New(context.Background(), testDeps())
	beads := []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen},
		{BRN: "baron-c", Title: "C", Status: store.BeadStatusOpen},
	}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	m.width, m.height = 120, 36
	m.detailTab = 1
	m.detail = beads[0]
	m.liveBRN = "baron-a"
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(m.termDims())}
	return m
}

// TestFrameTickStaysSingleChainAcrossNavigation is the regression test for the
// TUI freeze the user hit ("beadsler arası gezinemiyorum bile" — I can't even
// navigate between beads): every call to termTick() used to arm its own
// self-sustaining tea.Tick chain, and afterSelect() calls termTick() on every
// single bead navigation. Since the tickMsg handler re-arms one tick per
// tickMsg it receives, each extra chain lived forever — so N keypresses left N
// concurrent chains all firing every 42ms, each running a full checkTmuxDone
// (a whole-screen render per live session) plus a full View(). CPU grew without
// bound the more the user navigated, which is exactly why navigating was the
// thing that broke.
//
// The invariant: no matter how much navigation happens, exactly one frame-tick
// chain exists.
func TestFrameTickStaysSingleChainAcrossNavigation(t *testing.T) {
	m := tickTestModel(t)

	// Arm the initial chain the way a spawn does.
	if got := countTicks(t, m.termTick()); got != 1 {
		t.Fatalf("initial termTick armed %d chains, want exactly 1", got)
	}

	// Navigate: each of these calls afterSelect(), which calls termTick().
	// None of them may arm a second chain.
	for i := range 20 {
		next, cmd := m.Update(key("j"))
		m = asModel(next)
		if got := countTicks(t, cmd); got != 0 {
			t.Fatalf("navigation #%d armed %d extra tick chains, want 0 — a second chain doubles the frame rate permanently", i+1, got)
		}
	}

	// Tab switching goes through tabCmd(), which also calls termTick().
	for i := range 10 {
		next, cmd := m.Update(key("tab"))
		m = asModel(next)
		if got := countTicks(t, cmd); got != 0 {
			t.Fatalf("tab switch #%d armed %d extra tick chains, want 0", i+1, got)
		}
	}
}

// TestFrameTickReArmsExactlyOncePerTick: the running chain must sustain itself
// at a constant rate — one tickMsg in, exactly one tick out. More than one is
// the doubling bug; zero stops the frame updates entirely.
func TestFrameTickReArmsExactlyOncePerTick(t *testing.T) {
	m := tickTestModel(t)
	m.tickScheduled = true // the chain is running

	_, cmd := m.Update(tickMsg{})
	if got := countTicks(t, cmd); got != 1 {
		t.Fatalf("tickMsg re-armed %d chains, want exactly 1", got)
	}
}

// TestFrameTickStopsWhenNoSessionsLive: with nothing live there is nothing to
// animate, so the chain must end rather than spin forever.
func TestFrameTickStopsWhenNoSessionsLive(t *testing.T) {
	m := tickTestModel(t)
	m.tickScheduled = true
	delete(m.sessions, "baron-a")

	next, cmd := m.Update(tickMsg{})
	m = asModel(next)
	if got := countTicks(t, cmd); got != 0 {
		t.Fatalf("tickMsg with no live session armed %d chains, want 0", got)
	}
	if m.tickScheduled {
		t.Error("tickScheduled must clear once the chain stops, so a later spawn can start a fresh one")
	}
}

// TestFrameTickRestartsAfterStopping: once the chain has stopped (no live
// sessions), spawning again must be able to start a new one — the guard must
// not latch permanently.
func TestFrameTickRestartsAfterStopping(t *testing.T) {
	m := tickTestModel(t)
	m.tickScheduled = true
	delete(m.sessions, "baron-a")
	next, _ := m.Update(tickMsg{})
	m = asModel(next)

	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, emu: vt.NewSafeEmulator(m.termDims())}
	if got := countTicks(t, m.termTick()); got != 1 {
		t.Fatalf("termTick after a stopped chain armed %d chains, want 1", got)
	}
}

// TestQuitDetachesEveryViewer is the regression test for the other half of
// the freeze report — the tmux viewer leak. Quitting used to go straight to
// tea.Quit, so every viewer session BARON had created (one per attached bead,
// named with this process's pid) survived the process that owned it. They are
// invisible and individually harmless, so they piled up unnoticed: 280 of them
// on the reporting user's machine, each grouped with the shared agent session.
// Quit must detach all of them, and must not kill the agents themselves.
func TestQuitDetachesEveryViewer(t *testing.T) {
	host := &fakeAgentHost{aliveWindows: map[string]bool{}}
	deps := testDeps()
	deps.AgentHost = host
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen},
		{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen},
	}})
	m = asModel(next)
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, tmuxWindow: "baron-a", emu: vt.NewSafeEmulator(10, 5)}
	m.sessions["baron-b"] = &agentTerminal{brn: "baron-b", kind: kindAgent, tmuxWindow: "baron-b", emu: vt.NewSafeEmulator(10, 5)}
	// A bare-pty diff session has no viewer to detach.
	m.diffSessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindDiff, emu: vt.NewSafeEmulator(10, 5)}

	cmd := m.detachAllCmd()
	if cmd == nil {
		t.Fatal("detachAllCmd returned nil with two tmux-backed sessions live")
	}
	cmd()

	host.mu.Lock()
	detached, killed := append([]string(nil), host.detached...), append([]string(nil), host.killed...)
	host.mu.Unlock()
	slices.Sort(detached)
	if !slices.Equal(detached, []string{"baron-a", "baron-b"}) {
		t.Errorf("detached %v, want both tmux-backed windows and nothing else", detached)
	}
	if len(killed) != 0 {
		t.Errorf("quit killed %v — quitting BARON must never stop a running agent", killed)
	}
}

// TestQuitCmdIsSequencedBeforeExit: the detach must complete before the
// program ends. Batched alongside tea.Quit it simply loses the race, which is
// how the leak survived unnoticed.
func TestQuitCmdIsSequencedBeforeExit(t *testing.T) {
	host := &fakeAgentHost{aliveWindows: map[string]bool{}}
	deps := testDeps()
	deps.AgentHost = host
	m := New(context.Background(), deps)
	m.sessions["baron-a"] = &agentTerminal{brn: "baron-a", kind: kindAgent, tmuxWindow: "baron-a", emu: vt.NewSafeEmulator(10, 5)}

	// tea's sequenceMsg is unexported, so the assertion is by elimination:
	// with cleanup pending, quitCmd is neither a bare quit (which would skip
	// the detach entirely) nor a batch (which would race it).
	msg := m.quitCmd()()
	if _, isQuit := msg.(tea.QuitMsg); isQuit {
		t.Fatal("quitCmd quit immediately — the viewer detach would never run")
	}
	if _, isBatch := msg.(tea.BatchMsg); isBatch {
		t.Fatal("quitCmd batched the detach against the quit — it loses that race; sequence it")
	}

	// With no host wired (no tmux) there is nothing to clean up, so quit
	// stays a plain immediate quit.
	plain := New(context.Background(), testDeps())
	if _, ok := plain.quitCmd()().(tea.QuitMsg); !ok {
		t.Errorf("quitCmd without an AgentHost must be a bare quit, got %T", plain.quitCmd()())
	}
}

// TestDoneSentinelDetectedWithoutPerFrameRender: checkTmuxDone used to render
// every live session's whole screen (~213µs each) on every 42ms frame just to
// grep for the sentinel. The pump already sees every byte, so detection moved
// there — this asserts the detection still works, now driven by what was
// written rather than by a per-frame render.
func TestDoneSentinelDetectedWithoutPerFrameRender(t *testing.T) {
	m := tickTestModel(t)
	m.tickScheduled = true
	tm := m.sessions["baron-a"]
	tm.tmuxWindow = "baron-a"

	if cmds := m.checkTmuxDone(); len(cmds) != 0 {
		t.Fatalf("checkTmuxDone fired %d exits before the sentinel appeared", len(cmds))
	}

	// The pump's own scan is what notices it.
	tm.scanForDone([]byte(domain.DoneSentinel + " exit=0\r\n"))

	cmds := m.checkTmuxDone()
	if len(cmds) != 1 {
		t.Fatalf("checkTmuxDone returned %d exit cmds after the sentinel, want 1", len(cmds))
	}
	if _, ok := cmds[0]().(agentExitMsg); !ok {
		t.Fatalf("want an agentExitMsg, got %T", cmds[0]())
	}
	if cmds := m.checkTmuxDone(); len(cmds) != 0 {
		t.Fatalf("checkTmuxDone re-fired %d exits for an already-done session", len(cmds))
	}
}

// TestRapidTabSwitchingSpawnsOneSessionOnly is the regression test for a
// duplicate-process leak found by inspecting a real machine after an e2e run:
// three concurrent hunk processes for a single bead, each with its own daemon
// session.
//
// Spawning is asynchronous — a session only lands in the registry when its
// agentSpawnedMsg arrives — so embeddedSpawnCmd's "already running?" check is
// blind for the whole startup window. The Diff tab auto-starts hunk when it
// opens, so switching tabs faster than a process starts (holding tab does it)
// launched a new one every time, and none of them were ever reachable to be
// cleaned up.
func TestRapidTabSwitchingSpawnsOneSessionOnly(t *testing.T) {
	m := tickTestModel(t)
	m.detail = store.Bead{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}

	spawns := 0
	for range 25 {
		cmd := m.embeddedSpawnCmd("baron-a", kindDiff)
		if cmd == nil {
			continue
		}
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, c := range batch {
				if c == nil {
					continue
				}
				if _, ok := c().(agentSpawnedMsg); ok {
					spawns++
				}
			}
		}
	}
	if spawns > 1 {
		t.Errorf("25 rapid Diff-tab opens started %d processes, want at most 1 — each extra one is an orphaned hunk session", spawns)
	}
}

// TestFailedSpawnDoesNotLatchTheGuard: the in-flight guard has to clear on
// failure too, or one failed start disables the Diff tab for the rest of the
// process with no way to retry.
func TestFailedSpawnDoesNotLatchTheGuard(t *testing.T) {
	m := tickTestModel(t)
	m.spawning[spawnKey("baron-a", kindDiff)] = true

	next, _ := m.Update(agentSpawnedMsg{brn: "baron-a", kind: kindDiff, err: errors.New("hunk not found")})
	m = asModel(next)

	if m.spawning[spawnKey("baron-a", kindDiff)] {
		t.Error("a failed spawn left the guard set — the Diff tab can never start again")
	}
}

// spawnAttempts runs cmd, recursing through nested tea.Batch fan-out, and
// counts the spawn results it produces. embeddedSpawnCmd returns a batch of
// its own inside afterSelect's batch, so a single-level walk sees nothing.
func spawnAttempts(cmd tea.Cmd, depth int) int {
	if cmd == nil || depth > 4 {
		return 0
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		n := 0
		for _, c := range v {
			n += spawnAttempts(c, depth+1)
		}
		return n
	case agentSpawnedMsg:
		return 1
	}
	return 0
}

// TestDiffTabFollowsTheSelectedBead: moving to another bead kills the previous
// bead's Diff session, so with the Diff tab on screen the pane would sit empty
// until the user switched tabs away and back. hunk should be running for
// exactly the bead whose diff is being looked at.
func TestDiffTabFollowsTheSelectedBead(t *testing.T) {
	m := tickTestModel(t)
	m.detailTab = 2
	m.detail = store.Bead{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen}
	m.liveBRN = "baron-b"

	if spawnAttempts(m.afterSelect(), 0) == 0 {
		t.Error("selecting a bead with the Diff tab open started no diff session — the pane stays empty")
	}
}

// TestDiffTabDoesNotSpawnOnOtherTabs: the Diff session auto-starts because the
// Diff tab is what the user is looking at. Navigating with any other tab open
// must not launch hunk processes in the background for every bead touched —
// that is the accumulation this kind was rewritten to avoid.
func TestDiffTabDoesNotSpawnOnOtherTabs(t *testing.T) {
	for _, tab := range []int{0, 1, 3} {
		m := tickTestModel(t)
		m.detailTab = tab
		m.detail = store.Bead{BRN: "baron-b", Title: "B", Status: store.BeadStatusOpen}
		m.liveBRN = "baron-b"

		if n := spawnAttempts(m.afterSelect(), 0); n != 0 {
			t.Errorf("tab %d spawned %d session(s) just from navigating", tab, n)
		}
	}
}
