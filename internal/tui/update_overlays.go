package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/domain"
)

func (m Model) openModelPicker(brn string) (Model, tea.Cmd) {
	m.modelCursor = 0
	m.modelSearch = ""
	if m.deps.Models == nil {
		// No model source: fall back to the command bar so assign still works.
		m.modelChoices = nil
		m.modelForm = nil
		m.modelResult = nil
		m.modelOptions = nil
		return m.startCommandPrefill("work assign " + brn + " --model "), nil
	}
	// The old 1h list cache existed only to avoid re-spawning the provider
	// subprocess on every open, which no longer applies, and it actively
	// went stale: a model newly discovered by `baron doctor` (or removed
	// from PATH) wouldn't show up in the picker for up to an hour. Always
	// fetch fresh; only the usage-based recent/popular ranking is cached.
	m.modelChoices = []string{}
	m.modelRecent = nil
	m.modelPopular = nil
	m.modelLoading = true
	m.pendingAssignBRN = brn
	m.modelForm = nil
	m.modelResult = nil
	m.modelOptions = nil
	return m, loadModels(m.deps)
}

// updateModelPickerKey handles keys while the model picker is open: typing
// filters live (via huh's Filtering), j/k move, enter assigns (recording usage),
// esc/q close.
func (m Model) updateModelPickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	// Handle esc/q directly to close the picker immediately (form.State
	// doesn't transition to Aborted in test harness without tea.Program).
	// Checked before the modelForm==nil guard below: the picker counts as
	// "open" (m.modelChoices != nil, see updateKey's dispatch) from the
	// moment Deps.Models starts loading, before modelForm exists — esc/q
	// must be able to cancel during that window too, not just once the form
	// has landed.
	if msg.String() == "esc" || msg.String() == "q" {
		m.modelChoices = nil
		m.modelForm = nil
		m.modelResult = nil
		m.modelLoading = false
		m.runAfterAssign = ""
		return m, nil
	}
	if m.modelForm == nil {
		return m, nil
	}
	form := m.modelForm
	newModel, cmd := form.Update(msg)
	newForm, ok := newModel.(*huh.Form)
	if !ok {
		return m, cmd
	}
	m.modelForm = newForm

	// Check if the form's completion command was returned. In test harness
	// without tea.Program, form.State doesn't reach Completed but the form
	// returns a completion command that would be processed by tea.Program.
	// Gated to enter/tab (Select's Next/Submit keys): with Filtering(true),
	// every filter keystroke also routes through the filter textinput's own
	// Update, which returns its own (blink) cmd — treating any non-nil cmd
	// as completion would fire the assign command on every character typed
	// into the filter box.
	if cmd != nil && (msg.String() == "enter" || msg.String() == "tab") {
		// Form completed - the bound value is already set in m.modelResult
		picked := *m.modelResult
		_ = loadModelCache().Record(picked).Save()
		if m.deps.EffortChoices != nil {
			if levels, err := m.deps.EffortChoices(picked); err == nil && len(levels) > 0 {
				// This model has a selectable reasoning-effort level: hold
				// the picked model and open the follow-up effort overlay
				// instead of assigning immediately. esc/no-choice on that
				// overlay still assigns — effort just falls back to the
				// agent's own default (assignCmd with effort="").
				m.effortChoices = levels
				m.effortCursor = 0
				m.pendingAssignModel = picked

				var result string
				selectField := huh.NewSelect[string]().
					Title("Reasoning effort — " + picked).
					Options(huh.NewOptions(levels...)...).
					Value(&result)
				form := newOverlayForm(huh.NewGroup(selectField))
				m.effortForm = initHuhForm(form)
				m.effortResult = &result
				// Clear model picker form, keep modelResult for test verification
				m.modelForm = nil
				m.modelChoices = nil
				return m, tea.Batch(cmd, nil)
			}
		}
		brn := m.pendingAssignBRN
		// Close the picker overlay; modelResult is left set (not nilled) so
		// callers/tests can still read the picked value off the model
		// returned by this Update call.
		m.modelForm = nil
		m.modelChoices = nil
		m.modelLoading = false
		// Chain form's completion command with our assign command
		return m, tea.Batch(cmd, assignCmd(m.deps, brn, picked, ""))
	}
	return m, nil
}

