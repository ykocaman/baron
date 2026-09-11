package tui

// bindingScope groups bindings by which screen/mode they apply in. This is
// the single source of truth the help screen renders from — hand-written
// help drifts, generated help doesn't — and that
// TestNoUppercaseExceptR / TestNoConflictingVerbsPerScope validate against.
type bindingScope string

const (
	scopeGlobal         bindingScope = "Global"
	scopeDashboardSplit bindingScope = "Dashboard"
	scopeMergeFlow      bindingScope = "Merge flow"
	scopeConfirm        bindingScope = "Confirm dialog"
	scopePicker         bindingScope = "Status/model picker"
	scopeForm           bindingScope = "Form (new bead / comment)"
	scopeCmdBar         bindingScope = "Command bar"
	scopePrompt         bindingScope = "Prompt Mode"
)

// Key strings repeated across the bindings table, the update*Key handlers,
// and the footer hint lists — kept as constants so the three stay in sync
// by construction instead of by matching typos.
const (
	keyShiftEsc       = "shift+esc"
	keyShiftLeftRight = "shift+←/→"
	keyShiftUpDown    = "shift+↑/↓"
)

// binding is one keyboard shortcut. verb is a short footer-style label;
// help is the fuller help-screen description.
type binding struct {
	key   string
	scope bindingScope
	verb  string
	help  string
}

