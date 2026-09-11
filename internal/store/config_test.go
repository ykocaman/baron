package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_defaults(t *testing.T) {
	cfg, err := loadConfig(t.TempDir(), filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := DefaultConfig()
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("defaults mismatch:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadConfig_precedence(t *testing.T) {
	projectDir := t.TempDir()
	userPath := filepath.Join(t.TempDir(), "config.toml")

	// user config sets profile and timeout
	writeConfig(t, userPath, `
[general]
profile = "user-profile"

[gate]
timeout = 100
`)
	// project config overrides profile only; timeout stays from user
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
profile = "proj-profile"
`)

	cfg, err := loadConfig(projectDir, userPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.Profile != "proj-profile" {
		t.Errorf("project should override user profile, got %q", cfg.General.Profile)
	}
	if cfg.Gate.Timeout != 100 {
		t.Errorf("user timeout should survive, got %d", cfg.Gate.Timeout)
	}
	if cfg.Gate.Gitleaks.Enabled != true {
		t.Errorf("default gitleaks.enabled should survive, got %v", cfg.Gate.Gitleaks.Enabled)
	}
}

func TestLoadConfig_envOverridesAll(t *testing.T) {
	projectDir := t.TempDir()
	userPath := filepath.Join(t.TempDir(), "config.toml")

	writeConfig(t, userPath, `[general]
profile = "user-profile"
`)
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `[general]
profile = "proj-profile"
`)

	t.Setenv("BARON_GENERAL_PROFILE", "env-profile")
	t.Setenv("BARON_GATE_TIMEOUT", "600")
	t.Setenv("BARON_MERGE_AUTO_ENABLED", "true")
	t.Setenv("BARON_COST_BUDGET_LIMIT", "12.5")

	cfg, err := loadConfig(projectDir, userPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.Profile != "env-profile" {
		t.Errorf("env should override profile, got %q", cfg.General.Profile)
	}
	if cfg.Gate.Timeout != 600 {
		t.Errorf("env should override timeout, got %d", cfg.Gate.Timeout)
	}
	if !cfg.Merge.Auto.Enabled {
		t.Errorf("env should enable merge.auto, got %v", cfg.Merge.Auto.Enabled)
	}
	if cfg.Cost.BudgetLimit != 12.5 {
		t.Errorf("env should set budget limit, got %v", cfg.Cost.BudgetLimit)
	}
}

func TestLoadConfig_envInvalidValue(t *testing.T) {
	t.Setenv("BARON_GATE_TIMEOUT", "not-a-number")
	if _, err := loadConfig(t.TempDir(), filepath.Join(t.TempDir(), "config.toml")); err == nil {
		t.Fatal("expected error for invalid env value")
	}
}

func TestLoadConfig_retryBudgetTooHighRejected(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[gate]
retry_budget = 5
`)
	if _, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml")); err == nil {
		t.Fatal("loadConfig() with retry_budget=5: want an error, got nil")
	}
}

func TestLoadConfig_retryBudgetAtCapAccepted(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[gate]
retry_budget = 3
`)
	if _, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml")); err != nil {
		t.Fatalf("loadConfig() with retry_budget=3 (the cap): unexpected error: %v", err)
	}
}

func TestConfigValidate_negativeRetryBudgetRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Gate.RetryBudget = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() with retry_budget=-1: want an error, got nil")
	}
}

func TestLoadConfig_unknownFields(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
profile = "go"
bogus_key = "ignored"

[gate]
timeout = 300
unknown_section = 1
`)
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("unknown fields should not fail, got %v", err)
	}
	if cfg.General.Profile != "go" {
		t.Errorf("known fields should still load, got %q", cfg.General.Profile)
	}
}

