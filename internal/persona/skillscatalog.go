package persona

// SkillCatalogEntry is one well-known Claude Code Skill a persona's Skills
// field can reference by name — the TUI's Skills picker surfaces these
// (plus a free-text custom slot for anything not on this list) so
// attaching a known skill is a keypress instead of hand-typing a spec from
// memory. Name is what goes into Persona.Skills and what
// personaSkillExtras (internal/cli/persona_trigger.go) matches against to
// resolve a fetch.
type SkillCatalogEntry struct {
	Name        string
	Description string
}

// SkillCatalog is a static, bundled-in-source list — deliberately not
// fetched live from GitHub (confirmed with the user: no network dependency
// just to open the persona editor). Curated from github.com/anthropics/
// skills' current top-level skills (the official Anthropic repo, checked
// during this feature's own design), so it goes stale only as slowly as
// that repo's own top-level skill set changes and BARON gets updated to
// match — never wrong in a way that breaks anything, just possibly
// incomplete for a brand-new skill until then.
func SkillCatalog() []SkillCatalogEntry {
	return []SkillCatalogEntry{
		{"frontend-design", "Review and improve UI/frontend code for design quality"},
		{"skill-creator", "Scaffold a new Claude Code Skill"},
		{"pdf", "Read, create, and edit PDF documents"},
		{"docx", "Read, create, and edit Word documents"},
		{"xlsx", "Read, create, and edit Excel spreadsheets"},
		{"pptx", "Read, create, and edit PowerPoint presentations"},
		{"webapp-testing", "Drive and test a web app with Playwright"},
		{"mcp-builder", "Build a new MCP server"},
		{"canvas-design", "Visual/canvas-based design work"},
		{"web-artifacts-builder", "Build interactive web artifacts"},
		{"doc-coauthoring", "Collaboratively draft and edit documents"},
		{"theme-factory", "Generate and apply visual themes"},
		{"algorithmic-art", "Generate algorithmic/generative art"},
		{"brand-guidelines", "Apply a project's brand guidelines consistently"},
		{"internal-comms", "Draft internal communications (announcements, updates)"},
		{"slack-gif-creator", "Create GIFs for Slack"},
		{"claude-api", "Reference the Claude API / Anthropic SDK"},
	}
}

// KnownSkill reports whether name matches a SkillCatalog entry — the
// dividing line personaSkillExtras uses between "resolve via the catalog's
// own fetch mechanism (openskills)" and "treat as a raw npm spec or plugin
// URL, same as before this catalog existed."
func KnownSkill(name string) bool {
	for _, e := range SkillCatalog() {
		if e.Name == name {
			return true
		}
	}
	return false
}
