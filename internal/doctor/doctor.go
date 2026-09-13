// Package doctor implements BARON's health-check system. It probes required
// CLI dependencies, discovers the coding agents installed on the machine
// and the models they can run, and validates project configuration, then
// renders the findings as text or JSON.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/profile"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// Severity represents the severity of a health check result.
type Severity string

// Severity values rank from "ok" (healthy) to "error" (broken).
const (
	SeverityOK    Severity = "ok"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
	SeverityInfo  Severity = "info"
)

// CheckResult holds the result of a single health check.
type CheckResult struct {
	Name        string   `json:"name"` // e.g., "git", "bd", "gitleaks"
	Severity    Severity `json:"severity"`
	Message     string   `json:"message"`                // human-readable message
	Version     string   `json:"version,omitempty"`      // installed version (if found)
	Required    bool     `json:"required"`               // true = mandatory, false = optional
	InstallHint string   `json:"install_hint,omitempty"` // e.g. "brew install bd"
}

// Report holds the full health check results.
type Report struct {
	Checks []CheckResult `json:"checks"`
	// Agents is the full candidate checklist, including CLIs not installed;
	// Models is the assignable catalog those agents contribute, which only
	// installed agents appear in. The two are different questions — "what
	// is on this machine" and "what can I assign a bead to" — and the
	// report answers both.
	Agents    []agent.Agent `json:"agents"`
	Models    []agent.Model `json:"models"`
	Profile   string        `json:"profile"`
	ConfigOK  bool          `json:"config_ok"`
	ConfigMsg string        `json:"config_msg"`
}

// depTimeout bounds each dependency's version probe.
const depTimeout = 10 * time.Second

// versionFlag is the version-probe flag shared by every dependency except
// tmux, which uses -V instead.
const versionFlag = "--version"

// depKind classifies how a missing or outdated dependency is reported.
type depKind int

const (
	depMandatory depKind = iota // error if missing
	depRequired                 // warn if missing
	depOptional                 // info if missing
)

// depSpec describes a single dependency check.
type depSpec struct {
	name    string
	flag    string // version flag, e.g. "--version"
	min     string // minimum version, "" = none
	pinned  string // exact pinned version, "" = none
	kind    depKind
	install string // brew install hint
}

// deps lists every dependency BARON relies on, in display order.
var deps = []depSpec{
	{name: "git", flag: versionFlag, min: "2.30.0", kind: depMandatory, install: "xcode-select --install"},
	{name: "bd", flag: versionFlag, pinned: "1.1.2", kind: depMandatory, install: "brew install beads"},
	{name: "gitleaks", flag: versionFlag, pinned: "8.30.1", kind: depRequired, install: "brew install gitleaks"},
	{name: "hunk", flag: versionFlag, min: "0.17.0", kind: depOptional, install: "brew install hunk"},
	{name: "tmux", flag: "-V", min: "3.3.0", kind: depOptional, install: "brew install tmux"},
}

// CheckDeps probes each dependency and returns its health result.
func CheckDeps(ctx context.Context, runner tool.Runner) []CheckResult {
	results := make([]CheckResult, 0, len(deps))
	for _, d := range deps {
		results = append(results, checkDep(ctx, runner, d))
	}
	return results
}

// checkDep runs one dependency's version probe and classifies the result.
func checkDep(ctx context.Context, runner tool.Runner, d depSpec) CheckResult {
	res, err := runner.Run(ctx, d.name, []string{d.flag}, tool.Options{Timeout: depTimeout})
	if err != nil {
		return missingResult(d)
	}
	version := parseVersion(res.Stdout)
	if version == "" {
		version = parseVersion(res.Stderr)
	}
	return versionResult(d, version)
}

// missingResult builds the result for a dependency not found on PATH.
func missingResult(d depSpec) CheckResult {
	r := CheckResult{Name: d.name, Required: d.kind != depOptional, InstallHint: d.install}
	switch d.kind {
	case depMandatory:
		r.Severity, r.Message = SeverityError, fmt.Sprintf("not found (%s)", constraint(d))
	case depRequired:
		r.Severity, r.Message = SeverityWarn, fmt.Sprintf("not found (%s)", constraint(d))
	case depOptional:
		r.Severity, r.Message = SeverityInfo, "not found (optional)"
	}
	return r
}

// versionResult builds the result for a found dependency given its version.
func versionResult(d depSpec, version string) CheckResult {
	r := CheckResult{Name: d.name, Required: d.kind != depOptional, Version: version}
	switch {
	case version == "":
		r.Severity, r.Message = SeverityWarn, "installed, version unknown"
	case d.pinned != "" && version != d.pinned:
		r.Severity, r.Message = SeverityError, fmt.Sprintf("%s (pinned %s) — version drift!", version, d.pinned)
	case d.pinned != "":
		r.Severity, r.Message = SeverityOK, fmt.Sprintf("%s (pinned %s)", version, d.pinned)
	case d.min != "" && compareVersions(version, d.min) < 0:
		r.Severity, r.Message = SeverityWarn, fmt.Sprintf("%s (min %s) — below minimum!", version, d.min)
	case d.min != "":
		r.Severity, r.Message = SeverityOK, fmt.Sprintf("%s (min %s)", version, d.min)
	default:
		// No min or pinned constraint declared for d — every entry in deps
		// currently sets one or the other, but nothing enforces that, so this
		// renders the bare version instead of a nonsensical "1.0 (min )".
		r.Severity, r.Message = SeverityOK, version
	}
	return r
}

