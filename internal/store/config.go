// Package store implements BARON's persistent configuration and audit storage.
package store

import (
	"github.com/baron-cli/baron/internal/agent"
)

// Config is the root BARON configuration. Field names map to TOML keys via
// the `toml` struct tags (snake_case in files, CamelCase in Go) and to JSON
// via the `json` tags (same snake_case keys).
type Config struct {
	General GeneralConfig `toml:"general" json:"general"`
	Gate    GateConfig    `toml:"gate" json:"gate"`
	Merge   MergeConfig   `toml:"merge" json:"merge"`
	TUI     TUIConfig     `toml:"tui" json:"tui"`
	Cost    CostConfig    `toml:"cost" json:"cost"`
	Audit   AuditConfig   `toml:"audit" json:"audit"`
	// Agents holds [[agents]] blocks: overrides applied on top of the
	// machine-wide agent discovery, and the documented no-code way to
	// register an agent CLI BARON ships no candidate for. They stay
	// project-local — the shared cache in ~/.cache/baron records only what
	// was probed on the machine.
	Agents []agent.ConfigAgent `toml:"agents" json:"agents"`
	// Profiles holds [profiles.<name>] sections; not yet consumed (schema alignment).
	Profiles map[string]Profile `toml:"profiles" json:"profiles"`
}

// GeneralConfig holds project identity settings. The canonical keys are
// project_name/default_profile; name/profile are shorthand aliases, synced on
// load (see overlay and applyEnv).
type GeneralConfig struct {
	Name                 string `toml:"name" json:"name"`
	Profile              string `toml:"profile" json:"profile"`
	BRNPrefix            string `toml:"brn_prefix" json:"brn_prefix"`
	ProjectName          string `toml:"project_name" json:"project_name"`
	DefaultModel         string `toml:"default_model" json:"default_model"`
	DefaultProfile       string `toml:"default_profile" json:"default_profile"`
	AutoStart            bool   `toml:"auto_start" json:"auto_start"`
	SilentDeathThreshold int    `toml:"silent_death_threshold" json:"silent_death_threshold"`
	HeartbeatInterval    int    `toml:"heartbeat_interval" json:"heartbeat_interval"`
	// BaseBranch is the branch PRs target and gitleaks diffs against
	// (e.g. "origin/main...HEAD").
	BaseBranch string `toml:"base_branch" json:"base_branch"`
}

// GateConfig holds gate execution settings.
type GateConfig struct {
	// Timeout is in minutes; domain.NewGateRunner converts to seconds,
	// matching profile.GateStep.Timeout's existing unit.
	Timeout     int            `toml:"timeout" json:"timeout"`
	FailFast    bool           `toml:"fail_fast" json:"fail_fast"`
	RetryBudget int            `toml:"retry_budget" json:"retry_budget"`
	Gitleaks    GitleaksConfig `toml:"gitleaks" json:"gitleaks"`
	// AutoRetry opts into the reconciler relaunching a bead it finds
	// sitting in "retry" on its own, instead of only surfacing it for a
	// human to relaunch. Off by default: unlike the reconciler's other
	// actions (fixing a stale status label), this spawns a real agent
	// process on a timer, and the retry budget it counts against is
	// derived from the audit log rather than a counter that survives a
	// process boundary, so it's a bigger trust step than the rest of
	// reconciliation.
	AutoRetry bool `toml:"auto_retry" json:"auto_retry"`
}

// GitleaksConfig controls secret scanning.
type GitleaksConfig struct {
	Enabled      bool   `toml:"enabled" json:"enabled"`
	ReportPath   string `toml:"report_path" json:"report_path"`
	Redact       bool   `toml:"redact" json:"redact"`
	IgnorePath   string `toml:"ignore_path" json:"ignore_path"`
	BaselinePath string `toml:"baseline_path" json:"baseline_path"`
}

// MergeConfig holds merge policy settings.
type MergeConfig struct {
	RequireHuman bool            `toml:"require_human" json:"require_human"`
	Auto         AutoMergeConfig `toml:"auto" json:"auto"`
}

// AutoMergeConfig controls automatic merging.
type AutoMergeConfig struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	// RequireTags lists the bd tags a bead must carry (any one of them) to
	// be eligible for auto-merge; empty means no tag restriction — every
	// bead that clears the other guardrails below qualifies (`baron init`'s
	// auto-merge prompt leaves this empty, so opting in needs no per-bead
	// tagging).
	RequireTags     []string `toml:"require_tags" json:"require_tags"`
	MaxChangedFiles int      `toml:"max_changed_files" json:"max_changed_files"`
	MaxDiffLines    int      `toml:"max_diff_lines" json:"max_diff_lines"`
	ForbidPaths     []string `toml:"forbid_paths" json:"forbid_paths"`
}

// TUIConfig holds terminal UI settings.
type TUIConfig struct {
	ShowCost      bool   `toml:"show_cost" json:"show_cost"`
	Theme         string `toml:"theme" json:"theme"`
	DefaultScreen string `toml:"default_screen" json:"default_screen"`
	PageSize      int    `toml:"page_size" json:"page_size"`
	Tmux          string `toml:"tmux" json:"tmux"` // "auto", "always", or "never"
	TmuxSession   string `toml:"tmux_session" json:"tmux_session"`
}

