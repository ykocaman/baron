package tui

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"
)

// Run starts the TUI program and blocks until the user quits. Callers must
// only invoke this in a TTY — non-TTY handling is the caller's
// responsibility, matching how `baron`/`baron
// tui` already decide TTY-vs-plain-output for every other command. in/out
// are always wired explicitly (tea.WithInput/WithOutput accept any
// io.Reader/io.Writer) — never fall back to the process's real os.Stdin/
// os.Stdout, which would be wrong when Run is driven by something other
// than the live terminal (as in tests).
func Run(ctx context.Context, deps Deps, in io.Reader, out io.Writer) error {
	m := New(ctx, deps)
	// Alt screen and mouse capture are declared on the View (View fields in
	// v2): View() sets AltScreen + MouseModeCellMotion so the app can scroll
	// its own panes; without mouse capture the wheel falls through to the
	// terminal's scrollback (the "terminal text shows behind the UI" symptom).
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out))
	_, err := p.Run()
	return err
}
