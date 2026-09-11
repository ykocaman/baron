package domain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// scriptedRunner dispatches on the git subcommand (args[0]) so a single fake
// can answer the list/rev-parse/add/remove sequence a worktree operation
// makes, unlike the fixed-order fakeRunner in gate_test.go.
type scriptedRunner struct {
	listOutput   string
	branchExists bool
	calls        [][]string
}

func (s *scriptedRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	s.calls = append(s.calls, append([]string{name}, args...))
	if len(args) == 0 {
		return tool.Result{}, nil
	}
	switch args[0] {
	case "worktree":
		if len(args) > 1 && args[1] == "list" {
			return tool.Result{Stdout: s.listOutput}, nil
		}
		return tool.Result{}, nil
	case "rev-parse":
		if !s.branchExists {
			return tool.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return tool.Result{}, nil
	default:
		return tool.Result{}, nil
	}
}

func TestWorktreeManagerCreateNew(t *testing.T) {
	runner := &scriptedRunner{}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	wt, err := m.Create(context.Background(), "BRN-baron-0001", "task", "")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if wt.Path != "/repo/.baron/worktrees/BRN-baron-0001" {
		t.Errorf("Path = %q, want worktrees dir + BRN", wt.Path)
	}
	if wt.Branch != "task/0001" {
		t.Errorf("Branch = %q, want task/0001", wt.Branch)
	}

	var addArgs []string
	for _, c := range runner.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" {
			addArgs = c
		}
	}
	if addArgs == nil {
		t.Fatalf("expected a `git worktree add` call, got calls: %v", runner.calls)
	}
	if !slices.Contains(addArgs, "-b") {
		t.Errorf("worktree add args %v missing -b (new branch)", addArgs)
	}
}

func TestWorktreeManagerCreateReusesExistingBranch(t *testing.T) {
	runner := &scriptedRunner{branchExists: true}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	if _, err := m.Create(context.Background(), "BRN-baron-0002", "task", ""); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	for _, c := range runner.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" && slices.Contains(c, "-b") {
			t.Errorf("worktree add args %v should not create a new branch when one exists", c)
		}
	}
}

func TestWorktreeManagerCreateIdempotent(t *testing.T) {
	runner := &scriptedRunner{
		listOutput: "worktree /repo/.baron/worktrees/BRN-baron-0003\n" +
			"HEAD abc123\n" +
			"branch refs/heads/task/0003\n\n",
	}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	wt, err := m.Create(context.Background(), "BRN-baron-0003", "task", "")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if wt.Branch != "task/0003" {
		t.Errorf("Branch = %q, want reused branch from list", wt.Branch)
	}
	for _, c := range runner.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" {
			t.Errorf("Create() should not call `git worktree add` when a worktree already exists, got %v", c)
		}
	}
}

// TestWorktreeManagerCreateCaseInsensitiveReuse: on case-insensitive
// filesystems (macOS, Windows) the listed worktree path may differ from
// rootDir's path only by case; Create must still reuse it instead of failing
// with "already exists".
func TestWorktreeManagerCreateCaseInsensitiveReuse(t *testing.T) {
	runner := &scriptedRunner{
		listOutput: "worktree /REPO/.baron/worktrees/BRN-baron-0003\n" +
			"HEAD abc123\n" +
			"branch refs/heads/task/0003\n\n",
	}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	wt, err := m.Create(context.Background(), "BRN-baron-0003", "task", "")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if wt.Branch != "task/0003" {
		t.Errorf("Branch = %q, want reused branch from list", wt.Branch)
	}
	for _, c := range runner.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "add" {
			t.Errorf("Create() should not call `git worktree add` for a case-variant path, got %v", c)
		}
	}
}

func TestWorktreeManagerList(t *testing.T) {
	runner := &scriptedRunner{
		listOutput: "worktree /repo\n" +
			"HEAD abc123\n" +
			"branch refs/heads/main\n\n" +
			"worktree /repo/.baron/worktrees/BRN-baron-0004\n" +
			"HEAD def456\n" +
			"branch refs/heads/task/0004\n\n",
	}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() returned %d worktrees, want 1 (main repo excluded)", len(got))
	}
	if got[0].BRN != "BRN-baron-0004" {
		t.Errorf("BRN = %q, want BRN-baron-0004", got[0].BRN)
	}
}

func TestWorktreeManagerRemove(t *testing.T) {
	runner := &scriptedRunner{}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")

	if err := m.Remove(context.Background(), "BRN-baron-0005"); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	found := false
	for _, c := range runner.calls {
		if len(c) > 2 && c[1] == "worktree" && c[2] == "remove" {
			found = true
			if !slices.Contains(c, "--force") {
				t.Errorf("remove args %v missing --force", c)
			}
			if !slices.Contains(c, "/repo/.baron/worktrees/BRN-baron-0005") {
				t.Errorf("remove args %v missing worktree path", c)
			}
		}
	}
	if !found {
		t.Fatalf("expected a `git worktree remove` call, got calls: %v", runner.calls)
	}
}

