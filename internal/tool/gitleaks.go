package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SecretFinding is one gitleaks match, from its JSON report.
type SecretFinding struct {
	RuleID      string `json:"RuleID"`
	File        string `json:"File"`
	StartLine   int    `json:"StartLine"`
	Description string `json:"Description"`
}

// ScanResult is one gitleaks target's outcome. A non-zero exit code splits
// into a "finding" (a valid report with len > 0) versus an "error" (the
// report is missing, unparseable, or the run itself failed) — both block
// the merge, but are audited under different action names by the caller
// (baron-fp4/cli wiring, not this package).
type ScanResult struct {
	Target   string // "branch_diff", "staged", or "untracked"
	Clean    bool
	Findings []SecretFinding
	RunError string // non-empty only when gitleaks didn't produce a usable report
}

// GitleaksOptions configures a scan. ReportDir holds one JSON report per
// target; IgnorePath is not passed as a flag — gitleaks auto-discovers
// .gitleaksignore in the scanned directory, so callers just need the file
// to exist under dir.
type GitleaksOptions struct {
	ReportDir    string
	Redact       bool
	BaselinePath string
}

// ScanAll runs gitleaks against the branch diff (base...HEAD), staged
// content, and untracked files in dir, stopping at the first non-clean
// result: once one target blocks the merge, there's no reason to keep
// scanning.
func ScanAll(ctx context.Context, runner Runner, dir, base string, opts GitleaksOptions) ScanResult {
	targets := []struct {
		name string
		args []string
	}{
		{"branch_diff", []string{"git", "--log-opts=" + base + "...HEAD"}},
		{"staged", []string{"git", "--staged"}},
		{"untracked", []string{"dir", dir}},
	}
	for _, t := range targets {
		res := scanOne(ctx, runner, dir, t.name, t.args, opts)
		if !res.Clean {
			return res
		}
	}
	return ScanResult{Clean: true}
}

// scanOne runs one gitleaks invocation and classifies its result.
func scanOne(ctx context.Context, runner Runner, dir, target string, cmdArgs []string, opts GitleaksOptions) ScanResult {
	reportPath := filepath.Join(opts.ReportDir, target+"-report.json")
	args := append(append([]string{}, cmdArgs...), "--report-path", reportPath)
	if opts.Redact {
		args = append(args, "--redact")
	}
	if opts.BaselinePath != "" {
		args = append(args, "--baseline-path", opts.BaselinePath)
	}

	res, err := runner.Run(ctx, "gitleaks", args, Options{Dir: dir})
	if err == nil && res.ExitCode == 0 {
		return ScanResult{Target: target, Clean: true}
	}
	// A gitleaks that never ran writes no report, so the report-not-found
	// path below would blame the report for a missing binary. Say what is
	// actually wrong instead: the scan is mandatory, so this still blocks.
	if isNotInstalled(err) {
		return ScanResult{Target: target, RunError: "gitleaks is not installed (brew install gitleaks); the secret scan cannot be skipped"}
	}

	findings, parseErr := readGitleaksReport(reportPath)
	if parseErr != nil {
		return ScanResult{Target: target, RunError: fmt.Sprintf("gitleaks %s: %v", target, parseErr)}
	}
	if len(findings) == 0 {
		// Non-zero exit with an empty, valid report: treat as a run error
		// rather than silently calling it clean.
		return ScanResult{Target: target, RunError: fmt.Sprintf("gitleaks %s: exit %d with no findings in report", target, res.ExitCode)}
	}
	return ScanResult{Target: target, Findings: findings}
}

// readGitleaksReport reads and parses a gitleaks JSON report. A missing
// file is a run error.
func readGitleaksReport(path string) ([]SecretFinding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("report not found: %w", err)
	}
	var findings []SecretFinding
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, fmt.Errorf("report unparseable: %w", err)
	}
	return findings, nil
}
