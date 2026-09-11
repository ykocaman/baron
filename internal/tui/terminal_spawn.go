package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/baron-cli/baron/internal/domain"
	"github.com/baron-cli/baron/internal/store"
)

// findBead returns the bead identified by brn out of beads, if present.
func findBead(beads []store.Bead, brn string) (store.Bead, bool) {
	for _, b := range beads {
		if string(b.BRN) == brn {
			return b, true
		}
	}
	return store.Bead{}, false
}

// clampTermSize applies spawnAgentTerminal/spawnDiffTerminal's pty size
// defaults for a zero/negative dimension (e.g. before the TUI's first
// WindowSizeMsg has arrived).
func clampTermSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return cols, rows
}

func reconcileWorkingCmd(ctx context.Context, host AgentHost, brn string) tea.Cmd {
	return func() tea.Msg {
		alive, err := host.Alive(ctx, domain.TmuxWindowName(brn))
		return reconcileWorkingMsg{brn: brn, alive: alive, err: err}
	}
}

// agentPrompt picks what a freshly spawned agent opens on: the bead's standard
// work brief, or — when a follow-up was supplied — that text instead.
//
// The substitution rather than an append is deliberate. A follow-up only
// happens when a comment arrives for an agent that has already stopped (see
// deliverCommentCmd), which means it already read the brief and acted on it.
// Handing it the brief again invites it to redo finished work; the review note
// is the whole of what is new — including .baron/prompt.md's project
// context, which the agent already received in its original brief this
// session and doesn't need repeated.
//
// projectDir is always the repo root (deps.Dir), never the bead's worktree
// — .baron/prompt.md lives at the project root regardless of which
// worktree an agent happens to be running in.
func agentPrompt(bead store.Bead, projectDir, followUp string) string {
	if followUp != "" {
		return followUp
	}
	return domain.ProjectContext(projectDir) + domain.Prompt(bead.Title, bead.Description, bead.AcceptanceCriteria)
}

// spawnAgentTerminal starts brn's assigned agent in a pty: the bead's
// worktree is created (or reused), the interactive command runs with the
// worktree as its cwd, and a goroutine pumps the pty's output into the
// emulator until the child exits. deps.Worktrees nil falls back to the main
// checkout (tests).
//
// followUp, when non-empty, replaces the bead's standard opening prompt with
// that text. This is how a comment reaches an agent that has already stopped:
// rather than the work brief it has plainly already read, the freshly started
// agent opens on the note the reviewer just left. Empty (the normal case)
// means the standard prompt. See deliverCommentCmd.
func spawnAgentTerminal(ctx context.Context, deps Deps, beads []store.Bead, brn string, cols, rows int, followUp string) tea.Cmd {
	return func() tea.Msg { return spawnAgentTerminalMsg(ctx, deps, beads, brn, cols, rows, followUp) }
}

