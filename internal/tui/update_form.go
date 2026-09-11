package tui

import (
	"errors"
	"reflect"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/persona"
)

func formFieldIsFiltering(form *huh.Form) bool {
	ms, ok := form.GetFocusedField().(*huh.MultiSelect[string])
	return ok && ms.GetFiltering()
}

func (m Model) updateFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.beadForm == nil {
		return m, nil
	}
	form := m.beadForm
	if msg.String() == "ctrl+c" || (msg.String() == "esc" && !formFieldIsFiltering(form)) {
		m.beadForm = nil
		m.screen = m.formReturnScreen
		return m, nil
	}
	newModel, cmd := form.Update(msg)
	newForm, ok := newModel.(*huh.Form)
	if !ok {
		return m, cmd
	}
	newForm = pumpHuhForm(newForm, cmd, 5)
	m.beadForm = newForm
	if newForm.State == huh.StateAborted {
		m.beadForm = nil
		m.screen = m.formReturnScreen
		return m, nil
	}
	if newForm.State == huh.StateCompleted {
		m.beadForm = nil
		return m.submitForm()
	}
	return m, nil
}

// pumpHuhForm resolves huh's own internal field/group-advance messages
// (nextFieldMsg, nextGroupMsg, ...) synchronously so that a single keypress
// fully applies — advancing focus, or completing the form on its last
// field — within one call. A real tea.Program does this by executing the
// returned command and feeding the resulting message back through
// Model.Update over subsequent frames; this codebase drives every huh form
// purely by return values instead (see updateStatusMenuKey), so nothing
// else will ever perform that round trip for a multi-field form. Bounded by
// depth, and — critically — never calls a command that looks like a cursor
// blink tick (see cmdLooksLikeBlink): unlike huh's structural nextField/
// nextGroup commands, a focused Input/Text field hands back one of those on
// nearly every keystroke, and it is a real tea.Tick — calling it blocks the
// calling goroutine for the cursor's blink interval (bubbles v2 defaults to
// 530ms) and then hands back another blink command just like it, so
// draining it here would either run out of depth slowly or, at shallow
// depth, silently cost every keypress half a second for no behavioral
// effect (blink is purely cosmetic and this form's box is never drawn
// through a live renderer in the first place).
func pumpHuhForm(form *huh.Form, cmd tea.Cmd, depth int) *huh.Form {
	if cmd == nil || depth <= 0 || cmdLooksLikeBlink(cmd) {
		return form
	}
	msg := cmd()
	if msg == nil {
		return form
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			form = pumpHuhForm(form, c, depth-1)
		}
		return form
	}
	newModel, newCmd := form.Update(msg)
	f, ok := newModel.(*huh.Form)
	if !ok {
		return form
	}
	return pumpHuhForm(f, newCmd, depth-1)
}

// cmdLooksLikeBlink reports whether cmd is (or was built from) a bubbles
// cursor-blink command, identified by its compiled function name — e.g.
// "charm.land/bubbles/v2/cursor.(*Model).Blink.func1" — without invoking
// it. Checked before every pumpHuhForm call precisely so the blocking
// tea.Tick inside it never runs.
func cmdLooksLikeBlink(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	name := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer()).Name()
	return strings.Contains(strings.ToLower(name), "blink") || strings.Contains(strings.ToLower(name), "cursor")
}

// initHuhForm runs form.Init() through pumpHuhForm instead of the
// recursive `next, _ := m.Update(initCmd())` dance the rest of this file
// used to reach for (see git history on updateStatusMenuKey) — that
// re-entrant call fed Init's result back into the *whole* Model.Update,
// including its top-level key/mouse/message switch, for a message no case
// there ever matched; it was a no-op wearing the shape of initialization
// code. Driving Init through the same blink-safe pump every keypress
// already uses is both more honest about what actually happens (nothing
// outside the form needs to see Init's message) and guarantees the exact
// same safety property: no cursor-blink tea.Tick ever gets invoked
// synchronously here either.
func initHuhForm(form *huh.Form) *huh.Form {
	return pumpHuhForm(form, form.Init(), 5)
}

// readPersonaFormFields reads the persona edit/new form's shared field
// pointers into one persona.FormFields — the field-reading logic
// formKindPersonaEdit and formKindPersonaNew's submitForm cases used to
// each duplicate almost verbatim. ID/Name are deliberately left unset: only
// formKindPersonaNew's form has ID/Name fields (Edit's target persona is
// m.editingPersonaID, never renamed), so each case sets those itself on the
// returned value.
func readPersonaFormFields(m Model) (persona.FormFields, error) {
	prompt := strings.TrimSpace(*m.formPersonaPromptResult)
	if prompt == "" {
		return persona.FormFields{}, errors.New("prompt is required")
	}
	customStr := ""
	if m.formPersonaEventsCustomStr != nil {
		customStr = *m.formPersonaEventsCustomStr
	}
	events := personaEventsText(*m.formPersonaEventsResult, customStr)
	if _, err := persona.ParseTransitionRules(events); err != nil {
		return persona.FormFields{}, err
	}
	tier := "standard"
	if m.formPersonaTierResult != nil && *m.formPersonaTierResult != "" {
		tier = *m.formPersonaTierResult
	}
	agent := ""
	if m.formPersonaAgentResult != nil {
		agent = *m.formPersonaAgentResult
	}
	bdWrite := false
	if m.formPersonaBDWriteResult != nil {
		bdWrite = *m.formPersonaBDWriteResult == "allowed"
	}
	var bdActions []string
	if m.formPersonaBDActionsResult != nil {
		bdActions = *m.formPersonaBDActionsResult
	}
	return persona.FormFields{
		Prompt:      prompt,
		Description: strings.TrimSpace(*m.formDescResult),
		Schedule:    strings.TrimSpace(*m.formPersonaScheduleResult),
		Events:      events,
		Skills:      strings.TrimSpace(*m.formPersonaSkillsResult),
		ModelTier:   tier,
		ModelAgent:  agent,
		BDWrite:     bdWrite,
		BDActions:   bdActions,
		Enabled:     *m.formPersonaEnabledResult == "on",
	}, nil
}

