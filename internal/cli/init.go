package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"

	"github.com/baron-cli/baron/internal/agent"
	"github.com/baron-cli/baron/internal/doctor"
	"github.com/baron-cli/baron/internal/profile"
	"github.com/baron-cli/baron/internal/store"
	"github.com/baron-cli/baron/internal/tool"
)

// initResult is Init's result.
type initResult struct {
	Workspace string               `json:"workspace"`
	Created   bool                 `json:"created"`
	Profile   string               `json:"profile"`
	Agents    []agent.Agent        `json:"agents"`
	Models    []agent.Model        `json:"models"`
	Dataflow  dataflow             `json:"dataflow"`
	Checks    []doctor.CheckResult `json:"-"`
}

// Init runs the init flow, cobra-free: git-repo check, config defaults
// (profile detection + optional auto-merge policy), beads workspace setup,
// doctor mandatory-dependency gate, bd-version drift check, legacy cache
// cleanup. Idempotent — existing configs and workspaces are kept, so it's
// safe to call unconditionally (e.g. automatically on startup).
//
// confirm gates whether init proceeds at all — the caller decides how (a
// CLI stdin prompt, a TUI screen, or a func that always approves when
// there's no reason to ask, e.g. auto-init on startup against an empty
// project). confirmAutoMerge gates the one-time auto-merge policy prompt on
// a fresh project; kept as a separate parameter because unlike confirm, it
// must never silently default to "enabled" just because nothing could ask
// (see its own doc comment). ok=false with err=nil means confirm declined.
func (a *app) Init(ctx context.Context, confirm func(string) (bool, error), confirmAutoMerge func() (bool, error)) (res initResult, ok bool, err error) {
	// 1. init only makes sense inside a git work tree.
	if err := a.checkGitRepo(ctx); err != nil {
		return initResult{}, false, err
	}

	// 2. confirm before touching the directory; --yes skips the prompt.
	approved, err := confirm(fmt.Sprintf("initialize baron in %s", a.dir))
	if err != nil {
		return initResult{}, false, err
	}
	if !approved {
		return initResult{}, false, nil
	}

	// 3. write .baron/config.toml with defaults when absent, assigning the
	// detected gate profile and asking about auto-merge. An existing config
	// is left untouched — both only happen on a project's first init.
	if err := a.initConfigDefaults(confirmAutoMerge); err != nil {
		return initResult{}, false, err
	}

	// 4. detect or initialize the beads workspace (bd init).
	workspace, created, err := a.initBeadsWorkspace(ctx)
	if err != nil {
		return initResult{}, false, err
	}

	cfg, err := a.loadConfig(a.dir)
	if err != nil {
		return initResult{}, false, err
	}

	// 5+5b. doctor's mandatory-dependency gate and the bd-version drift
	// check — both abort init outright.
	rep, err := a.runInitChecks(ctx, cfg)
	if err != nil {
		return initResult{}, false, err
	}

	// 6. drop a pre-rename project-local agent cache, so nothing is left
	// claiming to be the source of truth next to the one that is.
	a.removeLegacyModelCache()

	a.auditLog("init", a.dir, "initialized baron workspace")

	return initResult{
		Workspace: workspace,
		Created:   created,
		Profile:   cfg.General.Profile,
		Agents:    rep.Agents,
		Models:    rep.Models,
		Dataflow:  dataflowDeclaration(),
		Checks:    rep.Checks,
	}, true, nil
}

// initConfigDefaults writes .baron/config.toml with defaults when absent,
// assigning the detected gate profile and asking about auto-merge — a
// project's first init only; an existing config is left untouched.
func (a *app) initConfigDefaults(confirmAutoMerge func() (bool, error)) error {
	if _, err := os.Stat(a.configPath()); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	detected := profile.Detect(a.dir).Name
	if err := store.SetConfigField(a.dir, "general.profile", detected); err != nil {
		return err
	}
	autoMerge, err := confirmAutoMerge()
	if err != nil {
		return err
	}
	if autoMerge {
		if err := store.SetConfigField(a.dir, "merge.auto.enabled", "true"); err != nil {
			return err
		}
	}
	return exportBuiltinProfiles(a.dir)
}

// initBeadsWorkspace detects or initializes the beads workspace (bd init),
// reporting the workspace path and whether this call created it.
func (a *app) initBeadsWorkspace(ctx context.Context) (workspace string, created bool, err error) {
	workspace = filepath.Join(a.dir, ".beads")
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		return workspace, false, err
	}
	res, err := a.runner.Run(ctx, "bd", []string{"init", "--quiet", "--skip-agents"}, tool.Options{Dir: a.dir})
	if err != nil {
		return "", false, fmt.Errorf("bd init: %s", strings.TrimSpace(res.Stderr))
	}
	return workspace, true, nil
}