// assignCmd runs Deps.Assign for the picker's chosen catalog entry,
// reported as a commandRanMsg (a footer status line + board reload) —
// matches how the old string-arg `runCommand(deps, assignArgsForPicked(...))`
// reported it.
//
// agentName is always passed empty here, picked always whole as modelID:
// which CLI agent actually runs a catalog ID is not recoverable from the ID
// string itself (opencode's own IDs are "provider/model" pairs like
// "opencode-go/deepseek-v4-flash" whose prefix names a provider, not a CLI
// — see AssignBead's doc comment), so guessing it client-side from a "/"
// split is exactly the bug this used to have: splitPickedModel treated any
// non-"claude" prefix as "opencode" verbatim, so picking e.g.
// "agy/gemini-3.5-flash-low" (a real, distinct agent this project has
// installed) silently assigned the bead to opencode with that whole string
// as an unparseable model ID — caught for real via `bd show` reporting
// Assignee: opencode, model: agy/gemini-3.5-flash-low on a bead the picker
// had just been told to assign to agy. AssignBead's own resolveAssignment
// already does this correctly via a catalog lookup (the exact fix
// effortChoicesFor's sibling doc comment describes) whenever agentName is
// empty — the picker's job is just to hand over the ID it displayed,
// unmodified.
func assignCmd(deps Deps, brn, picked, effort string) tea.Cmd {
	return runTypedCommand(func() (string, error) {
		return deps.Assign(brn, "", picked, effort)
	})
}

// updateEffortPickerKey handles keys while the follow-up effort-level
// overlay is open (after a model was already chosen in the model picker):
// the huh form drives the selection; on completion or abort we run the
// assign command with the selected effort (or empty string on abort).
func (m Model) updateEffortPickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.effortForm == nil {
		return m, nil
	}

	// Handle esc/q explicitly to abort the form (Select field may not propagate esc)
	if msg.String() == "esc" || msg.String() == "q" {
		return m.finishAssignWithEffort("")
	}

	form := m.effortForm
	newModel, cmd := form.Update(msg)
	newForm, ok := newModel.(*huh.Form)
	if !ok {
		return m, cmd
	}
	m.effortForm = newForm

	// Check if form completed (cmd != nil indicates completion in test harness)
	if cmd != nil {
		effort := ""
		if m.effortResult != nil && *m.effortResult != "" {
			effort = *m.effortResult
		}
		return m.finishAssignWithEffort(effort)
	}
	return m, nil
}

// finishAssignWithEffort closes the effort overlay and runs the `work
// assign` command held since the model picker's enter — shared by both ways
// updateEffortPickerKey ends the overlay (esc/q with an empty effort, or
// the form completing with the picked one).
func (m Model) finishAssignWithEffort(effort string) (Model, tea.Cmd) {
	brn, picked := m.pendingAssignBRN, m.pendingAssignModel
	m.effortForm = nil
	m.effortResult = nil
	m.effortChoices = nil
	m.pendingAssignModel = ""
	return m, assignCmd(m.deps, brn, picked, effort)
}

// updateStatusMenuKey handles keys while the status menu is open on the
// bead detail screen. Rewritten off the "a non-nil cmd from form.Update
// means the form just completed" shortcut updateModelPickerKey/
// updateEffortPickerKey use — that shortcut only holds for THOSE pickers
// because completing is the only way a one-field group's Next key produces
// a command. It happened to also read true here, but for the wrong reason:
// completing this menu returns a command wrapping huh's own internal
// NextField advance (charm.land/huh/v2.NextField, producing a bare
// nextFieldMsg{}) batched together with our own action command — and nobody
// downstream of Model.Update ever recognizes nextFieldMsg, so on every
// completion that command rode all the way out to the real tea.Program
// attached to a *form this code had already discarded*
// (m.statusForm = nil happened in the very same branch). Repeatedly opening
// the menu kept releasing one more of those into the runtime's queue with
// nothing to receive it. pumpHuhForm resolves that transition here instead,
// synchronously, so the returned tea.Cmd is only ever this handler's own
// action — and huh.Form.State becomes the actual completion signal instead
// of a coincidence.
func (m Model) updateStatusMenuKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.statusForm == nil {
		return m, nil
	}
	// huh's Select field has no case for a bare "esc" (field_select.go only
	// binds it to SetFilter/ClearFilter, both gated on active filtering), so
	// it never self-aborts — handle esc/q directly, same as
	// updateModelPickerKey.
	if msg.String() == "esc" || msg.String() == "q" {
		m.statusForm = nil
		m.statusTargets = nil
		return m, nil
	}
	form := m.statusForm
	newModel, cmd := form.Update(msg)
	newForm, ok := newModel.(*huh.Form)
	if !ok {
		return m, cmd
	}
	newForm = pumpHuhForm(newForm, cmd, 5)
	m.statusForm = newForm

	if newForm.State == huh.StateAborted {
		m.statusForm = nil
		m.statusTargets = nil
		return m, nil
	}
	if newForm.State == huh.StateCompleted {
		target := *m.statusResult
		brn := m.detail.BRN
		m.statusForm = nil
		m.statusTargets = nil
		if target == domain.BeadStateAssigned {
			return m.openModelPicker(string(brn))
		}
		return m, runTypedAction(func() (string, error) { return m.deps.ChangeStatus(string(brn), string(target)) })
	}
	return m, nil
}

// togglePromptMode opens/closes Prompt Mode ('P') — see screenCrew's doc
// comment. Opening always lands on the Beads tab, right-top focused, with
// the persona list and every persona's live status freshly fetched (even
// though Personas isn't the tab shown first) so switching to it is instant.