func spawnAgentTerminalMsg(ctx context.Context, deps Deps, beads []store.Bead, brn string, cols, rows int, followUp string) tea.Msg {
	cols, rows = clampTermSize(cols, rows)
	// beads is the Model's own, already-loaded list (m.beads), not a
	// fresh `bd list` — see spawnDiffTerminal's doc comment for why
	// that subprocess is too slow to block a spawn on.
	bead, ok := findBead(beads, brn)
	if !ok {
		return agentSpawnedMsg{brn: brn, kind: kindAgent, err: errors.New(brn + " is gone")}
	}
	if bead.Assignee == "" {
		return agentSpawnedMsg{brn: brn, kind: kindAgent, err: domain.ErrNoAgent}
	}
	dir := deps.Dir
	if deps.Worktrees != nil {
		wt, err := deps.Worktrees.Create(ctx, brn, bead.IssueType, mergeBaseFor(bead, beads))
		if err != nil {
			return agentSpawnedMsg{brn: brn, kind: kindAgent, err: fmt.Errorf("worktree for %s: %w", brn, err)}
		}
		dir = wt.Path
	}
	launch, err := domain.InteractiveCommand(bead.Assignee, bead.Model(), bead.Effort(),
		agentPrompt(bead, deps.Dir, followUp))
	if err != nil {
		return agentSpawnedMsg{brn: brn, kind: kindAgent, err: err}
	}

	cmd, window, started, err := buildSpawnCmd(ctx, deps, brn, launch, dir)
	if err != nil {
		return agentSpawnedMsg{brn: brn, kind: kindAgent, err: err}
	}
	f, emu, err := startPtyEmulator(cmd, cols, rows)
	if err != nil {
		return agentSpawnedMsg{brn: brn, kind: kindAgent, err: err}
	}
	// cols/rows seeded to what pty.StartWithSize just set, so the
	// agentSpawnedMsg handler's resizeSessions() call — which runs
	// moments later at the same size — no-ops instead of re-triggering
	// a disruptive second resize (see resize's doc comment).
	t := &agentTerminal{brn: brn, kind: kindAgent, cmd: cmd, pty: f, emu: emu, exitCh: make(chan struct{}), tmuxWindow: window, cols: cols, rows: rows, startedAt: time.Now()}
	go t.pump()
	go t.pumpReplies()
	// A reattach to an agent already running from an earlier process
	// must not re-type the prompt into its now-established session.
	if started && launch.PromptKeys != "" {
		go t.typePrompt(launch)
	}
	// The agent is demonstrably working now; best-effort like the old
	// cockpit — a status write failure must not fail the spawn.
	_ = deps.Beads.Status(ctx, brn, store.BeadStatusWorking)
	return agentSpawnedMsg{brn: brn, kind: kindAgent, t: t}
}

// buildSpawnCmd resolves the *exec.Cmd to actually pty-attach to for this
// spawn: the tmux-attach command when deps.AgentHost is set (window becomes
// the tmux window name, ensured running first), or a bare direct exec of
// launch.Argv in dir otherwise (tests, and any host without tmux wired up).
// started reports whether EnsureRunning actually launched a fresh process
// (false on reattach to one already running); always true for the bare-exec
// path, since there's nothing to reattach to there.
func buildSpawnCmd(ctx context.Context, deps Deps, brn string, launch domain.InteractiveLaunch, dir string) (cmd *exec.Cmd, window string, started bool, err error) {
	if deps.AgentHost == nil {
		cmd = exec.CommandContext(ctx, launch.Argv[0], launch.Argv[1:]...)
		cmd.Dir = dir
		return cmd, "", true, nil
	}
	window = domain.TmuxWindowName(brn)
	started, err = deps.AgentHost.EnsureRunning(ctx, window, launch.Argv, dir)
	if err != nil {
		return nil, "", false, err
	}
	argv, err := deps.AgentHost.AttachArgv(ctx, window)
	if err != nil {
		return nil, "", false, err
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...), window, started, nil
}

// startPtyEmulator starts cmd in a pty sized cols x rows and wraps its
// output in a vt emulator — the OS-level plumbing shared by every embedded
// terminal kind (agent, diff, ...); callers build the rest of *agentTerminal
// (kind, exitCh, pump goroutines) on top.
func startPtyEmulator(cmd *exec.Cmd, cols, rows int) (*os.File, *vt.SafeEmulator, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: clampUint16(rows), Cols: clampUint16(cols)})
	if err != nil {
		return nil, nil, err
	}
	emu := vt.NewSafeEmulator(cols, rows)
	emu.SetScrollbackSize(5000)
	return f, emu, nil
}

