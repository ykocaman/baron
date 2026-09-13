package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// findModel returns the first model whose Agent matches agentName.
func findModel(models []agent.Model, agentName string) (agent.Model, bool) {
	for _, m := range models {
		if m.Agent == agentName {
			return m, true
		}
	}
	return agent.Model{}, false
}

// initOut returns a fake runner where every dependency is installed at its
// pinned or minimum version, bd matches the pin, and every model CLI exists
// on PATH.
func initOut() *fakeRunner {
	deps := map[string]string{
		"git": "2.50.1", "bd": "1.1.2", "gitleaks": "8.30.1",
		"hunk": "0.17.0", "tmux": "3.3a",
	}
	models := map[string]bool{"claude": true, "codex": true, "opencode": true, "gemini": true, "agy": true, "cline": true}
	return &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return tool.Result{Stdout: "true\n"}, nil
		}
		if v, ok := deps[name]; ok {
			return tool.Result{Stdout: name + " version " + v + "\n"}, nil
		}
		if name == "which" && len(args) > 0 {
			return tool.Result{Stdout: "/usr/local/bin/" + args[0] + "\n"}, nil
		}
		if models[name] {
			return tool.Result{Stdout: name + " version 1.0.0\n"}, nil
		}
		return tool.Result{}, nil
	}}
}

// declineConfirm is a confirm func that always declines, for tests
// exercising Init's own respect for a declined confirmation — independent
// of what Run happens to pass in production (always alwaysApprove there).
func declineConfirm(string) (bool, error) { return false, nil }

// skipAutoMerge is a confirmAutoMerge func that always declines, for tests
// that don't care about the auto-merge question.
func skipAutoMerge() (bool, error) { return false, nil }

func TestInitCreatesWorkspace(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	res, ok, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !ok {
		t.Fatal("Init: ok = false, want true")
	}
	if !hasCall(fr, "init", "--quiet") {
		t.Fatalf("calls = %v, want bd init --quiet", fr.calls)
	}
	if !res.Created {
		t.Fatalf("res.Created = false, want true (fresh workspace)")
	}
	data, err := os.ReadFile(filepath.Join(a.dir, ".baron", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `profile = "go"`) {
		t.Fatalf("config = %q, want go profile", data)
	}
	// Agent discovery is machine-wide: init writes the caches under
	// ~/.cache/baron and leaves .baron/ free of them entirely.
	if _, err := os.Stat(filepath.Join(a.dir, ".baron", "models.json")); !os.IsNotExist(err) {
		t.Fatalf(".baron/models.json exists after init (err=%v), want agent data cached machine-wide only", err)
	}
	reg, _, err := agent.LoadAgents()
	if err != nil {
		t.Fatalf("load agent cache: %v", err)
	}
	if len(reg.List()) == 0 {
		t.Fatal("agent cache empty, want the discovered agents")
	}
	if len(agent.LoadCatalog().Models) == 0 {
		t.Fatal("model catalog empty, want init to discover assignable models")
	}
	if len(res.Models) == 0 {
		t.Fatal("res.Models empty, want init to report discovered models")
	}
}

func TestInitFindsExistingWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	fr := initOut()
	a := newTestApp(t, fr)
	a.dir = dir
	a.beads = store.NewBeadStore(fr, dir)
	res, ok, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !ok {
		t.Fatal("Init: ok = false, want true")
	}
	if hasCall(fr, "init", "--quiet") {
		t.Fatalf("calls = %v, must not re-run bd init", fr.calls)
	}
	if res.Created {
		t.Fatal("res.Created = true, want false (workspace already existed)")
	}
}

func TestInitNotGitRepo(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return tool.Result{Stderr: "fatal: not a git repository (or any of the parent directories): .git"},
				errors.New("exit status 128")
		}
		return tool.Result{}, nil
	}}
	a := newTestApp(t, fr)
	_, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("Init() error = %v, want a not-a-git-repository error", err)
	}
	if _, err := os.Stat(filepath.Join(a.dir, ".baron")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".baron was created outside a git repository")
	}
}

func TestInitKeepsExistingConfig(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	cfgPath := filepath.Join(a.dir, ".baron", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[general]\nprofile = \"react-ts\"\n[gate]\ntimeout = 42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "timeout = 42") || !strings.Contains(string(data), `profile = "react-ts"`) {
		t.Fatalf("config = %q, want existing values preserved", data)
	}
}