// constraint renders the version expectation, e.g. "min 2.30.0" or
// "pinned 1.1.2".
func constraint(d depSpec) string {
	if d.pinned != "" {
		return fmt.Sprintf("pinned %s", d.pinned)
	}
	return fmt.Sprintf("min %s", d.min)
}

// PinnedBDVersion returns the exact bd version BARON requires. It drives
// store.BeadStore.CheckDrift, which shares the doctor's pinned constraint.
func PinnedBDVersion() string {
	for _, d := range deps {
		if d.name == "bd" {
			return d.pinned
		}
	}
	return ""
}

// parseVersion extracts the version token from a command's version output,
// e.g. "git version 2.50.1" -> "2.50.1", "bd version 1.0.5 (Homebrew)" ->
// "1.0.5", and "tmux 3.3a" -> "3.3a".
func parseVersion(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	out = strings.TrimSpace(out)
	if i := strings.Index(out, "version "); i >= 0 {
		out = out[i+len("version "):]
	} else if i := strings.IndexByte(out, ' '); i >= 0 {
		out = out[i+1:] // e.g. "tmux 3.3a" -> "3.3a"
	}
	if i := strings.IndexAny(out, " \t("); i >= 0 {
		out = out[:i]
	}
	return strings.TrimSpace(out)
}

// compareVersions compares dotted version strings numerically, segment by
// segment. It returns -1 if a < b, 0 if equal, and 1 if a > b.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := segNum(as, i), segNum(bs, i)
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// segNum returns segment i of parts as a number, 0 when absent or unparsable.
// A leading-digit parse handles suffixes like "3a" in "tmux 3.3a".
func segNum(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	if n, err := strconv.Atoi(parts[i]); err == nil {
		return n
	}
	j := 0
	for j < len(parts[i]) && parts[i][j] >= '0' && parts[i][j] <= '9' {
		j++
	}
	if j == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[i][:j])
	return n
}

// CheckConfig checks if .baron/config.toml exists and is valid.
// Returns (true, msg) when found and valid; (false, msg) otherwise.
// The second return carries a human-readable status for display.
func CheckConfig(projectDir string) (bool, string) {
	path := filepath.Join(projectDir, ".baron", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "not found — run `baron init` (using defaults)"
		}
		return false, fmt.Sprintf(".baron/config.toml unreadable: %v", err)
	}
	var cfg store.Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return false, fmt.Sprintf(".baron/config.toml invalid: %v", err)
	}
	return true, ".baron/config.toml found"
}

// CheckCommitSigning reports whether the repository can produce signed
// commits (docs/PRD/security.md §4: BARON never generates or stores keys,
// it only checks that the user's own signing configuration exists). This
// is not cosmetic: the pipeline's signed-commit check blocks readiness to
// merge and parks the bead in the human queue for any unsigned commit, so
// an unconfigured repo stalls on its first bead.
func CheckCommitSigning(ctx context.Context, runner tool.Runner, projectDir string) CheckResult {
	const name = "commit-signing"
	hint := "git config gpg.format ssh && git config user.signingkey <key> && git config commit.gpgsign true"
	key := gitConfig(ctx, runner, projectDir, "user.signingkey")
	if key == "" {
		return CheckResult{
			Name: name, Severity: SeverityWarn, Required: true, InstallHint: hint,
			Message: "no user.signingkey — unsigned commits are blocked before merging",
		}
	}
	if !isTruthy(gitConfig(ctx, runner, projectDir, "commit.gpgsign")) {
		return CheckResult{
			Name: name, Severity: SeverityWarn, Required: true, InstallHint: hint,
			Message: "user.signingkey set but commit.gpgsign is off — commits would be unsigned",
		}
	}
	format := gitConfig(ctx, runner, projectDir, "gpg.format")
	if format == "" {
		format = "openpgp"
	}
	return CheckResult{Name: name, Severity: SeverityOK, Required: true, Message: "enabled (" + format + ")"}
}

// gitConfig reads one git config value for projectDir, "" when unset — a
// missing key makes `git config --get` exit non-zero, which is absence, not
// an error.
func gitConfig(ctx context.Context, runner tool.Runner, dir, key string) string {
	res, err := runner.Run(ctx, "git", []string{"config", "--get", key}, tool.Options{Dir: dir, Timeout: depTimeout})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// isTruthy reports whether a git config value reads as enabled.
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// Run executes all health checks and returns a report. It also refreshes
// the machine-wide agent and model caches (~/.cache/baron), which is what
// makes `baron doctor` the way to pick up a newly installed agent CLI: the
// probe has to run anyway for the checklist, so the caches every later
// assign reads are rewritten from the same pass rather than by a second
// one. configAgents are the project's [[agents]] overrides, which shape
// the catalog but are never written to the shared cache.
func Run(ctx context.Context, projectDir string, runner tool.Runner, configAgents []agent.ConfigAgent) Report {
	configOK, configMsg := CheckConfig(projectDir)
	checks := CheckDeps(ctx, runner)
	checks = append(checks, CheckCommitSigning(ctx, runner, projectDir))
	// A cache-write failure is not a health problem worth failing the
	// report over: the probe results in hand are still complete and every
	// consumer falls back to probing on demand.
	d, _ := agent.Refresh(ctx, runner, configAgents)
	return Report{
		Checks:    checks,
		Agents:    d.Checklist,
		Models:    d.Catalog.Models,
		Profile:   profile.Detect(projectDir).Name,
		ConfigOK:  configOK,
		ConfigMsg: configMsg,
	}
}