// diffArgvFor builds the review command each Diff-tab session runs for a
// bead that hasn't merged yet: hunk's pager-style live diff view, watching
// the worktree so it stays current as the bead's agent keeps editing.
// target is the branch the bead will merge into (mergeBaseFor's parent
// branch, or the project's own base branch — see spawnDiffTerminal), always
// passed as an explicit diff target rather than left implicit: `hunk diff
// <target>` (== `git diff <target>`) shows the worktree's FULL accumulated
// change against target — any commits the branch already carries plus
// whatever's still uncommitted — where a bare `hunk diff` with no target
// shows only uncommitted changes. Those are the same thing while an agent
// is still mid-task with nothing committed yet, but they diverge hard the
// moment anything on the branch is committed: an agent committing its own
// work as it goes (docs/PRD/agent-contract.md §7), or BARON's own
// gate-triggered auto-commit once the gate passes (commitWork, right before
// a bead reaches mergable) — either one leaves a clean working tree with
// real, real committed change sitting on the branch, which a targetless
// diff cannot see at all. Caught live: a mergable bead (fully committed,
// nothing left uncommitted) showed a totally empty Diff tab despite `git
// diff main` in that same worktree reporting real changes.
// Empty target falls back to the old targetless (working-tree-only)
// behavior — only reachable if the project's own base branch is itself
// unset, which DefaultConfig never leaves the case in practice.
// --mode stack keeps it usable in the split-pane's narrower budget
// (side-by-side needs more columns than the detail pane usually has).
// ponytail: exclude .omo noise so real files (deneme.txt) surface on first page; watch still sees unstaged
func diffArgvFor(target string) []string {
	argv := []string{"hunk", "diff", "--pager", "--no-hunk-headers", "--watch", "--mode", "stack"}
	if target != "" {
		argv = append(argv, target)
	}
	return append(argv, "--", ":!.omo")
}

// mergedDiffArgv is what the Diff tab runs instead, once a bead is merged
// (or closed after having gone through one): the branch's own live diff
// against base has gone empty by then (base now contains it too, so `git
// diff base...branch` is nothing left to show), and the bead's worktree is
// usually already removed (runMerge/tryAutoMerge both remove it right after
// merging) — so there is no live thing left to watch.
//
// `hunk diff <before-sha>` (not `hunk show <merge-sha>` — tried first, and
// wrong: a plain `git show`/`git diff-tree` on a clean, non-conflicting
// --no-ff merge commit is EMPTY by git's own default combined-diff
// semantics, reproduced live as hunk's "No files match the current
// filter") diffs the main checkout's current working tree — sitting at the
// merge commit, clean, right after a merge — against the base's own tip
// *before* that merge ran (domain.MergeBaseSHA, embedded in the "merge"/
// "merge_auto" audit event at merge time — see domain.MergeBaseDetail's own
// doc comment), which correctly reproduces exactly what the branch
// introduced. Run in the main checkout rather than any worktree, which
// always has the full history regardless of the worktree's own fate.
var mergedDiffArgv = []string{"hunk", "diff", "--pager", "--no-hunk-headers", "--mode", "stack", "--", ":!.omo"}

// diffAgainstRecordedMerge reports whether status means the Diff tab should
// use mergedDiffArgv (a recorded pre-merge SHA in the main checkout)
// instead of a live worktree diff against the merge-base branch — true only
// for merged and closed-after-merge; every other status (including
// mergable, human_queue and retry reached after a bead's own work is
// already committed) still has its own worktree and branch to diff
// live — see diffArgvFor's own doc comment for why that stays correct even
// once nothing is left uncommitted.
func diffAgainstRecordedMerge(status store.BeadStatus) bool {
	return status == store.BeadStatusMerged || status == store.BeadStatusClosed
}

// latestMergeBaseSHA finds brn's newest "merge" or "merge_auto" audit event
// and extracts the pre-merge base SHA domain.MergeBaseDetail embedded in it
// — ok is false for a bead that was never merged, or one merged before that
// embedding existed.
func latestMergeBaseSHA(audit *store.AuditStore, brn string) (sha string, ok bool) {
	events, err := audit.Query(brn)
	if err != nil {
		return "", false
	}
	for _, ev := range slices.Backward(events) { // events is oldest-first; newest wins

		if ev.Action != "merge" && ev.Action != "merge_auto" {
			continue
		}
		return domain.MergeBaseSHA(ev.Detail)
	}
	return "", false
}

