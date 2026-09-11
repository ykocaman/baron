package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scriptedGitleaks simulates gitleaks: writes a report file (as the real
// binary would) and returns an exit code, keyed by scan target (the last
// positional arg gitleaks receives: the log-opts value, "--staged", or the
// scanned dir).
type scriptedGitleaks struct {
	reports map[string]string // target name -> report JSON to write ("" = don't write)
	exit    map[string]int    // target name -> exit code (default 0)
	calls   [][]string
}

func (g *scriptedGitleaks) Run(_ context.Context, name string, args []string, opts Options) (Result, error) {
	if name != "gitleaks" {
		return Result{}, nil
	}
	g.calls = append(g.calls, args)
	target := targetOf(args)

	var reportPath string
	for i, a := range args {
		if a == "--report-path" && i+1 < len(args) {
			reportPath = args[i+1]
		}
	}
	if report, ok := g.reports[target]; ok && reportPath != "" {
		_ = os.WriteFile(reportPath, []byte(report), 0o600)
	}

	exit := g.exit[target]
	if exit != 0 {
		return Result{ExitCode: exit}, errors.New("exit status")
	}
	return Result{}, nil
}

// targetOf recovers which ScanAll target a gitleaks invocation is for, from
// its subcommand and arguments.
func targetOf(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "git":
		if slices.Contains(args[1:], "--staged") {
			return "staged"
		}
		return "branch_diff"
	case "dir":
		return "untracked"
	}
	return ""
}

func TestScanAllClean(t *testing.T) {
	dir := t.TempDir()
	g := &scriptedGitleaks{}
	res := ScanAll(context.Background(), g, dir, "origin/main", GitleaksOptions{ReportDir: dir})
	if !res.Clean {
		t.Errorf("ScanAll() = %+v, want clean", res)
	}
	if len(g.calls) != 3 {
		t.Errorf("gitleaks calls = %d, want 3 (pr_diff, staged, untracked)", len(g.calls))
	}
}

func TestScanAllFindingBlocksAndStops(t *testing.T) {
	dir := t.TempDir()
	g := &scriptedGitleaks{
		exit:    map[string]int{"branch_diff": 1},
		reports: map[string]string{"branch_diff": `[{"RuleID":"aws-key","File":"a.go","StartLine":3,"Description":"AWS key"}]`},
	}
	res := ScanAll(context.Background(), g, dir, "origin/main", GitleaksOptions{ReportDir: dir})
	if res.Clean {
		t.Fatal("ScanAll() clean = true, want a finding to block")
	}
	if res.RunError != "" {
		t.Errorf("RunError = %q, want empty (this is a finding, not a run error)", res.RunError)
	}
	if len(res.Findings) != 1 || res.Findings[0].RuleID != "aws-key" {
		t.Errorf("Findings = %+v, want one aws-key finding", res.Findings)
	}
	if len(g.calls) != 1 {
		t.Errorf("gitleaks calls = %d, want 1 (stops at the first non-clean target)", len(g.calls))
	}
}

func TestScanAllMissingReportIsRunError(t *testing.T) {
	dir := t.TempDir()
	g := &scriptedGitleaks{exit: map[string]int{"branch_diff": 1}} // no report written
	res := ScanAll(context.Background(), g, dir, "origin/main", GitleaksOptions{ReportDir: dir})
	if res.Clean {
		t.Fatal("ScanAll() clean = true, want a run error to block")
	}
	if res.RunError == "" {
		t.Error("RunError = \"\", want a missing-report error")
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none for a run error", res.Findings)
	}
}

func TestScanAllUnparseableReportIsRunError(t *testing.T) {
	dir := t.TempDir()
	g := &scriptedGitleaks{
		exit:    map[string]int{"staged": 1},
		reports: map[string]string{"staged": "not json"},
	}
	res := ScanAll(context.Background(), g, dir, "origin/main", GitleaksOptions{ReportDir: dir})
	if res.Clean || res.RunError == "" {
		t.Errorf("ScanAll() = %+v, want a run error for an unparseable report", res)
	}
}

func TestScanAllPassesRedactAndBaseline(t *testing.T) {
	dir := t.TempDir()
	g := &scriptedGitleaks{}
	ScanAll(context.Background(), g, dir, "origin/main", GitleaksOptions{ReportDir: dir, Redact: true, BaselinePath: "baseline.json"})
	for _, call := range g.calls {
		if !slices.Contains(call, "--redact") {
			t.Errorf("call %v missing --redact", call)
		}
		if !slices.Contains(call, "--baseline-path") {
			t.Errorf("call %v missing --baseline-path", call)
		}
	}
}

func TestReadGitleaksReportMissingFile(t *testing.T) {
	_, err := readGitleaksReport(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Error("readGitleaksReport() error = nil, want an error for a missing file")
	}
}

// missingGitleaks is a Runner where gitleaks is not on PATH, the way
// os/exec reports it.
type missingGitleaks struct{}

func (missingGitleaks) Run(_ context.Context, name string, _ []string, _ Options) (Result, error) {
	if name == "gitleaks" {
		return Result{ExitCode: -1}, &exec.Error{Name: "gitleaks", Err: exec.ErrNotFound}
	}
	return Result{}, nil
}

// TestScanAllGitleaksNotInstalled is the regression test for baron-a0h: a
// gitleaks that never ran writes no report, and the missing report was
// reported instead of the missing binary ("report not found: open
// .baron/pr_diff-report.json: no such file or directory").
func TestScanAllGitleaksNotInstalled(t *testing.T) {
	res := ScanAll(context.Background(), missingGitleaks{}, t.TempDir(), "origin/main", GitleaksOptions{ReportDir: t.TempDir()})
	if res.Clean {
		t.Fatal("ScanAll: a scan that could not run must not be clean")
	}
	if !strings.Contains(res.RunError, "not installed") || !strings.Contains(res.RunError, "brew install gitleaks") {
		t.Errorf("RunError = %q, want it to name the missing binary and how to install it", res.RunError)
	}
	if strings.Contains(res.RunError, "report not found") {
		t.Errorf("RunError = %q, want the missing binary blamed, not the missing report", res.RunError)
	}
}