func TestLoadConfig_missingFieldsFillDefaults(t *testing.T) {
	projectDir := t.TempDir()
	// partial TOML: only general.profile set
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
profile = "react-ts"
`)
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.Profile != "react-ts" {
		t.Errorf("profile mismatch, got %q", cfg.General.Profile)
	}
	if cfg.Gate.Timeout != 10 {
		t.Errorf("missing gate.timeout should default to 10, got %d", cfg.Gate.Timeout)
	}
	if !cfg.Gate.Gitleaks.Enabled {
		t.Errorf("missing gitleaks.enabled should default to true")
	}
	if cfg.Audit.Path != ".baron/audit/events.jsonl" {
		t.Errorf("missing audit.path should default, got %q", cfg.Audit.Path)
	}
}

func TestLoadConfig_explicitFalseWins(t *testing.T) {
	projectDir := t.TempDir()
	// explicit false must override the true default
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[gate]
fail_fast = false

[gate.gitleaks]
enabled = false

[tui]
show_cost = false
`)
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Gate.FailFast {
		t.Error("explicit fail_fast=false should win over default")
	}
	if cfg.Gate.Gitleaks.Enabled {
		t.Error("explicit gitleaks.enabled=false should win over default")
	}
	if cfg.TUI.ShowCost {
		t.Error("explicit show_cost=false should win over default")
	}
}

func TestSetConfigField_roundTrip(t *testing.T) {
	projectDir := t.TempDir()

	if err := SetConfigField(projectDir, "merge.auto.enabled", "true"); err != nil {
		t.Fatalf("SetConfigField: %v", err)
	}
	if err := SetConfigField(projectDir, "general.profile", "react-ts"); err != nil {
		t.Fatalf("SetConfigField: %v", err)
	}
	if err := SetConfigField(projectDir, "cost.budget_limit", "42.5"); err != nil {
		t.Fatalf("SetConfigField: %v", err)
	}

	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.Merge.Auto.Enabled {
		t.Error("merge.auto.enabled should be true after set")
	}
	if cfg.General.Profile != "react-ts" {
		t.Errorf("profile mismatch, got %q", cfg.General.Profile)
	}
	if cfg.Cost.BudgetLimit != 42.5 {
		t.Errorf("budget limit mismatch, got %v", cfg.Cost.BudgetLimit)
	}
	if cfg.Gate.Timeout != 10 {
		t.Errorf("untouched gate.timeout should stay default, got %d", cfg.Gate.Timeout)
	}
}

func TestSetConfigField_preservesExisting(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
profile = "go"

[gate]
timeout = 500
`)
	if err := SetConfigField(projectDir, "general.profile", "react-ts"); err != nil {
		t.Fatalf("SetConfigField: %v", err)
	}
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.Profile != "react-ts" {
		t.Errorf("profile mismatch, got %q", cfg.General.Profile)
	}
	if cfg.Gate.Timeout != 500 {
		t.Errorf("existing gate.timeout should be preserved, got %d", cfg.Gate.Timeout)
	}
}

func TestSetConfigField_unknownField(t *testing.T) {
	if err := SetConfigField(t.TempDir(), "bogus.field", "x"); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestSetConfigField_invalidValue(t *testing.T) {
	if err := SetConfigField(t.TempDir(), "gate.timeout", "abc"); err == nil {
		t.Fatal("expected error for invalid int value")
	}
}

// loadPRDFieldsFixture writes the shared PRD-fields TOML fixture (one of
// every field group TestLoadConfig_prdFields' sibling tests each check a
// slice of) and loads it — split out so each sibling test doesn't repeat the
// same fixture text.
func loadPRDFieldsFixture(t *testing.T) *Config {
	t.Helper()
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
project_name = "demo"
default_model = "claude"
default_profile = "react-ts"
auto_start = false
silent_death_threshold = 45
heartbeat_interval = 10

[gate]
retry_budget = 2

[gate.gitleaks]
report_path = ".baron/gitleaks.json"
redact = false
ignore_path = ".baron/ignore"
baseline_path = ".baron/baseline"

[merge.auto]
require_tags = ["critical", "hotfix"]
max_changed_files = 5
max_diff_lines = 1000
forbid_paths = ["vendor/", "*.lock"]

[tui]
theme = "light"
default_screen = "queue"
page_size = 50
tmux = "always"
tmux_session = "work"

[cost]
currency = "EUR"
report_format = "json"

[audit]
verbose = true

[[agents]]
name = "opencode"
command = "opencode"
tags = ["code"]

[profiles.custom]
formatter = "my-fmt"
linter = "my-lint"
typecheck = "my-typecheck"
test = "my-test"
build = "my-build"
`)
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg
}