// spawnDiffTerminal starts brn's Diff-tab session: hunk diff --watch in the
// same worktree the bead's agent uses (Worktrees.Create is idempotent —
// see spawnAgentTerminal — so this never creates a second checkout) for a
// bead still in flight, or — once merged (or closed after a merge) — hunk
// show on the recorded merge commit in the main checkout instead (see
// mergedDiffArgv's own doc comment): the live diff has nothing left to show
// by then and the worktree is usually already gone. Deliberately NOT
// tmux-backed, unlike the agent kind: the Diff tab opens
// itself the instant the tab is selected (see tabCmd) with zero deliberate
// action from the user, so tmux-persisting it compounds fast — one
// forgotten window per bead ever glanced at, each a live process the
// ~24fps render tick (checkTmuxDone/resizeSessions/termTick) re-scans
// forever. Observed live: browsing many beads left 230+ tmux sessions and
// 20+ live hunk processes running, pegging the CPU. A bare pty avoids this
// entirely — killSession's kill() (or the bead-switch cleanup in
// selectListRow) just closes the local pty, and there's nothing left
// running afterwards; hunk itself is cheap enough to relaunch on demand
// that there's no real persistence value being given up here the way there
// would be for a long-running agent process mid-task.
func spawnDiffTerminal(ctx context.Context, deps Deps, beads []store.Bead, brn string, cols, rows int) tea.Cmd {
	return func() tea.Msg { return spawnDiffTerminalMsg(ctx, deps, beads, brn, cols, rows) }
}

func spawnDiffTerminalMsg(ctx context.Context, deps Deps, beads []store.Bead, brn string, cols, rows int) tea.Msg {
	cols, rows = clampTermSize(cols, rows)
	// The branch must fork by the bead's real type (a feature bead must
	// not get a task/ branch) and, for a hierarchical child, from its
	// parent's branch, so the bead is looked up here rather than
	// defaulting — and its status decides which of the two diff modes
	// below applies. beads is the Model's own, already-loaded list
	// (m.beads), not a fresh `bd list` — that subprocess alone can take
	// seconds on a large/slow bd store, and blocking the Diff tab's
	// spawn on it (instead of the render loop's existing periodic
	// reload) was the actual cause of a visibly late-opening Diff tab.
	bead, _ := findBead(beads, brn)

	dir := deps.Dir
	var argv []string
	merged := diffAgainstRecordedMerge(bead.Status)
	if merged && deps.Audit != nil {
		if sha, ok := latestMergeBaseSHA(deps.Audit, brn); ok {
			argv = append(append([]string{}, mergedDiffArgv...), sha)
		} else {
			merged = false // no recorded merge commit (old event, pre-dates this) — fall back below
		}
	}
	if !merged {
		argv = diffArgvFor(mergeBaseBranch(bead, beads, deps.BaseBranch))
		if deps.Worktrees != nil {
			wt, err := deps.Worktrees.Create(ctx, brn, bead.IssueType, mergeBaseFor(bead, beads))
			if err != nil {
				return agentSpawnedMsg{brn: brn, kind: kindDiff, err: fmt.Errorf("worktree for %s: %w", brn, err)}
			}
			dir = wt.Path
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	f, emu, err := startPtyEmulator(cmd, cols, rows)
	if err != nil {
		return agentSpawnedMsg{brn: brn, kind: kindDiff, err: err}
	}
	t := &agentTerminal{brn: brn, kind: kindDiff, cmd: cmd, pty: f, emu: emu, exitCh: make(chan struct{}), cols: cols, rows: rows, startedAt: time.Now()}
	go t.pump()
	go t.pumpReplies()
	return agentSpawnedMsg{brn: brn, kind: kindDiff, t: t}
}

// pump copies the child's pty output into the emulator until the child
// exits, then releases the exit waiter. The child's own screen remains in
// the emulator afterwards. Each chunk stamps lastActivity for waitForQuiet.
