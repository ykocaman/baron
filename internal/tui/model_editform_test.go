package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/store"
)

// firstActionDone runs cmd, unwrapping a tea.Batch, and returns the
// actionDoneMsg inside it. Submitting a comment fans out into two commands —
// recording it on the bead and delivering it to the agent (see
// deliverCommentCmd) — so the recording result arrives inside a batch.
func firstActionDone(t *testing.T, cmd tea.Cmd) actionDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command after submit")
	}
	switch v := cmd().(type) {
	case actionDoneMsg:
		return v
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			if ad, ok := c().(actionDoneMsg); ok {
				return ad
			}
		}
		t.Fatal("no actionDoneMsg in the submit batch — the comment was never recorded on the bead")
	default:
		t.Fatalf("msg = %T, want actionDoneMsg (not the commandRanMsg bounce)", v)
	}
	return actionDoneMsg{}
}

// TestSplitArrowKeysAddressPanes pins the split view's arrow model down: a
// plain arrow acts on the left pane, the same arrow with shift acts on the
// right one. It replaced tab/shift+tab, which named no pane at all — and
// tab must no longer move the detail tabs, or the two models coexist and
// neither is learnable.
func TestSplitArrowKeysAddressPanes(t *testing.T) {
	m := openDetail(t, []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}})

	next, _ := m.Update(key("shift+right"))
	if got := asModel(next).detailTab; got != 1 {
		t.Errorf("shift+right: detailTab = %d, want 1 (right pane advances)", got)
	}
	next, _ = asModel(next).Update(key("shift+left"))
	if got := asModel(next).detailTab; got != 0 {
		t.Errorf("shift+left: detailTab = %d, want 0 (right pane steps back)", got)
	}
	next, _ = asModel(next).Update(key("tab"))
	if got := asModel(next).detailTab; got != 0 {
		t.Errorf("tab: detailTab = %d, want it unchanged — tab no longer switches detail tabs", got)
	}
}

// TestNewBeadFormNoStrayPlaceholder: huh v2.0.3 renders an empty input's
// placeholder as its first character in the cursor position, so a "required"
// placeholder painted a stray "r" into the Title field on open. The form
// must render no placeholder artifact (title "r" or comment "C").
func TestNewBeadFormNoStrayPlaceholder(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)

	next, _ = m.Update(key("n"))
	m = asModel(next)
	v := stripANSI(m.View().Content)
	for i, l := range splitLines(v) {
		if strings.Contains(l, "Title") && i+1 < len(splitLines(v)) {
			if got := strings.TrimSpace(splitLines(v)[i+1]); strings.HasPrefix(got, "> r") {
				t.Errorf("Title field renders stray %q — placeholder first-char bug", got)
			}
		}
	}

	next, _ = m.Update(key("esc"))
	m = asModel(next)
	next, _ = m.Update(key("c"))
	m = asModel(next)
	v = stripANSI(m.View().Content)
	for i, l := range splitLines(v) {
		if strings.Contains(l, "Comment") && i+1 < len(splitLines(v)) {
			if got := strings.TrimSpace(splitLines(v)[i+1]); strings.HasPrefix(got, "> C") {
				t.Errorf("Comment field renders stray %q — placeholder first-char bug", got)
			}
		}
	}
}

// TestEditBeadFormPrefillsAndSubmits: 'e' opens an edit form pre-filled with
// the selected bead's current title/description (not blank — the whole
// point is fixing a typo in what's already there, not starting over), and
// submitting calls Deps.EditBead with the edited title.
func TestEditBeadFormPrefillsAndSubmits(t *testing.T) {
	var gotBRN, gotTitle, gotDesc string
	d := testDeps()
	d.EditBead = func(brn, title, description string) (string, error) {
		gotBRN, gotTitle, gotDesc = brn, title, description
		return "updated " + brn, nil
	}
	m := New(context.Background(), d)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-a", Title: "Typo-ed title", Description: "original desc", Status: store.BeadStatusOpen},
	}})
	m = asModel(next)

	next, _ = m.Update(key("e"))
	m = asModel(next)
	if m.screen != screenForm || m.formKind != formKindEditBead {
		t.Fatalf("screen/formKind = %v/%v, want screenForm/formKindEditBead", m.screen, m.formKind)
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Typo-ed title") {
		t.Errorf("View() = %q, want the pre-filled existing title visible", view)
	}

	// Clear the pre-filled title (huh starts an Input's cursor at the end of
	// its bound value) and retype it.
	for range len("Typo-ed title") {
		next, _ = m.Update(key("backspace"))
		m = asModel(next)
	}
	for _, r := range "Fixed title" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, _ = m.Update(key("tab")) // title -> description
	m = asModel(next)
	next, cmd := m.Update(key("enter")) // submit on the last field
	m = asModel(next)
	if cmd == nil {
		t.Fatal("cmd = nil, want the EditBead action command")
	}
	msg := cmd()
	ad, ok := msg.(actionDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want actionDoneMsg", msg)
	}
	if _, _ = m.Update(ad); gotBRN != "baron-a" {
		t.Errorf("EditBead brn = %q, want baron-a", gotBRN)
	}
	if gotTitle != "Fixed title" {
		t.Errorf("EditBead title = %q, want %q", gotTitle, "Fixed title")
	}
	if gotDesc != "original desc" {
		t.Errorf("EditBead description = %q, want the untouched original %q", gotDesc, "original desc")
	}
}

