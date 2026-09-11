// Package store implements BARON's persistent configuration and audit storage.
package store

import (
	"fmt"
	"os"
	"reflect"
	"strings"
)

// applyEnv overrides Config fields from BARON_<SECTION>_<KEY> env vars,
// walking configFields (config_field.go) instead of hand-listing each one:
// a field with a non-empty env there gets its override applied, then
// syncAliases mirrors it onto its shorthand/canonical counterpart (name<->
// project_name, profile<->default_profile) exactly as overlay does for the
// same pair on config.toml load.
func applyEnv(cfg *Config) error {
	v := reflect.ValueOf(cfg).Elem()
	for _, cf := range configFields {
		if cf.env == "" {
			continue
		}
		val, ok := os.LookupEnv(cf.env)
		if !ok {
			continue
		}
		fv, err := fieldAtPath(v, cf.tomlPath)
		if err != nil {
			return fmt.Errorf("%s: %w", cf.env, err)
		}
		parsed, err := parseValue(fv.Type(), val)
		if err != nil {
			return fmt.Errorf("%s: %w", cf.env, err)
		}
		fv.Set(parsed)
		syncAliases(cfg, strings.Join(cf.tomlPath, "."))
	}
	return nil
}
