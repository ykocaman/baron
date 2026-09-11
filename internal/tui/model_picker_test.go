package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/baron-cli/baron/internal/store"
)

// TestModelPickerAssignsSelectedModel: a opens a model picker; enter runs
// `work assign <BRN> --model <chosen>` for the selected real model name.
func TestModelPickerAssignsSelectedModel(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	deps := testDeps()
	deps.Models = func() ([]string, error) {
		return []string{"big-pickle", "deepseek-v4-flash"}, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelForm == nil {
		t.Fatal("modelForm = nil, want the picker form open")
	}
	// The form's bound result should be set after picking
	if m.modelResult == nil {
		t.Fatal("modelResult = nil, want bound result pointer")
	}

	// Filter down to the target model by typing, rather than counting
	// "down" presses — robust against the list's own ordering.
	for _, r := range "deepseek" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, cmd = m.Update(key("enter"))
	m = asModel(next)
	// Form completion is deferred in test harness - check bound value
	if *m.modelResult != "deepseek-v4-flash" {
		t.Fatalf("modelResult = %q, want deepseek-v4-flash", *m.modelResult)
	}
	// Should have a completion command (batch with form completion + assign)
	if cmd == nil {
		t.Fatal("expected an assign command after picking a model")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	var ran commandRanMsg
	for _, c := range batch {
		if c == nil {
			continue
		}
		if cr, ok := c().(commandRanMsg); ok {
			ran = cr
			break
		}
	}
	if !strings.Contains(ran.output, "work assign baron-a") || !strings.Contains(ran.output, "deepseek-v4-flash") {
		t.Errorf("output = %q, want the assign command for the picked model", ran.output)
	}
	cache := loadModelCache()
	if cache.Counts["deepseek-v4-flash"] != 1 {
		t.Errorf("cache counts = %v, want deepseek-v4-flash recorded once", cache.Counts)
	}
}

// TestModelPickerOpensEffortStepForClaude: picking a model with selectable
// reasoning effort (Deps.EffortChoices) must not assign immediately — it
// opens the follow-up effort overlay, and only the effort pick (or an
// explicit skip) actually runs `work assign`.
func TestModelPickerOpensEffortStepForClaude(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	deps := testDeps()
	deps.Models = func() ([]string, error) { return []string{"claude"}, nil }
	deps.EffortChoices = func(picked string) ([]string, error) {
		if picked != "claude" {
			t.Fatalf("EffortChoices(%q), want claude", picked)
		}
		return []string{"low", "medium", "high", "xhigh", "max"}, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)

	// Filter to "claude" by typing, rather than counting "down" presses —
	// robust against the list's own ordering.
	for _, r := range "claude" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, _ = m.Update(key("enter")) // select "claude"
	m = asModel(next)
	// Model picker form should be done, effort picker should open
	if m.effortForm == nil {
		t.Fatal("effortForm = nil, want the effort picker form open")
	}
	if m.effortResult == nil {
		t.Fatal("effortResult = nil, want bound result pointer")
	}
	if m.pendingAssignModel != "claude" {
		t.Errorf("pendingAssignModel = %q, want claude", m.pendingAssignModel)
	}

	next, _ = m.Update(key("down")) // move to "medium"
	m = asModel(next)
	next, cmd = m.Update(key("enter"))
	m = asModel(next)
	if m.effortForm != nil {
		t.Error("effortForm != nil, want the overlay closed after picking")
	}
	if cmd == nil {
		t.Fatal("expected an assign command after picking an effort level")
	}
	ran, ok := cmd().(commandRanMsg)
	if !ok {
		t.Fatalf("msg = %T, want commandRanMsg", cmd())
	}
	if !strings.Contains(ran.output, "work assign baron-a") || !strings.Contains(ran.output, "claude") || !strings.Contains(ran.output, "medium") {
		t.Errorf("output = %q, want the assign command carrying effort medium", ran.output)
	}
}

// TestModelPickerEffortStepSkippable: esc on the effort overlay still
// assigns, just without an --effort flag (the agent's own default).
func TestModelPickerEffortStepSkippable(t *testing.T) {
	deps := testDeps()
	deps.Models = func() ([]string, error) { return []string{"claude"}, nil }
	deps.EffortChoices = func(string) ([]string, error) { return []string{"low", "high"}, nil }
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	// Filter to "claude" by typing, rather than counting "down" presses.
	for _, r := range "claude" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	next, _ = m.Update(key("enter")) // select "claude"
	m = asModel(next)
	if m.effortForm == nil {
		t.Fatal("effortForm = nil, want the effort picker form open")
	}
	if m.effortResult == nil {
		t.Fatal("effortResult = nil, want bound result pointer")
	}
	next, cmd = m.Update(key("esc"))
	m = asModel(next)
	if m.effortForm != nil {
		t.Error("effortForm != nil, want the overlay closed after esc")
	}
	if cmd == nil {
		t.Fatal("expected an assign command after skipping effort")
	}
	ran, ok := cmd().(commandRanMsg)
	if !ok {
		t.Fatalf("msg = %T, want commandRanMsg", cmd())
	}
	want := "ran: work assign baron-a  claude "
	if ran.output != want {
		t.Errorf("output = %q, want %q (empty agent, model claude, empty effort after skipping)", ran.output, want)
	}
}

// TestModelPickerAssignsWholeIDWithEmptyAgent: picking any catalog entry —
// a "claude/<alias>" shorthand, an opencode-catalog "provider/model" pair,
// or any other agent's own compound ID — must call Deps.Assign with the
// picked string whole as modelID and agentName left empty, letting
// AssignBead's own catalog-based resolveAssignment (internal/cli/
// work_mutate.go) resolve the real agent from it — that mapping isn't
// recoverable from the ID string itself (opencode's own IDs are
// "provider/model" pairs whose prefix names a provider, not a CLI).
//
// This replaces a real, shipping bug: the TUI used to split the ID itself
// (splitPickedModel), special-casing a "claude/" prefix and treating every
// other prefix as "opencode" — so picking "agy/gemini-3.5-flash-low" (a
// real, distinct installed agent) silently assigned the bead to opencode
// with that whole string as an unparseable model ID. Caught for real via
// `bd show` reporting Assignee: opencode, model: agy/gemini-3.5-flash-low
// right after assigning to agy through the picker.
func TestModelPickerAssignsWholeIDWithEmptyAgent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var gotBRN, gotAgent, gotModel, gotEffort string
	deps := testDeps()
	deps.Models = func() ([]string, error) { return []string{"claude/haiku"}, nil }
	deps.Assign = func(brn, agentName, modelID, effort string) (string, error) {
		gotBRN, gotAgent, gotModel, gotEffort = brn, agentName, modelID, effort
		return "assigned", nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	next, cmd = m.Update(key("enter"))
	_ = asModel(next)
	if cmd == nil {
		t.Fatal("expected an assign command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	for _, c := range batch {
		if c != nil {
			c()
		}
	}
	if gotBRN != "baron-a" || gotAgent != "" || gotModel != "claude/haiku" || gotEffort != "" {
		t.Errorf("Assign(brn, agent, model, effort) = (%q, %q, %q, %q), want (\"baron-a\", \"\", \"claude/haiku\", \"\")",
			gotBRN, gotAgent, gotModel, gotEffort)
	}
}

// TestModelPickerEscCloses: esc closes the model picker without assigning.
func TestModelPickerEscCloses(t *testing.T) {
	deps := testDeps()
	deps.Models = func() ([]string, error) {
		return []string{"big-pickle"}, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelForm == nil {
		t.Fatal("modelForm = nil, want the picker open")
	}
	next, cmd = m.Update(key("esc"))
	m = asModel(next)
	if m.modelForm != nil {
		t.Fatal("modelForm != nil, want picker form closed after esc")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil on cancel", cmd)
	}
}

// TestModelPickerCenteredRender: the picker draws huh's own Select field
// (border box, "/" filter prompt, option list) centered in the overlay —
// not a hand-rolled list. Comparing against a second, separate
// form.View() call would be flaky (the filter textinput's cursor blinks,
// so two calls can render different ANSI), so this checks structural
// markers from the single View() the model produced instead.
func TestModelPickerCenteredRender(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	deps := testDeps()
	deps.Models = func() ([]string, error) { return []string{"alpha", "beta", "gamma"}, nil }
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	m.width, m.height = 80, 40
	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelForm == nil {
		t.Fatal("modelForm = nil, want the picker form open")
	}
	view := m.View().Content
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Errorf("View() = %q, want the overlay box border", view)
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() = %q, want model %q listed", view, want)
		}
	}
}

// TestModelPickerSearchFilters: typing in the popup filters the model list
// live; enter assigns the filtered selection.
func TestModelPickerSearchFilters(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	deps := testDeps()
	deps.Models = func() ([]string, error) {
		return []string{"deepseek-v4-flash", "big-pickle", "gpt-5"}, nil
	}
	m := New(context.Background(), deps)
	next, _ := m.Update(beadsLoadedMsg{beads: []store.Bead{{BRN: "baron-a", Title: "A", Status: store.BeadStatusOpen}}})
	m = asModel(next)
	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	if m.modelForm == nil {
		t.Fatal("modelForm = nil, want the picker form open")
	}

	// Type to filter - the form's Filtering(true) handles this internally
	for _, r := range "deep" {
		next, _ = m.Update(key(string(r)))
		m = asModel(next)
	}
	// The form's bound result should reflect the filtered selection
	if m.modelResult == nil {
		t.Fatal("modelResult = nil, want bound result pointer")
	}
	// Verify filtered view
	view := m.View().Content
	if !strings.Contains(view, "deepseek-v4-flash") || strings.Contains(view, "big-pickle") {
		t.Errorf("View() = %q, want only the matching model listed", view)
	}

	next, cmd = m.Update(key("enter"))
	m = asModel(next)
	if cmd == nil {
		t.Fatal("expected a command after enter")
	}
	// The command should be a batch containing the form's completion command and our assign command
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	var ran commandRanMsg
	for _, c := range batch {
		if c == nil {
			continue
		}
		if cr, ok := c().(commandRanMsg); ok {
			ran = cr
			break
		}
	}
	if !strings.Contains(ran.output, "deepseek-v4-flash") {
		t.Fatalf("assign output = %v, want the filtered model assigned", ran)
	}
	if m.modelForm != nil {
		t.Fatal("modelForm != nil, want picker form closed after assign")
	}
}

// TestModelCacheRanking: the usage index orders recent (last 5, by last use)
// then popular (top 3, by count, not already shown) then the rest in their
// original order.
// TestModelCacheRanking: the usage index orders recent (last 5, by last use)
// then popular (top 3, by count, not already shown) then the rest in their
// original order.
func TestModelCacheRanking(t *testing.T) {
	base := time.Now()
	last := map[string]time.Time{
		"a": base.Add(1 * time.Hour),
		"b": base.Add(2 * time.Hour),
		"c": base.Add(3 * time.Hour),
		"d": base.Add(4 * time.Hour),
		"e": base.Add(5 * time.Hour),
		"f": base.Add(6 * time.Hour),
		"g": base.Add(7 * time.Hour),
		"h": base.Add(8 * time.Hour),
		"i": base.Add(9 * time.Hour),
		"j": base.Add(10 * time.Hour),
	}
	counts := map[string]int{"a": 9, "b": 8, "c": 7, "d": 6, "e": 5, "f": 4, "g": 3, "h": 2, "i": 1, "j": 1}
	models := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	recent, popular, rest := rank(&modelCache{Counts: counts, Last: last}, models)
	if !slices.Equal(recent, []string{"j", "i", "h", "g", "f"}) {
		t.Errorf("recent = %v, want the 5 most recently used, newest first", recent)
	}
	if !slices.Equal(popular, []string{"a", "b", "c"}) {
		t.Errorf("popular = %v, want the 3 most used not already shown", popular)
	}
	if !slices.Equal(rest, []string{"d", "e"}) {
		t.Errorf("rest = %v, want the remainder in original order", rest)
	}
}

// TestModelCachePersistsUsage: a recorded assign survives a reload.
func TestModelCachePersistsUsage(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_ = loadModelCache().Record("claude").Save()
	c := loadModelCache()
	if c.Counts["claude"] != 1 || c.Last["claude"].IsZero() {
		t.Errorf("cache = counts:%v last:%v, want claude recorded with a timestamp", c.Counts, c.Last)
	}
}

// TestModelPickerAlwaysFetchesFreshList: every genuine picker open (close,
// then reopen) re-fetches Deps.Models rather than serving a stale list.
// Deps.Models reads the machine-wide model catalog (a cheap file read, not
// a provider subprocess spawn), so unlike the old opencode-subprocess-backed
// implementation there is no cost to avoid by caching the list a second
// time here — and caching it for up to an hour (the old behavior) hid
// newly discovered or newly-removed models from the picker for that long.
func TestModelPickerAlwaysFetchesFreshList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	calls := 0
	deps := testDeps()
	deps.Models = func() ([]string, error) {
		calls++
		return []string{"alpha", "beta"}, nil
	}
	m := New(context.Background(), deps)
	beads := []store.Bead{{BRN: "baron-a", Title: "Alpha", Status: store.BeadStatusOpen}}
	next, _ := m.Update(beadsLoadedMsg{beads: beads})
	m = asModel(next)

	next, cmd := m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)
	next, _ = m.Update(key("esc"))
	m = asModel(next)
	next, cmd = m.Update(key("a"))
	m = asModel(next)
	m = pumpModels(t, m, cmd)

	if calls != 2 {
		t.Errorf("provider Models() called %d times, want 2 (close+reopen must re-fetch)", calls)
	}
	if m.modelChoices == nil || len(m.modelChoices) != 2 {
		t.Errorf("modelChoices = %v, want the freshly fetched list", m.modelChoices)
	}
}

// TestModelCacheCorruptFallsBack: a corrupt cache file silently yields the
// unsorted model order.
func TestModelCacheCorruptFallsBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "baron"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "baron", "models.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	recent, popular, rest := rank(loadModelCache(), []string{"x", "y"})
	if len(recent) != 0 || len(popular) != 0 || !slices.Equal(rest, []string{"x", "y"}) {
		t.Errorf("rank on corrupt cache = recent:%v popular:%v rest:%v, want unsorted fallback", recent, popular, rest)
	}
}