func TestLoadConfig_generalFields(t *testing.T) {
	cfg := loadPRDFieldsFixture(t)
	if cfg.General.ProjectName != "demo" || cfg.General.Name != "demo" {
		t.Errorf("project_name should sync to name, got project=%q name=%q", cfg.General.ProjectName, cfg.General.Name)
	}
	if cfg.General.DefaultModel != "claude" {
		t.Errorf("default_model mismatch, got %q", cfg.General.DefaultModel)
	}
	if cfg.General.DefaultProfile != "react-ts" || cfg.General.Profile != "react-ts" {
		t.Errorf("default_profile should sync to profile, got default=%q profile=%q", cfg.General.DefaultProfile, cfg.General.Profile)
	}
	if cfg.General.AutoStart {
		t.Error("explicit auto_start=false should win over default")
	}
	if cfg.General.SilentDeathThreshold != 45 || cfg.General.HeartbeatInterval != 10 {
		t.Errorf("thresholds mismatch: %d/%d", cfg.General.SilentDeathThreshold, cfg.General.HeartbeatInterval)
	}
}

func TestLoadConfig_gateAndGitleaksFields(t *testing.T) {
	cfg := loadPRDFieldsFixture(t)
	if cfg.Gate.RetryBudget != 2 {
		t.Errorf("retry_budget mismatch, got %d", cfg.Gate.RetryBudget)
	}
	g := cfg.Gate.Gitleaks
	if g.ReportPath != ".baron/gitleaks.json" || g.Redact || g.IgnorePath != ".baron/ignore" || g.BaselinePath != ".baron/baseline" {
		t.Errorf("gitleaks mismatch: %+v", g)
	}
}

func TestLoadConfig_mergeAutoFields(t *testing.T) {
	cfg := loadPRDFieldsFixture(t)
	a := cfg.Merge.Auto
	if len(a.RequireTags) != 2 || a.RequireTags[0] != "critical" {
		t.Errorf("require_tags mismatch, got %v", a.RequireTags)
	}
	if a.MaxChangedFiles != 5 || a.MaxDiffLines != 1000 {
		t.Errorf("merge limits mismatch: %+v", a)
	}
	if len(a.ForbidPaths) != 2 || a.ForbidPaths[1] != "*.lock" {
		t.Errorf("forbid_paths mismatch, got %v", a.ForbidPaths)
	}
}

func TestLoadConfig_tuiCostAuditFields(t *testing.T) {
	cfg := loadPRDFieldsFixture(t)
	if cfg.TUI.Theme != "light" || cfg.TUI.DefaultScreen != "queue" || cfg.TUI.PageSize != 50 ||
		cfg.TUI.Tmux != "always" || cfg.TUI.TmuxSession != "work" {
		t.Errorf("tui mismatch: %+v", cfg.TUI)
	}
	if cfg.Cost.Currency != "EUR" || cfg.Cost.ReportFormat != "json" {
		t.Errorf("cost mismatch: %+v", cfg.Cost)
	}
	if !cfg.Audit.Verbose {
		t.Error("audit.verbose should be true")
	}
}

func TestLoadConfig_agentsAndProfiles(t *testing.T) {
	cfg := loadPRDFieldsFixture(t)
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "opencode" || cfg.Agents[0].Command != "opencode" {
		t.Errorf("agents mismatch: %+v", cfg.Agents)
	}
	if p := cfg.Profiles["custom"]; p.Formatter != "my-fmt" || p.Test != "my-test" || p.Build != "my-build" {
		t.Errorf("profiles.custom mismatch: %+v", p)
	}
	if _, ok := cfg.Profiles["go"]; !ok {
		t.Error("default go profile should survive when a file defines other profiles")
	}
}