// readNewBeadFormFields reads the new-bead form's shared field pointers —
// the field-reading logic formKindNewBead and formKindPromptNewBead's
// submitForm cases each used to duplicate verbatim. Returns an already-user-
// facing error (via errEmptyTitle/errEmptyTier) for a blank title or tier,
// same as each case's own inline checks did.
func readNewBeadFormFields(m Model) (title, desc, accept, typ, parent, tier string, err error) {
	title = strings.TrimSpace(*m.formTitleResult)
	if title == "" {
		return "", "", "", "", "", "", errEmptyTitle
	}
	tier = strings.TrimSpace(*m.formTierResult)
	if tier == "" {
		return "", "", "", "", "", "", errEmptyTier
	}
	typ = strings.TrimSpace(*m.formTypeResult)
	parent = strings.TrimSpace(*m.formParentResult)
	desc = strings.TrimSpace(*m.formDescResult)
	accept = strings.TrimSpace(*m.formAcceptResult)
	return title, desc, accept, typ, parent, tier, nil
}

// submitPersonaEditForm handles submitForm's formKindPersonaEdit case.
func (m Model) submitPersonaEditForm() (tea.Model, tea.Cmd) {
	if m.deps.UpdatePersona == nil {
		return m, nil
	}
	f, err := readPersonaFormFields(m)
	if err != nil {
		return m, m.notifyErr(err)
	}
	f.ID = m.editingPersonaID
	return m, personaUpdateCmd(m.deps, f)
}

// submitPersonaNewForm handles submitForm's formKindPersonaNew case.
func (m Model) submitPersonaNewForm() (tea.Model, tea.Cmd) {
	if m.deps.CreatePersona == nil {
		return m, nil
	}
	name := strings.TrimSpace(*m.formPersonaNameResult)
	if name == "" {
		return m, m.notifyErr(errEmptyTitle)
	}
	f, err := readPersonaFormFields(m)
	if err != nil {
		return m, m.notifyErr(err)
	}
	f.Name = name
	if m.formPersonaIDResult != nil {
		f.ID = strings.TrimSpace(*m.formPersonaIDResult)
	}
	return m, personaCreateCmd(m.deps, f)
}

func (m Model) submitForm() (tea.Model, tea.Cmd) {
	m.screen = m.formReturnScreen
	switch m.formKind {
	case formKindNewBead:
		title, desc, accept, typ, parent, tier, err := readNewBeadFormFields(m)
		if err != nil {
			return m, m.notifyErr(err)
		}
		m.pendingNewBead = true
		return m, runTypedCommand(func() (string, error) {
			return m.deps.CreateBead(title, desc, accept, "", typ, parent, tier)
		})
	case formKindComment:
		text := strings.TrimSpace(*m.formCommentResult)
		if text == "" {
			return m, m.notifyErr(errEmptyComment)
		}
		brn := string(m.detail.BRN)
		// Stay on the Overview tab with the comment list expanded, so the
		// just-posted comment is immediately visible at the bottom.
		m.detailTab = 0
		m.detailVP.SetYOffset(0)
		m.commentsExpanded = true
		// Record it on the bead, and get it to the agent — running or not.
		// See deliverCommentCmd for why the stopped case matters as much as
		// the running one.
		//
		// Built before the return, not inside it: a return statement's
		// operands are evaluated left to right, so `return m, ...Cmd(...)`
		// would copy m before the call and silently discard every field the
		// call sets (liveBRN, and the tab switch on a restart).
		record := runTypedAction(func() (string, error) { return m.deps.Comment(brn, text) })
		deliver := m.deliverCommentCmd(brn, text)
		return m, tea.Batch(record, deliver)
	case formKindEditBead:
		title := strings.TrimSpace(*m.formTitleResult)
		if title == "" {
			return m, m.notifyErr(errEmptyTitle)
		}
		desc := strings.TrimSpace(*m.formDescResult)
		brn := string(m.detail.BRN)
		return m, runTypedAction(func() (string, error) { return m.deps.EditBead(brn, title, desc) })
	case formKindPersonaEdit:
		return m.submitPersonaEditForm()
	case formKindPersonaNew:
		return m.submitPersonaNewForm()
	case formKindPromptNewBead:
		title, desc, accept, typ, parent, tier, err := readNewBeadFormFields(m)
		if err != nil {
			return m, m.notifyErr(err)
		}
		return m, runTypedCommand(func() (string, error) {
			return m.deps.CreateBead(title, desc, accept, "", typ, parent, tier)
		})
	}
	return m, nil
}