func TestInitAssignsDetectedProfile(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if err := os.WriteFile(filepath.Join(a.dir, "package.json"), []byte(`{"name": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(a.dir, ".baron", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `profile = "react-ts"`) {
		t.Fatalf("config = %q, want react-ts profile", data)
	}
}

// TestInitExportsBuiltinProfiles: a fresh init writes every built-in
// language's gate profile — not just the detected one — as an editable
// .toml file, one per language, so a project ends up with a small library
// of starter pipelines instead of one hardcoded, undiscoverable choice.
func TestInitExportsBuiltinProfiles(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}

	for _, name := range []string{"go", "react-ts", "rust", "php", "python"} {
		path := filepath.Join(a.dir, ".baron", "profiles", name+".toml")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s not exported: %v", path, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(a.dir, ".baron", "profiles", "go.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `command = "gofmt"`) {
		t.Errorf("go.toml = %q, want the exported gofmt step", data)
	}
}

// TestInitExportsBuiltinProfilesGlobally: the same files also land in the
// machine-wide user profiles directory, so every future project on this
// machine gets them without needing its own init to write them again.
func TestInitExportsBuiltinProfilesGlobally(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}

	userDir, err := store.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userDir, "profiles", "python.toml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s not exported: %v", path, err)
	}
}

// TestInitExportNeverOverwritesHandEditedProfile: a profile file the user
// already customized (in a project that already has a config.toml, so
// init's export step is skipped entirely) must survive untouched.
func TestInitExportNeverOverwritesHandEditedProfile(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if err := os.MkdirAll(filepath.Join(a.dir, ".baron"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.dir, ".baron", "config.toml"), []byte("[general]\nprofile = \"go\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profilesDir := filepath.Join(a.dir, ".baron", "profiles")
	if err := os.MkdirAll(profilesDir, 0o750); err != nil {
		t.Fatal(err)
	}
	handEdited := "[[steps]]\nname = \"test\"\ncommand = \"hand-edited\"\n"
	if err := os.WriteFile(filepath.Join(profilesDir, "go.toml"), []byte(handEdited), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(profilesDir, "go.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != handEdited {
		t.Errorf("go.toml = %q, want the hand-edited content untouched (config.toml already existed, so export must not have run)", data)
	}
}

func TestInitAbortsOnMandatoryDepMissing(t *testing.T) {
	fr := &fakeRunner{run: func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "--version" {
			return tool.Result{Stderr: "zsh: command not found: bd"}, errors.New("exit status 127")
		}
		return tool.Result{Stdout: "true\n"}, nil
	}}
	a := newTestApp(t, fr)
	_, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err == nil || !strings.Contains(err.Error(), "mandatory dependencies missing: bd") {
		t.Fatalf("Init() error = %v, want a mandatory-deps error naming bd", err)
	}
}

func TestInitConfirmDecline(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestApp(t, fr)
	_, ok, err := a.Init(context.Background(), declineConfirm, skipAutoMerge)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if ok {
		t.Fatal("Init: ok = true, want false after a declined confirm")
	}
	if hasCall(fr, "init", "--quiet") {
		t.Fatalf("calls = %v, want no bd init after decline", fr.calls)
	}
	if _, err := os.Stat(filepath.Join(a.dir, ".baron")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".baron was created after decline")
	}
}

func TestInitAutoMergeAcceptEnablesConfig(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.in = strings.NewReader("y\n")
	a.inTTY = func() bool { return true }
	if _, _, err := a.Init(context.Background(), alwaysApprove, a.confirmAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Merge.Auto.Enabled {
		t.Fatalf("merge.auto.enabled = false, want true after accepting the prompt")
	}
}

func TestInitAutoMergeDeclineLeavesConfigUnset(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	a.in = strings.NewReader("n\n")
	a.inTTY = func() bool { return true }
	if _, _, err := a.Init(context.Background(), alwaysApprove, a.confirmAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Merge.Auto.Enabled {
		t.Fatal("merge.auto.enabled = true, want false after declining auto-merge")
	}
}

func TestInitAutoMergeSkippedOutsideTTY(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	if _, _, err := a.Init(context.Background(), alwaysApprove, a.confirmAutoMerge); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := store.LoadConfig(a.dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Merge.Auto.Enabled {
		t.Fatal("merge.auto.enabled = true, want false outside a TTY (never opt in without asking)")
	}
}

func TestInitAbortsOnBdDrift(t *testing.T) {
	fr := initOut()
	orig := fr.run
	fr.run = func(name string, args []string) (tool.Result, error) {
		if name == "bd" && len(args) > 0 && args[0] == "--version" {
			return tool.Result{Stdout: "bd version 1.0.5 (Homebrew)\n"}, nil
		}
		return orig(name, args)
	}
	a := newTestApp(t, fr)
	_, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err == nil || !strings.Contains(err.Error(), "mandatory dependencies missing: bd") {
		t.Fatalf("Init() error = %v, want a drift abort naming bd", err)
	}
}

// TestInitAppliesConfigAgentOverrides: an [[agents]] block both overrides a
// discovered agent and registers one BARON ships no candidate for. The
// override shapes the report and the catalog, but never the machine-wide
// cache — that stays a record of what is actually installed, shared by
// every project on the machine.
func TestInitAppliesConfigAgentOverrides(t *testing.T) {
	fr := initOut()
	a := newTestApp(t, fr)
	active := true
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.Agents = []agent.ConfigAgent{
			{Name: "claude", Command: "claude-code"},
			{Name: "custom", Command: "my-agent", Tags: []string{"cli"}, Active: &active},
		}
		return cfg, nil
	}
	res, _, err := a.Init(context.Background(), alwaysApprove, skipAutoMerge)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, ok := findModel(res.Models, "custom"); !ok {
		t.Errorf("models = %+v, want the config-registered custom agent assignable", res.Models)
	}
	reg, _, err := agent.LoadAgents()
	if err != nil {
		t.Fatalf("load agent cache: %v", err)
	}
	if _, ok := reg.Get("custom"); ok {
		t.Error("machine-wide cache has the project's [[agents]] entry, want project overrides kept out of it")
	}
}

func TestLoadAgentsAppliesConfigOverrides(t *testing.T) {
	a := newTestApp(t, &fakeRunner{})
	a.loadConfig = func(string) (*store.Config, error) {
		cfg := store.DefaultConfig()
		cfg.Agents = []agent.ConfigAgent{{Name: "claude", Command: "claude-code"}}
		return cfg, nil
	}
	reg := agent.NewRegistry()
	reg.Add(agent.Agent{Name: "claude", Command: "claude", Status: agent.StatusActive, Backend: "subprocess"})
	if err := agent.SaveAgents(reg); err != nil {
		t.Fatal(err)
	}
	a.loadAgents()
	got, ok := a.agents.Get("claude")
	if !ok || got.Command != "claude-code" {
		t.Fatalf("claude = %+v, want command claude-code from config", got)
	}
}