func TestBranchName(t *testing.T) {
	cases := []struct{ issueType, id, want string }{
		{"task", "BRN-baron-0001", "task/0001"},
		{"feature", "BRN-baron-0001", "feature/0001"},
		{"unknown", "BRN-baron-0001", "task/0001"}, // unknown types fall back to task
		{"bug", "dxs", "bug/dxs"},                  // no dash: id used as-is
		{"epic", "task/dxs", "epic/dxs"},           // already-prefixed id is idempotent
		{"chore", "baron/BRN-x.2", "chore/x.2"},    // old-scheme id re-derives correctly
		{"feature", "feature/BRN-y", "feature/y"},  // same-type prefix idempotent
	}
	for _, c := range cases {
		if got := BranchName(c.issueType, c.id); got != c.want {
			t.Errorf("BranchName(%q, %q) = %q, want %q", c.issueType, c.id, got, c.want)
		}
	}
}

func TestParseWorktreeListSkipsMainRepo(t *testing.T) {
	output := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n"
	got := parseWorktreeList(output, "/repo/.baron/worktrees")
	if len(got) != 0 {
		t.Errorf("parseWorktreeList() = %v, want empty (main repo not under rootDir)", got)
	}
}

func TestWorktreeManagerRemoveNotAWorkingTreeIsNotError(t *testing.T) {
	runner := &fakeErrRunner{msg: "fatal: '/repo/.baron/worktrees/BRN-baron-0006' is not a working tree"}
	m := NewWorktreeManager(runner, "/repo", "/repo/.baron/worktrees")
	if err := m.Remove(context.Background(), "BRN-baron-0006"); err != nil {
		t.Fatalf("Remove() error = %v, want nil for a worktree that was never created", err)
	}
}

type fakeErrRunner struct{ msg string }

func (f *fakeErrRunner) Run(context.Context, string, []string, tool.Options) (tool.Result, error) {
	return tool.Result{ExitCode: 1, Stderr: f.msg}, errors.New(f.msg)
}

// TestWorktreeManagerCreateIdempotentAcrossSymlinkedRoot is a real-git
// integration test (every other test in this file fakes the runner) —
// this specific bug can only be caught against real git, since it's git
// itself that resolves symlinks when it reports paths back via `worktree
// list --porcelain`, no matter what path was passed to `worktree add`.
//
// Caught for real via a live baron binary: macOS's /tmp -> /private/tmp
// (any project root under a symlinked path hits the same thing — an
// iCloud Drive folder, some Homebrew-managed locations, an NFS mount) made
// WorktreeManager.Create's own idempotency check (List(), string-compared
// against an unresolved m.Path(brn)) silently miss an already-existing
// worktree, so a second Create — from a brand new WorktreeManager
// instance, exactly what happens on every TUI restart via
// spawnAgentTerminal's reattach path — ran `git worktree add` again at the
// (canonically identical, string-different) path and failed outright
// ("already exists"), stranding a bead's still-alive agent as invisible
// ("press r to start it") with no way back except deleting the worktree by
// hand.
func TestWorktreeManagerCreateIdempotentAcrossSymlinkedRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	runner := tool.NewRunner()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if _, err := runner.Run(ctx, "git", args, tool.Options{Dir: link}); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(link, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")

	// repoDir/rootDir given via the SYMLINKED path — exactly what a real
	// project's own working directory looks like when it (or an ancestor)
	// is a symlink, the same as accessing a project under macOS's /tmp.
	rootDir := filepath.Join(link, ".baron", "worktrees")
	first := NewWorktreeManager(runner, link, rootDir)
	wt1, err := first.Create(ctx, "baron-a1", "task", "")
	if err != nil {
		t.Fatalf("first Create() error: %v", err)
	}

	// A brand new WorktreeManager instance — simulating the TUI restarting
	// and reattaching to a bead's still-alive agent (spawnAgentTerminal
	// calls Create again, from a fresh Deps/app, every time).
	second := NewWorktreeManager(runner, link, rootDir)
	wt2, err := second.Create(ctx, "baron-a1", "task", "")
	if err != nil {
		t.Fatalf("second Create() (reattach simulation) error: %v — must reuse the existing worktree, not fail", err)
	}
	if wt2.Branch != wt1.Branch {
		t.Errorf("second Create() branch = %q, want the same branch reused: %q", wt2.Branch, wt1.Branch)
	}
}
