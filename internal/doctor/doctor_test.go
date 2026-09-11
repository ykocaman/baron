package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// fakeResult is a canned runner response for one command invocation.
type fakeResult struct {
	stdout string
	stderr string
	err    bool
}

// fakeRunner returns pre-defined outputs keyed by "name arg...".
// Commands without an entry behave like a missing executable.
type fakeRunner struct {
	outputs map[string]fakeResult
}

// Run implements tool.Runner.
func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ tool.Options) (tool.Result, error) {
	if r, ok := f.outputs[name+" "+strings.Join(args, " ")]; ok {
		if r.err {
			return tool.Result{}, errors.New("not found")
		}
		return tool.Result{Stdout: r.stdout, Stderr: r.stderr}, nil
	}
	return tool.Result{}, errors.New("not found")
}

// okDeps returns a fake runner where every dependency is installed at a
// version that satisfies its constraint.
func okDeps() *fakeRunner {
	return &fakeRunner{outputs: map[string]fakeResult{
		"git --version":      {stdout: "git version 2.50.1\n"},
		"bd --version":       {stdout: "bd version 1.1.2 (Homebrew)\n"},
		"gitleaks --version": {stdout: "gitleaks version 8.30.1\n"},
		"hunk --version":     {stdout: "hunk version 0.17.0\n"},
		"glow --version":     {stdout: "glow version 2.1.0\n"},
		"tmux -V":            {stdout: "tmux 3.3a\n"},
	}}
}

// depResult finds a check by name in a report.
func depResult(t *testing.T, results []CheckResult, name string) CheckResult {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no check result for %q in %+v", name, results)
	return CheckResult{}
}

func TestCheckDepsAllPresent(t *testing.T) {
	results := CheckDeps(context.Background(), okDeps())
	if len(results) != 6 {
		t.Fatalf("got %d checks, want 6", len(results))
	}
	for _, r := range results {
		if r.Severity != SeverityOK {
			t.Errorf("%s: Severity = %q, want %q (message %q)", r.Name, r.Severity, SeverityOK, r.Message)
		}
	}
}

func TestCheckDepsMissingMandatory(t *testing.T) {
	// Override git to behave as not installed.
	results := CheckDeps(context.Background(), &fakeRunner{outputs: map[string]fakeResult{
		"bd --version":       {stdout: "bd version 1.1.2 (Homebrew)\n"},
		"gitleaks --version": {stdout: "gitleaks version 8.30.1\n"},
		"hunk --version":     {stdout: "hunk version 0.17.0\n"},
		"glow --version":     {stdout: "glow version 2.1.0\n"},
		"tmux -V":            {stdout: "tmux 3.3a\n"},
	}})

	git := depResult(t, results, "git")
	if git.Severity != SeverityError {
		t.Errorf("git missing: Severity = %q, want %q", git.Severity, SeverityError)
	}
	if !git.Required {
		t.Errorf("git missing: Required = false, want true")
	}
	if git.InstallHint == "" {
		t.Errorf("git missing: InstallHint is empty, want install hint")
	}
}

func TestCheckDepsVersionDrift(t *testing.T) {
	runner := okDeps()
	runner.outputs["bd --version"] = fakeResult{stdout: "bd version 1.0.5 (Homebrew)\n"}

	bd := depResult(t, CheckDeps(context.Background(), runner), "bd")
	if bd.Severity != SeverityError {
		t.Errorf("bd drift: Severity = %q, want %q", bd.Severity, SeverityError)
	}
	if bd.Version != "1.0.5" {
		t.Errorf("bd drift: Version = %q, want %q", bd.Version, "1.0.5")
	}
}

func TestPinnedBDVersion(t *testing.T) {
	if got := PinnedBDVersion(); got != "1.1.2" {
		t.Fatalf("PinnedBDVersion() = %q, want 1.1.2", got)
	}
}

// TestCheckDepsMissingTmux: Layer 3 — tmux is optional
// (degrades to subprocess + log file), so a missing tmux must be an info,
// not an error that fails `baron doctor`/`baron init`.
func TestCheckDepsMissingTmux(t *testing.T) {
	runner := okDeps()
	delete(runner.outputs, "tmux -V")

	tmux := depResult(t, CheckDeps(context.Background(), runner), "tmux")
	if tmux.Severity != SeverityInfo {
		t.Errorf("tmux missing: Severity = %q, want %q", tmux.Severity, SeverityInfo)
	}
	if tmux.Required {
		t.Errorf("tmux missing: Required = true, want false (optional, Layer 3)")
	}
	if tmux.InstallHint == "" {
		t.Errorf("tmux missing: InstallHint is empty, want hint")
	}
	if !strings.Contains(tmux.Message, "optional") {
		t.Errorf("tmux missing: Message = %q, want it to say optional", tmux.Message)
	}
}

