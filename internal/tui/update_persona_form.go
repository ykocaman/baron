package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/persona"
)

func personaEventOptions() []huh.Option[string] {
	opts := make([]huh.Option[string], len(personaEventPresetStates))
	for i, s := range personaEventPresetStates {
		opts[i] = huh.NewOption(string(s), string(s))
	}
	return opts
}

// splitPersonaEventRules separates on into the preset-selectable "any ->
// state" rules the eventsField multi-select can show as checked (the
// common case — see personaEventPresetStates) from any rarer custom rule:
// a specific (non-"*") From, or a To outside the preset list entirely.
// personaEventsText re-joins both back into ParseTransitionRules' expected
// text at submit time, so a custom rule picked up outside the TUI rides
// through Edit -> submit unchanged instead of silently vanishing the
// moment someone who didn't even touch it hits enter.
func splitPersonaEventRules(on []persona.TransitionRule) (presetTo []string, custom []persona.TransitionRule) {
	valid := make(map[string]bool, len(personaEventPresetStates))
	for _, s := range personaEventPresetStates {
		valid[string(s)] = true
	}
	for _, r := range on {
		if r.From == "*" && valid[r.To] {
			presetTo = append(presetTo, r.To)
			continue
		}
		custom = append(custom, r)
	}
	return presetTo, custom
}

// personaEventsText re-joins a preset selection plus any custom rules string
// into the "from->to, from->to" text persona.ParseTransitionRules reads.
func personaEventsText(selected []string, customStr string) string {
	sel := make(map[string]bool, len(selected))
	for _, s := range selected {
		sel[s] = true
	}
	var parts []string
	for _, s := range personaEventPresetStates {
		if sel[string(s)] {
			parts = append(parts, "*->"+string(s))
		}
	}
	customStr = strings.TrimSpace(customStr)
	if customStr != "" {
		parts = append(parts, customStr)
	}
	return strings.Join(parts, ", ")
}

// personaFormValues holds the writable locals openPersonaEditForm and
// openPersonaNewForm each declare and bind into fields via
// personaCommonFields, then read back into the Model's formPersonaXxxResult
// pointers after submit.
type personaFormValues struct {
	prompt, desc, schedule, eventsCustomStr, skills, tier, agent, bdWrite, enabled *string
	eventsSelected, bdActions                                                      *[]string
}

// personaCommonFields builds the 11 fields shared by openPersonaEditForm and
// openPersonaNewForm — Prompt through Enabled. The New form prepends its own
// ID/Name fields ahead of this slice.
func personaCommonFields(m Model, v *personaFormValues) []huh.Field {
	return []huh.Field{
		huh.NewText().
			Title("Prompt").
			Description("Instruction sent to the agent").
			CharLimit(4000).
			Lines(4).
			Value(v.prompt),
		huh.NewInput().
			Title(fieldLabelDescription).
			Description("Roster label").
			CharLimit(200).
			Value(v.desc),
		huh.NewInput().
			Title("Cron schedule").
			Description("Empty = no time trigger").
			CharLimit(60).
			Value(v.schedule),
		huh.NewMultiSelect[string]().
			Title("Fires when a bead reaches").
			Description("space toggles, / searches").
			Options(personaEventOptions()...).
			Filterable(true).
			Height(5).
			Value(v.eventsSelected),
		huh.NewInput().
			Title("Custom event rules (optional)").
			Description("e.g. working->retry").
			CharLimit(200).
			Value(v.eventsCustomStr),
		huh.NewInput().
			Title("Skills").
			Description("comma-separated skill names").
			CharLimit(300).
			Value(v.skills),
		huh.NewSelect[string]().
			Title("Model Tier").
			Options(m.newBeadFormTierOptions()...).
			Value(v.tier),
		huh.NewSelect[string]().
			Title("Agent CLI").
			Options(huh.NewOption("auto (fallback/any)", ""), huh.NewOption("agy", "agy"), huh.NewOption("opencode", "opencode"), huh.NewOption("claude", "claude"), huh.NewOption("gemini", "gemini"), huh.NewOption("codex", "codex")).
			Value(v.agent),
		huh.NewSelect[string]().
			Title("BD Write Authority").
			Options(huh.NewOption("isolated (read-only)", "isolated"), huh.NewOption("allowed (read/write)", "allowed")).
			Value(v.bdWrite),
		huh.NewMultiSelect[string]().
			Title("Allowed BD Actions").
			Options(huh.NewOption("comment", "comment"), huh.NewOption("reopen", "reopen"), huh.NewOption("close", "close"), huh.NewOption("create", "create")).
			Height(4).
			Value(v.bdActions),
		huh.NewSelect[string]().
			Title("Enabled").
			Options(huh.NewOption("off", "off"), huh.NewOption("on", "on")).
			Value(v.enabled),
	}
}