// TestNewBeadCreateLandsOnTabWithNewBead: submitting the new-bead form must
// leave the dashboard on the tab holding the freshly created open bead, even
// when the previously active tab still has rows (the old first-non-empty
// fallback only fired when the Active tab was empty, so a create vanished
// from view whenever any bead was already working).
func TestNewBeadCreateLandsOnTabWithNewBead(t *testing.T) {
	d := testDeps()
	d.CreateBead = func(title, description, accept, priority, issueType, parent, tier string) (string, error) {
		return "created baron-new1: " + title, nil
	}
	m := New(context.Background(), d)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-work1", Title: "Working", Status: store.BeadStatusWorking},
	}})
	m = asModel(next)
	if m.listTab != 1 {
		t.Fatalf("listTab = %d, want 1 (Active has a working bead)", m.listTab)
	}

	next, _ = m.Update(key("n"))
	m = asModel(next)
	for _, r := range "new task" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	for range 5 { // title -> type -> tier -> parent -> description -> acceptance
		next, _ = m.Update(key("tab"))
		m = asModel(next)
	}
	next, cmd := m.Update(key("enter"))
	m = asModel(next)
	cr, ok := cmd().(commandRanMsg)
	if !ok {
		t.Fatalf("msg = %T, want commandRanMsg", cmd())
	}
	next, _ = m.Update(cr)
	m = asModel(next)

	next, _ = m.Update(beadsLoadedMsg{beads: []store.Bead{
		{BRN: "baron-work1", Title: "Working", Status: store.BeadStatusWorking},
		{BRN: "baron-new1", Title: "new task", Status: store.BeadStatusOpen},
	}})
	m = asModel(next)
	if m.listTab != 0 {
		t.Errorf("listTab = %d, want 0 (Backlog holds the new open bead)", m.listTab)
	}
	rows := m.currentRows()
	if len(rows) == 0 || rows[0].bead.BRN != "baron-new1" {
		t.Errorf("new bead not visible on the dashboard; rows = %v", rows)
	}
}

// stripANSI removes SGR escape sequences so rendered View() output can be
// asserted on; splitLines splits it without dropping blank lines.
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// --- Wave3 RED: sorting + shortcut no-conflict (must fail before GREEN) ---

func TestSplitRows_RecentActivityFirst(t *testing.T) {
	// revert: no bucket-local UpdatedAt sort — insertion/BRN order, children still brnNum
	old := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Old", Status: store.BeadStatusOpen, UpdatedAt: old},
		{BRN: "baron-b", Title: "Recent", Status: store.BeadStatusOpen, UpdatedAt: recent},
	}
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	rows := m.splitRows(0) // Backlog: open bucket
	if len(rows) < 2 {
		t.Fatalf("splitRows = %d, want 2", len(rows))
	}
	if rows[0].bead.BRN != "baron-a" || rows[1].bead.BRN != "baron-b" {
		t.Fatalf("splitRows order = %q, %q want baron-a then baron-b (reverted: no UpdatedAt, insertion/BRN wins)", rows[0].bead.BRN, rows[1].bead.BRN)
	}
}

func TestPromptBeadTreeRows_FrequencyWithinNonTerminal(t *testing.T) {
	old := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Old", Status: store.BeadStatusOpen, UpdatedAt: old},
		{BRN: "baron-b", Title: "Recent", Status: store.BeadStatusOpen, UpdatedAt: recent},
		{BRN: "baron-c", Title: "Mid", Status: store.BeadStatusOpen, UpdatedAt: mid},
	}
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	rows := m.promptBeadTreeRows()
	if len(rows) < 3 {
		t.Fatalf("promptBeadTreeRows = %d, want 3", len(rows))
	}
	if rows[0].bead.BRN != "baron-a" || rows[1].bead.BRN != "baron-b" || rows[2].bead.BRN != "baron-c" {
		t.Fatalf("promptBeadTreeRows order = %q %q %q want a b c (reverted BRN asc, no UpdatedAt)", rows[0].bead.BRN, rows[1].bead.BRN, rows[2].bead.BRN)
	}
}

func TestPromptBeadTreeRows_TerminalStillLast(t *testing.T) {
	old := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	beads := []store.Bead{
		{BRN: "baron-a", Title: "Open old", Status: store.BeadStatusOpen, UpdatedAt: old},
		{BRN: "baron-b", Title: "Closed recent", Status: store.BeadStatusClosed, UpdatedAt: recent},
		{BRN: "baron-c", Title: "Open mid", Status: store.BeadStatusOpen, UpdatedAt: mid},
		{BRN: "baron-d", Title: "Closed old", Status: store.BeadStatusClosed, UpdatedAt: old},
	}
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)
	rows := m.promptBeadTreeRows()
	if len(rows) != 4 {
		t.Fatalf("promptBeadTreeRows = %d, want 4", len(rows))
	}
	if rows[0].bead.BRN != "baron-a" || rows[1].bead.BRN != "baron-c" {
		t.Fatalf("non-terminal order = %q %q want a c (BRN asc, terminal last)", rows[0].bead.BRN, rows[1].bead.BRN)
	}
	if rows[2].bead.BRN != "baron-b" || rows[3].bead.BRN != "baron-d" {
		t.Fatalf("terminal partition = %q %q want b d last (BRN asc)", rows[2].bead.BRN, rows[3].bead.BRN)
	}
}

func TestEmptyPane_NoPanic(t *testing.T) {
	m := New(context.Background(), testDeps())
	next, _ := m.Update(beadsLoadedMsg{beads: nil})
	m = asModel(next)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on empty pane: %v", r)
		}
	}()
	_ = m.splitRows(0)
	_ = m.promptBeadTreeRows()
	_ = m.View().Content
	if len(m.currentRows()) != 0 {
		t.Fatalf("currentRows on empty = %d, want 0", len(m.currentRows()))
	}
	// RED gate: empty pane must not claim a selected bead
	if m.detail.BRN != "" {
		t.Fatalf("detail.BRN = %q on empty pane, want empty (no panic and no ghost selection)", m.detail.BRN)
	}
}