func TestCheckDepsTmuxPresent(t *testing.T) {
	tmux := depResult(t, CheckDeps(context.Background(), okDeps()), "tmux")
	if tmux.Severity != SeverityOK {
		t.Errorf("tmux present: Severity = %q, want %q", tmux.Severity, SeverityOK)
	}
	if tmux.Version != "3.3a" {
		t.Errorf("tmux present: Version = %q, want %q", tmux.Version, "3.3a")
	}
}

func TestCheckDepsMissingOptional(t *testing.T) {
	runner := okDeps()
	delete(runner.outputs, "glow --version")

	glow := depResult(t, CheckDeps(context.Background(), runner), "glow")
	if glow.Severity != SeverityInfo {
		t.Errorf("glow missing: Severity = %q, want %q", glow.Severity, SeverityInfo)
	}
	if glow.Required {
		t.Errorf("glow missing: Required = true, want false")
	}
	if glow.InstallHint == "" {
		t.Errorf("glow missing: InstallHint is empty, want hint")
	}
}

func TestCheckDepsBelowMinimum(t *testing.T) {
	runner := okDeps()
	runner.outputs["hunk --version"] = fakeResult{stdout: "hunk version 0.16.0\n"}

	hunk := depResult(t, CheckDeps(context.Background(), runner), "hunk")
	if hunk.Severity != SeverityWarn {
		t.Errorf("hunk below min: Severity = %q, want %q", hunk.Severity, SeverityWarn)
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{name: "git", out: "git version 2.50.1", want: "2.50.1"},
		{name: "bd homebrew", out: "bd version 1.0.5 (Homebrew)", want: "1.0.5"},
		{name: "gh date", out: "gh version 2.96.0 (2024-12-18)", want: "2.96.0"},
		{name: "gitleaks", out: "gitleaks version 8.30.1", want: "8.30.1"},
		{name: "tmux dash-v", out: "tmux 3.3a", want: "3.3a"},
		{name: "bare", out: "1.2.3\n", want: "1.2.3"},
		{name: "empty", out: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVersion(tc.out); got != tc.want {
				t.Fatalf("parseVersion(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{name: "newer major", a: "2.50.1", b: "2.30.0", want: 1},
		{name: "older minor", a: "1.0.5", b: "1.1.2", want: -1},
		{name: "equal", a: "2.0.0", b: "2.0.0", want: 0},
		{name: "uneven shorter", a: "2.0", b: "2.0.0", want: 0},
		{name: "uneven longer wins", a: "2.0.1", b: "2.0", want: 1},
		{name: "suffix equal", a: "3.3a", b: "3.3.0", want: 0},
		{name: "suffix older", a: "3.2.9", b: "3.3a", want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareVersions(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()

	if ok, msg := CheckConfig(dir); ok {
		t.Errorf("missing config: ok = true, want false (%s)", msg)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".baron"), 0o750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, ".baron", "config.toml")
	if err := os.WriteFile(configPath, []byte("[general]\nprofile = \"go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, msg := CheckConfig(dir); !ok {
		t.Errorf("valid config: ok = false, want true (%s)", msg)
	}

	if err := os.WriteFile(configPath, []byte("[general\nprofile = \""), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := CheckConfig(dir); ok {
		t.Errorf("invalid config: ok = true, want false")
	}
}

// TestCheckCommitSigning is the regression test for baron-178: docs/PRD/
// security.md §4 requires doctor to check that the user's signing
// configuration exists, since the pipeline blocks the merge on any unsigned
// commit.
func TestCheckCommitSigning(t *testing.T) {
	tests := []struct {
		name     string
		config   map[string]fakeResult
		severity Severity
		want     string
	}{
		{
			name:     "no signing key",
			config:   map[string]fakeResult{},
			severity: SeverityWarn,
			want:     "user.signingkey",
		},
		{
			name: "key set but signing off",
			config: map[string]fakeResult{
				"git config --get user.signingkey": {stdout: "ssh-ed25519 AAAA\n"},
				"git config --get commit.gpgsign":  {stdout: "false\n"},
			},
			severity: SeverityWarn,
			want:     "commit.gpgsign is off",
		},
		{
			name: "configured",
			config: map[string]fakeResult{
				"git config --get user.signingkey": {stdout: "ssh-ed25519 AAAA\n"},
				"git config --get commit.gpgsign":  {stdout: "true\n"},
				"git config --get gpg.format":      {stdout: "ssh\n"},
			},
			severity: SeverityOK,
			want:     "ssh",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckCommitSigning(context.Background(), &fakeRunner{outputs: tc.config}, t.TempDir())
			if got.Severity != tc.severity {
				t.Errorf("severity = %q, want %q (message: %s)", got.Severity, tc.severity, got.Message)
			}
			if !strings.Contains(got.Message, tc.want) {
				t.Errorf("message = %q, want it to mention %q", got.Message, tc.want)
			}
		})
	}
}