// openPersonaEditForm builds formKindPersonaEdit's huh.Form for p — Prompt,
// Description, cron schedule, event triggers, Skills, Model Tier, Agent,
// BD write, and enabled.
func (m Model) openPersonaEditForm(p persona.Persona) tea.Model {
	m.editingPersonaID = p.ID
	m.editingPersonaName = p.Name
	m.formReturnScreen = screenCrew
	m.formKind = formKindPersonaEdit

	prompt := p.Prompt
	desc := p.Description
	schedule := p.Trigger.Schedule
	eventsSelected, eventsCustom := splitPersonaEventRules(p.Trigger.On)
	eventsCustomStr := formatPersonaCustomRules(eventsCustom)
	skills := p.SkillsText()
	tier := p.Model.Tier
	if tier == "" {
		tier = "standard"
	}
	agent := p.Model.Agent
	bdWrite := "isolated"
	if p.Authority.BDWrite {
		bdWrite = "allowed"
	}
	bdActions := p.Authority.Actions
	enabled := "off"
	if p.Enabled {
		enabled = "on"
	}

	v := &personaFormValues{
		prompt: &prompt, desc: &desc, schedule: &schedule,
		eventsSelected: &eventsSelected, eventsCustomStr: &eventsCustomStr,
		skills: &skills, tier: &tier, agent: &agent, bdWrite: &bdWrite,
		bdActions: &bdActions, enabled: &enabled,
	}

	form := newOverlayForm(huh.NewGroup(personaCommonFields(m, v)...))
	m.beadForm = initHuhForm(form)
	m.formPersonaPromptResult = &prompt
	m.formDescResult = &desc
	m.formPersonaScheduleResult = &schedule
	m.formPersonaEventsResult = &eventsSelected
	m.formPersonaEventsCustom = eventsCustom
	m.formPersonaEventsCustomStr = &eventsCustomStr
	m.formPersonaSkillsResult = &skills
	m.formPersonaTierResult = &tier
	m.formPersonaAgentResult = &agent
	m.formPersonaBDWriteResult = &bdWrite
	m.formPersonaBDActionsResult = &bdActions
	m.formPersonaEnabledResult = &enabled
	m.screen = screenForm
	return m
}

// formatPersonaCustomRules renders custom transition rules back to a comma-separated string
func formatPersonaCustomRules(custom []persona.TransitionRule) string {
	var parts []string
	for _, r := range custom {
		parts = append(parts, r.From+"->"+r.To)
	}
	return strings.Join(parts, ", ")
}

// openPersonaNewForm builds formKindPersonaNew's huh.Form — the Personas
// tab's 'n', the only persona-create path in the app.
func (m Model) openPersonaNewForm() tea.Model {
	if m.deps.CreatePersona == nil {
		return m
	}
	m.formReturnScreen = screenCrew
	m.formKind = formKindPersonaNew

	id, name, prompt := "", "", ""
	desc, schedule, skills := "", "", ""
	var eventsSelected []string
	eventsCustomStr := ""
	tier := "standard"
	agent := ""
	bdWrite := "isolated"
	var bdActions []string
	enabled := "off"

	idField := huh.NewInput().
		Title("ID").
		Description("leave blank to auto-generate").
		CharLimit(60).
		Value(&id)
	nameField := huh.NewInput().
		Title("Name").
		CharLimit(80).
		Value(&name)

	v := &personaFormValues{
		prompt: &prompt, desc: &desc, schedule: &schedule,
		eventsSelected: &eventsSelected, eventsCustomStr: &eventsCustomStr,
		skills: &skills, tier: &tier, agent: &agent, bdWrite: &bdWrite,
		bdActions: &bdActions, enabled: &enabled,
	}
	fields := append([]huh.Field{idField, nameField}, personaCommonFields(m, v)...)

	form := newOverlayForm(huh.NewGroup(fields...))
	m.beadForm = initHuhForm(form)
	m.formPersonaIDResult = &id
	m.formPersonaNameResult = &name
	m.formPersonaPromptResult = &prompt
	m.formDescResult = &desc
	m.formPersonaScheduleResult = &schedule
	m.formPersonaEventsResult = &eventsSelected
	m.formPersonaEventsCustom = nil
	m.formPersonaEventsCustomStr = &eventsCustomStr
	m.formPersonaSkillsResult = &skills
	m.formPersonaTierResult = &tier
	m.formPersonaAgentResult = &agent
	m.formPersonaBDWriteResult = &bdWrite
	m.formPersonaBDActionsResult = &bdActions
	m.formPersonaEnabledResult = &enabled
	m.screen = screenForm
	return m
}

// openPromptNewBeadForm builds the Beads tab's 'n' — same field set as
// Board Mode's own inline 'n' (updateSplitKey) but NOT calling into that
// code: it hardcodes m.formReturnScreen = screenDashboard and reads
// m.listSelection() (Board-Mode-cursor-coupled), neither of which Prompt
// Mode may touch or rely on. formKindPromptNewBead (not formKindNewBead)
// keeps this from arming Board Mode's own pendingNewBead jump.