// runInitChecks runs doctor's mandatory-dependency gate (which, in the same
// pass, discovers this machine's agent CLIs and the models they can run,
// writing both to the machine-wide cache in ~/.cache/baron — nothing
// agent-related is written under .baron/, since which CLIs are installed is
// a fact about the machine and identical in every checkout, and that cache
// is what makes the TUI's assign picker open instantly instead of spawning
// a provider CLI on every keypress) and the bd-version drift check. Either
// failing aborts init outright.
func (a *app) runInitChecks(ctx context.Context, cfg *store.Config) (doctor.Report, error) {
	rep := doctor.Run(ctx, a.dir, a.runner, cfg.Agents)
	if msg := mandatoryMissing(rep.Checks); msg != "" {
		return doctor.Report{}, fmt.Errorf("init aborted: %s", msg)
	}
	if err := a.beads.CheckDrift(ctx, doctor.PinnedBDVersion()); err != nil {
		return doctor.Report{}, fmt.Errorf("init aborted: %w", err)
	}
	return rep, nil
}

// exportBuiltinProfiles writes every built-in language's gate profile
// (internal/profile's registry: go, react-ts, rust, php, python — not just
// the one detected for dir) as an editable .toml file, one per language, to
// both dir's project-local drop-in directory and the machine-wide user one.
// Each write is a no-op if that file already exists (store.WriteProfileFile
// never overwrites), so re-running init, or running it in a second project
// on the same machine, never clobbers a profile someone has since hand-
// edited — the whole point is that from here on these are just files: no
// Go code, no recompiling BARON, to add or tweak a language's pipeline.
func exportBuiltinProfiles(dir string) error {
	userDir, err := store.UserConfigDir()
	if err != nil {
		return err
	}
	dirs := []string{store.ProjectProfilesDir(dir), filepath.Join(userDir, store.ProfilesDirName)}

	for _, p := range profile.BuildAll(dir) {
		steps := make([]store.GateStepConfig, len(p.Steps))
		for i, s := range p.Steps {
			steps[i] = store.GateStepConfig{Name: s.Name, Command: s.Command, Args: s.Args, Timeout: s.Timeout}
		}
		for _, d := range dirs {
			if err := store.WriteProfileFile(d, p.Name, store.Profile{Steps: steps}); err != nil {
				return fmt.Errorf("export %s profile to %s: %w", p.Name, d, err)
			}
		}
	}
	return nil
}

// removeLegacyModelCache deletes .baron/models.json, where BARON used to
// cache this machine's agent CLIs once per project. The cache now lives in
// ~/.cache/baron (see agent.CacheDir), and a stale project-local copy left
// behind would only invite hand-editing a file nothing reads. Best-effort:
// a file that won't delete is not worth failing init over.
func (a *app) removeLegacyModelCache() {
	path := filepath.Join(a.dir, ".baron", "models.json")
	if _, err := os.Stat(path); err != nil {
		return
	}
	if err := os.Remove(path); err != nil {
		a.warn("could not remove legacy %s: %v", path, err)
		return
	}
	a.outf("Removed legacy %s (agent discovery is machine-wide now)\n", path)
}

// confirmAutoMerge asks whether to enable merge.auto.enabled on a fresh
// project — the one first-run question BARON always asks for real (see
// Run), never auto-approved: it changes the project's merge policy for
// good (every future bead, no more per-merge human review unless the
// project edits .baron/config.toml back). It defaults to false outside a
// TTY, since there's no one to ask.
func (a *app) confirmAutoMerge() (bool, error) {
	if !a.inTTY() {
		return false, nil
	}
	var value bool
	confirm := huh.NewConfirm().
		Title("Enable auto-merge for beads that pass the gate?").
		Description("A bead (or an epic, once every child is done) merges itself the moment its gate passes — no per-merge human review. Change this later in .baron/config.toml (merge.auto.enabled).").
		Affirmative("yes").
		Negative("no").
		Value(&value)
	err := huh.NewForm(huh.NewGroup(confirm)).
		WithInput(a.in).
		WithOutput(a.out).
		Run()
	return value, err
}

// checkGitRepo errors when dir is not inside a git work tree.
func (a *app) checkGitRepo(ctx context.Context) error {
	res, err := a.runner.Run(ctx, "git", []string{"rev-parse", "--is-inside-work-tree"}, tool.Options{Dir: a.dir})
	if err != nil {
		return fmt.Errorf("not a git repository (%s): %s", a.dir, strings.TrimSpace(res.Stderr))
	}
	if strings.TrimSpace(res.Stdout) != "true" {
		return fmt.Errorf("not a git repository: %s is outside any git work tree", a.dir)
	}
	return nil
}

// mandatoryMissing names the required dependencies the doctor check found
// broken, or "" when every mandatory dependency is healthy.
func mandatoryMissing(checks []doctor.CheckResult) string {
	missing := make([]string, 0, 2)
	for _, c := range checks {
		if c.Required && c.Severity == doctor.SeverityError {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("mandatory dependencies missing: %s (run `baron doctor`)", strings.Join(missing, ", "))
}