// CostConfig holds cost tracking settings.
type CostConfig struct {
	BudgetLimit  float64 `toml:"budget_limit" json:"budget_limit"`
	Currency     string  `toml:"currency" json:"currency"`
	ReportFormat string  `toml:"report_format" json:"report_format"`
	RunsPath     string  `toml:"runs_path" json:"runs_path"`
}

// AuditConfig holds audit log settings.
type AuditConfig struct {
	Path    string `toml:"path" json:"path"`
	Verbose bool   `toml:"verbose" json:"verbose"`
}

// Profile is a PRD-shaped gate profile from a [profiles.<name>] section: each
// stage is a command string. It is distinct from profile.Profile (which
// carries runtime-detected GateSteps); internal/cli's profileByName/
// profileFromStore is what actually consumes it, layering these fields (or
// Steps, for a fully custom pipeline) onto profile.Detect's auto-detected
// steps — see docs/PRD/harness-hardening.md §2.
type Profile struct {
	PackageManager string `toml:"package_manager,omitempty" json:"package_manager,omitempty"`
	Formatter      string `toml:"formatter,omitempty" json:"formatter,omitempty"`
	Linter         string `toml:"linter,omitempty" json:"linter,omitempty"`
	Typecheck      string `toml:"typecheck,omitempty" json:"typecheck,omitempty"`
	Test           string `toml:"test,omitempty" json:"test,omitempty"`
	Build          string `toml:"build,omitempty" json:"build,omitempty"`
	// Steps, when non-empty, fully replaces both the flat fields above and
	// profile.Detect's auto-detection for this profile: a completely custom
	// gate pipeline (e.g. for a language BARON has no built-in support for)
	// defined entirely in config.toml, no Go code required.
	Steps []GateStepConfig `toml:"steps,omitempty" json:"steps,omitempty"`
}

// GateStepConfig is one step of a fully custom gate pipeline
// ([[profiles.<name>.steps]]). Mirrors profile.GateStep's fields — kept as
// a separate type so internal/store has no dependency on internal/profile.
type GateStepConfig struct {
	Name    string   `toml:"name" json:"name"`
	Command string   `toml:"command" json:"command"`
	Args    []string `toml:"args,omitempty" json:"args,omitempty"`
	// Timeout in seconds; 0 means use the gate's own default.
	Timeout int `toml:"timeout,omitempty" json:"timeout,omitempty"`
}

// DefaultConfig returns the hardcoded defaults. Precedence is
// env > project > user > these defaults.
func DefaultConfig() *Config {
	return &Config{
		General: GeneralConfig{
			Profile:              "go",
			DefaultProfile:       "go",
			AutoStart:            true,
			SilentDeathThreshold: 30,
			HeartbeatInterval:    5,
			BaseBranch:           "main",
		},
		Gate: GateConfig{
			Timeout:     10,
			FailFast:    true,
			RetryBudget: 3,
			Gitleaks: GitleaksConfig{
				Enabled:    true,
				ReportPath: ".baron/gitleaks-report.json",
				Redact:     true,
				IgnorePath: ".gitleaksignore",
			},
		},
		Merge: MergeConfig{
			RequireHuman: true,
			Auto: AutoMergeConfig{
				Enabled: false,
			},
		},
		TUI: TUIConfig{
			ShowCost:      true,
			Theme:         "dark",
			DefaultScreen: "dashboard",
			PageSize:      20,
			Tmux:          "auto",
			TmuxSession:   "baron",
		},
		Cost: CostConfig{
			BudgetLimit:  0,
			Currency:     "USD",
			ReportFormat: "text",
			RunsPath:     ".baron/runs.jsonl",
		},
		Audit: AuditConfig{
			Path: ".baron/audit/events.jsonl",
		},
		Profiles: map[string]Profile{
			"go": {
				Formatter: "gofmt",
				Linter:    "go vet",
				Test:      "go test -count=1 ./...",
				Build:     "go build ./...",
			},
			"react-ts": {
				PackageManager: "auto",
				Formatter:      "prettier --check",
				Linter:         "eslint .",
				Typecheck:      "tsc --noEmit",
				Test:           "vitest run",
				Build:          "next build",
			},
		},
	}
}

// ProfilesDirName is the drop-in directory — under a project's .baron/, or
// alongside the user's config.toml — holding one TOML file per gate
// profile (e.g. .baron/profiles/rust.toml). Each file is a [profiles.<name>]
// section's body with the ".toml" stripped from its filename standing in
// for the section name; see UserProfilesDir/ProjectProfilesDir.
const ProfilesDirName = "profiles"

// ConfigFileName is BARON's config filename, at both the project
// (.baron/config.toml) and user (~/.config/baron/config.toml) layers.
const ConfigFileName = "config.toml"

// UserConfigDir returns ~/.config/baron, the root of BARON's per-user
// (machine-wide, cross-project) config: config.toml and its profiles/
// drop-in directory both live here.
