// Package store implements BARON's persistent configuration and audit storage.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// SetConfigField sets a dot-notation config field (e.g. "merge.auto.enabled")
// in the project config file, creating it from defaults if absent. The file is
// rewritten atomically.
//
// ponytail: BurntSushi/toml's encoder does not preserve comments on rewrite;
// a hand-edited file's comments are lost when a field is set. Acceptable for
// now — revisit if comment preservation becomes a requirement.
func SetConfigField(projectDir, field, value string) error {
	projPath := projectConfigPath(projectDir)
	cfg := DefaultConfig()
	if data, err := os.ReadFile(projPath); err == nil {
		if _, err := toml.Decode(string(data), cfg); err != nil {
			return fmt.Errorf("decode %s: %w", projPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", projPath, err)
	}

	if err := setField(cfg, field, value); err != nil {
		return err
	}
	syncAliases(cfg, field)

	dir := filepath.Dir(projPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "config-*.toml")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// cleanup removes the temp file when we bail before the rename.
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		cleanup()
		return fmt.Errorf("encode %s: %w", projPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, projPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s: %w", projPath, err)
	}
	return nil
}

// syncAliases keeps the shorthand keys (name/profile) and the canonical
// keys (project_name/default_profile) in lockstep after a field set, mirroring
// the overlay aliasing on load. Both spellings stay valid in config files.
func syncAliases(cfg *Config, field string) {
	switch field {
	case "general.name":
		cfg.General.ProjectName = cfg.General.Name
	case "general.project_name":
		cfg.General.Name = cfg.General.ProjectName
	case "general.profile":
		cfg.General.DefaultProfile = cfg.General.Profile
	case "general.default_profile":
		cfg.General.Profile = cfg.General.DefaultProfile
	}
}

// configField describes one leaf Config field controllable via env var
// and/or config.toml overlay — the single source of truth applyEnv
// (config_env.go) and overlay (config_overlay.go) both walk instead of each
// hand-listing the same ~30 fields separately. A field added to configFields
// automatically gains both env-var and overlay support; one left out of
// either list used to be possible and silent (no compile error, it just
// didn't respond to env vars, or got dropped on overlay).
type configField struct {
	// tomlPath is the field's toml-tag path (e.g. ["gate", "gitleaks",
	// "enabled"]) — walked by fieldAtPath and checked via
	// toml.MetaData.IsDefined for overlay.
	tomlPath []string
	// env is the BARON_<SECTION>_<KEY> variable name, or "" for a field with
	// no env override (base_branch, auto_retry, the two path lists,
	// runs_path).
	env string
}

var configFields = []configField{
	{tomlPath: []string{"general", "name"}, env: "BARON_GENERAL_NAME"},
	{tomlPath: []string{"general", "project_name"}, env: "BARON_GENERAL_PROJECT_NAME"},
	{tomlPath: []string{"general", "profile"}, env: "BARON_GENERAL_PROFILE"},
	{tomlPath: []string{"general", "default_profile"}, env: "BARON_GENERAL_DEFAULT_PROFILE"},
	{tomlPath: []string{"general", "brn_prefix"}, env: "BARON_GENERAL_BRN_PREFIX"},
	{tomlPath: []string{"general", "default_model"}, env: "BARON_GENERAL_DEFAULT_MODEL"},
	{tomlPath: []string{"general", "auto_start"}, env: "BARON_GENERAL_AUTO_START"},
	{tomlPath: []string{"general", "silent_death_threshold"}, env: "BARON_GENERAL_SILENT_DEATH_THRESHOLD"},
	{tomlPath: []string{"general", "heartbeat_interval"}, env: "BARON_GENERAL_HEARTBEAT_INTERVAL"},
	{tomlPath: []string{"general", "base_branch"}},
	{tomlPath: []string{"gate", "timeout"}, env: "BARON_GATE_TIMEOUT"},
	{tomlPath: []string{"gate", "fail_fast"}, env: "BARON_GATE_FAIL_FAST"},
	{tomlPath: []string{"gate", "retry_budget"}, env: "BARON_GATE_RETRY_BUDGET"},
	{tomlPath: []string{"gate", "auto_retry"}},
	{tomlPath: []string{"gate", "gitleaks", "enabled"}, env: "BARON_GATE_GITLEAKS_ENABLED"},
	{tomlPath: []string{"gate", "gitleaks", "report_path"}, env: "BARON_GATE_GITLEAKS_REPORT_PATH"},
	{tomlPath: []string{"gate", "gitleaks", "redact"}, env: "BARON_GATE_GITLEAKS_REDACT"},
	{tomlPath: []string{"gate", "gitleaks", "ignore_path"}, env: "BARON_GATE_GITLEAKS_IGNORE_PATH"},
	{tomlPath: []string{"gate", "gitleaks", "baseline_path"}, env: "BARON_GATE_GITLEAKS_BASELINE_PATH"},
	{tomlPath: []string{"merge", "require_human"}, env: "BARON_MERGE_REQUIRE_HUMAN"},
	{tomlPath: []string{"merge", "auto", "enabled"}, env: "BARON_MERGE_AUTO_ENABLED"},
	{tomlPath: []string{"merge", "auto", "require_tags"}},
	{tomlPath: []string{"merge", "auto", "max_changed_files"}, env: "BARON_MERGE_AUTO_MAX_CHANGED_FILES"},
	{tomlPath: []string{"merge", "auto", "max_diff_lines"}, env: "BARON_MERGE_AUTO_MAX_DIFF_LINES"},
	{tomlPath: []string{"merge", "auto", "forbid_paths"}},
	{tomlPath: []string{"tui", "show_cost"}, env: "BARON_TUI_SHOW_COST"},
	{tomlPath: []string{"tui", "theme"}, env: "BARON_TUI_THEME"},
	{tomlPath: []string{"tui", "default_screen"}, env: "BARON_TUI_DEFAULT_SCREEN"},
	{tomlPath: []string{"tui", "page_size"}, env: "BARON_TUI_PAGE_SIZE"},
	{tomlPath: []string{"tui", "tmux"}, env: "BARON_TUI_TMUX"},
	{tomlPath: []string{"tui", "tmux_session"}, env: "BARON_TUI_TMUX_SESSION"},
	{tomlPath: []string{"cost", "budget_limit"}, env: "BARON_COST_BUDGET_LIMIT"},
	{tomlPath: []string{"cost", "currency"}, env: "BARON_COST_CURRENCY"},
	{tomlPath: []string{"cost", "report_format"}, env: "BARON_COST_REPORT_FORMAT"},
	{tomlPath: []string{"cost", "runs_path"}},
	{tomlPath: []string{"audit", "path"}, env: "BARON_AUDIT_PATH"},
	{tomlPath: []string{"audit", "verbose"}, env: "BARON_AUDIT_VERBOSE"},
}

// fieldAtPath walks path (toml-tag segments, e.g. ["gate", "gitleaks",
// "enabled"]) from v down to the leaf field — the traversal setField,
// applyEnv, and overlay all share instead of each re-implementing their own
// dot-path walk.
func fieldAtPath(v reflect.Value, path []string) (reflect.Value, error) {
	for i, p := range path {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("config field %q is not a section", strings.Join(path[:i], "."))
		}
		fv, ok := fieldByToml(v, p)
		if !ok {
			return reflect.Value{}, fmt.Errorf("unknown config field %q", strings.Join(path, "."))
		}
		v = fv
	}
	return v, nil
}

// setField walks a dot-notation path and sets the leaf field, parsing value to
// the field's type.
func setField(cfg *Config, field, value string) error {
	fv, err := fieldAtPath(reflect.ValueOf(cfg).Elem(), strings.Split(field, "."))
	if err != nil {
		return err
	}
	parsed, err := parseValue(fv.Type(), value)
	if err != nil {
		return fmt.Errorf("set %s: %w", field, err)
	}
	fv.Set(parsed)
	return nil
}

// fieldByToml finds a struct field by its toml tag name.
func fieldByToml(v reflect.Value, key string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// parseValue parses a string into a reflect.Value of the given type.
func parseValue(t reflect.Type, value string) (reflect.Value, error) {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf(value).Convert(t), nil
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(b).Convert(t), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(n).Convert(t), nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(f).Convert(t), nil
	case reflect.Slice:
		if t.Elem().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("unsupported slice element type %s", t.Elem().Kind())
		}
		var items []string
		if err := json.Unmarshal([]byte(value), &items); err != nil {
			return reflect.Value{}, fmt.Errorf("expected JSON string array, got %q", value)
		}
		return reflect.ValueOf(items), nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported type %s", t.Kind())
	}
}
