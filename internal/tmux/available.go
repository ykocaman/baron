package tmux

import (
	"context"
	"strings"

	"github.com/baron-cli/baron/internal/tool"
)

// Available reports whether tmux is installed: `tmux -V` exits 0 and its
// output names tmux (e.g. "tmux 3.4"). Doctor uses this for the install
// check.
func Available(ctx context.Context, runner tool.Runner) bool {
	res, err := runner.Run(ctx, "tmux", []string{"-V"}, tmuxOpts)
	return err == nil && strings.Contains(res.Stdout, "tmux")
}

// Usable reports whether tmux can actually be used for a launch: the
// tui.tmux config setting hasn't opted out ("never"), and the binary is
// actually installed. tmuxSetting is cfg.TUI.Tmux — passed as a plain
// string rather than *store.Config to avoid an import cycle (store already
// imports domain, which imports tmux).
func Usable(ctx context.Context, runner tool.Runner, tmuxSetting string) bool {
	return tmuxSetting != "never" && Available(ctx, runner)
}