func TestLoadConfig_legacyKeysSync(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[general]
name = "legacy-name"
profile = "legacy-profile"
`)
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.ProjectName != "legacy-name" {
		t.Errorf("name should sync to project_name, got %q", cfg.General.ProjectName)
	}
	if cfg.General.DefaultProfile != "legacy-profile" {
		t.Errorf("profile should sync to default_profile, got %q", cfg.General.DefaultProfile)
	}
}

func TestLoadConfig_envNewFields(t *testing.T) {
	t.Setenv("BARON_GENERAL_DEFAULT_MODEL", "codex")
	t.Setenv("BARON_GENERAL_AUTO_START", "false")
	t.Setenv("BARON_GENERAL_SILENT_DEATH_THRESHOLD", "60")
	t.Setenv("BARON_GATE_RETRY_BUDGET", "1")
	t.Setenv("BARON_TUI_THEME", "light")
	t.Setenv("BARON_TUI_TMUX", "never")
	t.Setenv("BARON_COST_CURRENCY", "EUR")
	t.Setenv("BARON_AUDIT_VERBOSE", "true")

	cfg, err := loadConfig(t.TempDir(), filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.DefaultModel != "codex" || cfg.General.AutoStart {
		t.Errorf("general env mismatch: %+v", cfg.General)
	}
	if cfg.General.SilentDeathThreshold != 60 {
		t.Errorf("silent_death_threshold env mismatch, got %d", cfg.General.SilentDeathThreshold)
	}
	if cfg.Gate.RetryBudget != 1 {
		t.Errorf("retry_budget env mismatch, got %d", cfg.Gate.RetryBudget)
	}
	if cfg.TUI.Theme != "light" || cfg.TUI.Tmux != "never" {
		t.Errorf("tui env mismatch: %+v", cfg.TUI)
	}
	if cfg.Cost.Currency != "EUR" {
		t.Errorf("currency env mismatch, got %q", cfg.Cost.Currency)
	}
	if !cfg.Audit.Verbose {
		t.Error("audit.verbose env should be true")
	}
}

func TestLoadConfig_envAliasSync(t *testing.T) {
	t.Setenv("BARON_GENERAL_PROFILE", "env-profile")
	cfg, err := loadConfig(t.TempDir(), filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.General.DefaultProfile != "env-profile" {
		t.Errorf("BARON_GENERAL_PROFILE should sync default_profile, got %q", cfg.General.DefaultProfile)
	}
}

func TestSetConfigField_prdFields(t *testing.T) {
	projectDir := t.TempDir()
	if err := SetConfigField(projectDir, "gate.retry_budget", "2"); err != nil {
		t.Fatalf("SetConfigField retry_budget: %v", err)
	}
	if err := SetConfigField(projectDir, "merge.auto.require_tags", `["critical", "hotfix"]`); err != nil {
		t.Fatalf("SetConfigField require_tags: %v", err)
	}
	if err := SetConfigField(projectDir, "tui.tmux", "always"); err != nil {
		t.Fatalf("SetConfigField tmux: %v", err)
	}
	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Gate.RetryBudget != 2 {
		t.Errorf("retry_budget mismatch, got %d", cfg.Gate.RetryBudget)
	}
	if len(cfg.Merge.Auto.RequireTags) != 2 || cfg.Merge.Auto.RequireTags[1] != "hotfix" {
		t.Errorf("require_tags mismatch, got %v", cfg.Merge.Auto.RequireTags)
	}
	if cfg.TUI.Tmux != "always" {
		t.Errorf("tmux mismatch, got %q", cfg.TUI.Tmux)
	}
	if cfg.Gate.Timeout != 10 || cfg.TUI.Theme != "dark" {
		t.Errorf("untouched defaults should survive, got timeout=%d theme=%q", cfg.Gate.Timeout, cfg.TUI.Theme)
	}
}

func TestSetConfigField_invalidListValue(t *testing.T) {
	if err := SetConfigField(t.TempDir(), "merge.auto.require_tags", "critical"); err == nil {
		t.Fatal("expected error for non-JSON list value")
	}
}

// TestLoadConfig_profilesDirMerged: a drop-in file in the project's
// .baron/profiles/ directory becomes a usable [profiles.<name>] entry,
// named after the file, with no config.toml section for it at all.
func TestLoadConfig_profilesDirMerged(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(ProjectProfilesDir(projectDir), "rust.toml"), `
[[steps]]
name = "test"
command = "cargo"
args = ["test"]
`)

	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	p, ok := cfg.Profiles["rust"]
	if !ok {
		t.Fatalf("Profiles = %+v, want a \"rust\" entry from the drop-in file", cfg.Profiles)
	}
	if len(p.Steps) != 1 || p.Steps[0].Command != "cargo" {
		t.Errorf("rust profile = %+v, want the drop-in file's single cargo test step", p.Steps)
	}
}

// TestLoadConfig_profilesDirProjectOverridesUser: the project's own
// drop-in wins over a same-named one in the user's machine-wide directory.
func TestLoadConfig_profilesDirProjectOverridesUser(t *testing.T) {
	projectDir := t.TempDir()
	userPath := filepath.Join(t.TempDir(), "config.toml")

	writeConfig(t, filepath.Join(filepath.Dir(userPath), ProfilesDirName, "rust.toml"), `
[[steps]]
name = "test"
command = "user-cargo"
`)
	writeConfig(t, filepath.Join(ProjectProfilesDir(projectDir), "rust.toml"), `
[[steps]]
name = "test"
command = "project-cargo"
`)

	cfg, err := loadConfig(projectDir, userPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Profiles["rust"].Steps[0].Command; got != "project-cargo" {
		t.Errorf("rust profile command = %q, want the project drop-in to win (project-cargo)", got)
	}
}

// TestLoadConfig_configTomlOverridesProfilesDir: config.toml's own
// [profiles.<name>] section is the more deliberate edit — it wins over a
// same-named drop-in file in the same directory tier.
func TestLoadConfig_configTomlOverridesProfilesDir(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(ProjectProfilesDir(projectDir), "rust.toml"), `
[[steps]]
name = "test"
command = "from-dropin"
`)
	writeConfig(t, filepath.Join(projectDir, ".baron", "config.toml"), `
[profiles.rust]
[[profiles.rust.steps]]
name = "test"
command = "from-config-toml"
`)

	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.Profiles["rust"].Steps[0].Command; got != "from-config-toml" {
		t.Errorf("rust profile command = %q, want config.toml's section to win (from-config-toml)", got)
	}
}

// TestLoadConfig_profilesDirIgnoresNonToml: only *.toml files are treated
// as profile drop-ins; anything else in the directory is left alone.
func TestLoadConfig_profilesDirIgnoresNonToml(t *testing.T) {
	projectDir := t.TempDir()
	writeConfig(t, filepath.Join(ProjectProfilesDir(projectDir), "README.md"), "not a profile\n")

	cfg, err := loadConfig(projectDir, filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if _, ok := cfg.Profiles["README"]; ok {
		t.Errorf("Profiles = %+v, want README.md ignored", cfg.Profiles)
	}
}

func TestWriteProfileFile_createsFile(t *testing.T) {
	dir := t.TempDir()
	p := Profile{Steps: []GateStepConfig{{Name: "test", Command: "cargo", Args: []string{"test"}}}}

	if err := WriteProfileFile(dir, "rust", p); err != nil {
		t.Fatalf("WriteProfileFile: %v", err)
	}
	path := filepath.Join(dir, "rust.toml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rust.toml not written: %v", err)
	}

	var got Profile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(data), &got); err != nil {
		t.Fatalf("decode written file: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Command != "cargo" {
		t.Errorf("round-tripped profile = %+v, want the cargo test step", got.Steps)
	}
}

// TestWriteProfileFile_neverOverwrites: a second export must not clobber
// what the user may have since edited by hand.
func TestWriteProfileFile_neverOverwrites(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, "rust.toml"), `
[[steps]]
name = "test"
command = "hand-edited"
`)

	err := WriteProfileFile(dir, "rust", Profile{Steps: []GateStepConfig{{Name: "test", Command: "generated"}}})
	if err != nil {
		t.Fatalf("WriteProfileFile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "rust.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var got Profile
	if _, err := toml.Decode(string(data), &got); err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Command != "hand-edited" {
		t.Errorf("command = %q, want the hand-edited file left untouched", got.Steps[0].Command)
	}
}