// bindings is the full keymap. Keep this in sync with the per-screen
// update*Key handlers by construction: when a handler's switch changes,
// this list changes in the same commit.
var bindings = []binding{
	// Global
	{"?", scopeGlobal, "help", "toggle this help screen (works from anywhere)"},
	{":", scopeGlobal, "cmd", "open the command bar (runs baron CLI commands)"},
	{"/", scopeGlobal, "filter", "filter the dashboard list"},
	{"q", scopeGlobal, "quit", "quit (with confirmation); on a confirm dialog it means no/cancel"},
	{"esc", scopeGlobal, "back", "go back one screen (a no-op at the dashboard)"},
	{"ctrl+c", scopeGlobal, "quit", "quit immediately, no confirmation"},
	{"ctrl+l", scopeGlobal, "redraw", "redraw the screen"},
	{"P", scopeGlobal, "prompt", "toggle Prompt Mode — a live agent-CLI chat plus the Personas/Beads editors (works from any screen; a second P, or shift+esc from inside it, returns to the previous screen — a bare esc stays reserved for closing whatever's open inside it)"},
	{"+/-", scopeGlobal, "auto", "toggle auto-run for the selected bead"},

	// Dashboard (single split-pane screen)
	{"j/k", scopeDashboardSplit, "move", "move between rows (also ↑/↓)"},
	{"h/l", scopeDashboardSplit, "tab", "switch list tab (also 1-4 to jump directly)"},
	{"←/→", scopeDashboardSplit, "sub-tab", "left pane: step through the active tab's status buckets one at a time; spills into the next/previous tab once exhausted"},
	{keyShiftLeftRight, scopeDashboardSplit, "detail tab", "right pane: cycle the detail tabs (Overview/Terminal/Diff/Audit)"},
	{"g/G", scopeDashboardSplit, "top/bot", "jump to the first/last row"},
	{keyShiftUpDown, scopeDashboardSplit, "scroll", "right pane: scroll the detail pane one line (pgup/pgdn or the mouse wheel scroll by ten)"},
	{"pgup/pgdn", scopeDashboardSplit, "scroll", "scroll the detail pane (or the mouse wheel)"},
	{"n", scopeDashboardSplit, "new", "create a new bead"},
	{"p", scopeDashboardSplit, "prompt", "quick-prompt the selected bead — opens an inline text box; the typed text is sent straight into whichever agent-CLI tab is currently active in Prompt Mode's left pane (see also 'P')"},
	{"e", scopeDashboardSplit, "edit", "edit the selected bead's title/description — the fix for a typo made at 'n', which otherwise has no in-app correction path"},
	{"c", scopeDashboardSplit, "comment", "add a comment to the selected bead"},
	{"u", scopeDashboardSplit, "comments", "expand/collapse the Overview tab's comment list"},
	{"s", scopeDashboardSplit, "status", "change the selected bead's status"},
	{"a", scopeDashboardSplit, "assign", "assign a model"},
	{"y", scopeDashboardSplit, "copy", "copy to the system clipboard (OSC 52 — works over SSH/tmux, no mouse selection needed): the selected bead's BRN on Overview/Audit, or the visible Terminal/Diff pane's text on those tabs"},
	{"m", scopeDashboardSplit, "merge", "merge the selected bead's branch immediately, no confirmation — only works once the bead is ready to merge (visible inline in the Overview tab), which happens automatically once the gate passes"},
	{"x", scopeDashboardSplit, "stop", "stop the selected bead's running agent (if any) and send it to human_queue for a human decision (confirmation) — only valid from working/validating/retry"},
	{"r", scopeDashboardSplit, "run", "start the selected bead's agent in the embedded terminal (the Terminal tab); pressing it again brings a running agent back into view"},
	{"t", scopeDashboardSplit, "terminal", "toggle keyboard focus into the embedded terminal — type straight into the agent (esc goes to the agent too, since esc-esc is opencode's own interrupt gesture); on the Diff tab with no session running, retries starting it instead"},
	{keyShiftEsc, scopeDashboardSplit, "unfocus", "release keyboard focus from the embedded terminal back to the TUI (also exits zoom, if zoomed) — a bare esc is never used for this, since it must always reach the agent"},
	{"z", scopeDashboardSplit, "zoom", "zoom the embedded terminal fullscreen (hides the header/footer), or restore the split"},
	{"ctrl+d", scopeDashboardSplit, "close", "close the selected bead with double-press confirmation: first press arms the gesture and shows a 2 s notice; a second ctrl+d on the same bead within 2 s confirms; any other key cancels"},

	// Merge flow (preflight + approve/cancel/resolve-conflicts; 'd' jumps to
	// the bead's own Diff tab, zoomed, rather than duplicating a diff viewer
	// here)
	{"d", scopeMergeFlow, "diff", "review the change before deciding — jumps to the bead's Diff tab, zoomed"},
	{"c", scopeMergeFlow, "resolve", "launch the conflict resolver (when preflight is dirty)"},
	{"y", scopeMergeFlow, "merge", "confirm the merge"},
	{"n", scopeMergeFlow, "cancel", "cancel, go back"},

	// Confirm dialog
	{"enter/y", scopeConfirm, "yes", "confirm the pending action"},
	{"n/esc/q", scopeConfirm, "no", "cancel the pending action"},

	// Status/model pickers
	{"type", scopePicker, "filter", "type to filter the model list"},
	{"j/k", scopePicker, "move", "move the selection"},
	{"enter", scopePicker, "apply", "apply the selected status/model"},
	{"esc", scopePicker, "cancel", "close without changing anything"},

	// Form (new bead / comment)
	{"tab/shift+tab", scopeForm, "next/prev", "move between fields"},
	{"enter", scopeForm, "submit", "next field, or submit on the last field"},
	{"e", scopeForm, "parent", "jump to the parent (epic) field"},
	{"esc", scopeForm, "cancel", "cancel and go back"},

	// Command bar
	{"tab", scopeCmdBar, "complete", "complete command / subcommand / bead id"},
	{"enter", scopeCmdBar, "run", "run the typed command"},
	{"esc", scopeCmdBar, "cancel", "close the command bar"},

	// Prompt Mode (v7 redesign, docs/PRD/crew-mode.md) — 3 panes: left is a
	// tabbed, live agent-CLI chat; right, stacked, is a Personas/Beads list
	// over that selection's detail. shift+left/right and shift+up/down
	// always act (tab switch, list-cursor move), regardless of focus; the
	// per-tab verbs below likewise act regardless of focus, only gated by
	// which right-side tab is active.
	{"tab", scopePrompt, "focus", "cycle pane focus: left agent chat -> right list -> right detail -> left"},
	{keyShiftLeftRight, scopePrompt, "tab", "switch the right column between Personas and Beads"},
	{keyShiftUpDown, scopePrompt, "select", "move the right-top list's cursor (Personas or Beads, whichever tab is active) — works from any pane, including into an open persona's own work-output rows"},
	{"j/k", scopePrompt, "move", "left: (unused); right-top (focused): move the list cursor; right-bottom (focused, Beads tab): scroll the detail"},
	{"ctrl+h/ctrl+l", scopePrompt, "agent tab", "left pane (focused): switch which agent CLI's tab is active (also ←/→)"},
	{"t/enter", scopePrompt, "chat", "left pane (focused): focus keyboard into the active tab's live session, spawning it first if it isn't running yet"},
	{"enter", scopePrompt, "outputs", "Personas tab, on a persona row (or one of its own open work-output rows): toggle that persona's work-outputs accordion — focus the right-top list first (only one persona open at a time)"},
	{"space", scopePrompt, "on/off", "Personas tab: toggle the selected persona's enabled state"},
	{"e", scopePrompt, "edit", "Personas tab: edit the selected persona; Beads tab: edit the selected bead"},
	{"n", scopePrompt, "new", "Personas tab: create a new persona; Beads tab: create a new bead"},
	{"r", scopePrompt, "run", "Personas tab: run the selected persona now, ignoring its schedule/debounce"},
	{"c", scopePrompt, "comment", "Beads tab: comment on the selected bead as yourself"},
	{"u", scopePrompt, "comments", "Beads tab: expand/collapse the detail pane's comment list (mirrors Board Mode's own 'u')"},
	{"o", scopePrompt, "open", "hand a bead off to Board Mode's operational view (terminal/diff/gate) — the selected bead on the Beads tab, or the bead behind the cursor's work-output row on the Personas tab"},
	{keyShiftEsc, scopePrompt, "board mode", "return to Board Mode — a bare esc stays reserved for closing whatever's open (a form, the quick-prompt box) instead of also leaving Prompt Mode"},
}

// bindingsByScope groups bindings in declaration order, for the generated
// help screen.
func bindingsByScope() []struct {
	scope    bindingScope
	bindings []binding
} {
	var out []struct {
		scope    bindingScope
		bindings []binding
	}
	index := map[bindingScope]int{}
	for _, b := range bindings {
		i, ok := index[b.scope]
		if !ok {
			i = len(out)
			index[b.scope] = i
			out = append(out, struct {
				scope    bindingScope
				bindings []binding
			}{scope: b.scope})
		}
		out[i].bindings = append(out[i].bindings, b)
	}
	return out
}
