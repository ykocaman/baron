package tmux

import (
	"context"
	"fmt"
	"strings"
)

// CapturePane returns the raw pane content of window, limited to the last
// lines of scrollback via capture-pane's negative -S start line.
func (c *Client) CapturePane(ctx context.Context, window string, lines int) (string, error) {
	res, err := c.Runner.Run(ctx, "tmux", []string{
		"capture-pane", "-t", c.target(window), "-p", "-S", fmt.Sprintf("-%d", lines),
	}, tmuxOpts)
	if err != nil {
		// tmux reports a missing window on stderr ("can't find window: X");
		// fold it into the error so callers can tell a dead window from a
		// transient failure (the raw exec error is just "exit status 1").
		if strings.TrimSpace(res.Stderr) != "" {
			return "", fmt.Errorf("tmux capture-pane: %s", strings.TrimSpace(res.Stderr))
		}
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return res.Stdout, nil
}

// LastLines returns the last lines of window's pane as lines with trailing
// empty lines dropped (a fresh or scrolled pane ends in blank scrollback);
// leading blanks are preserved as screen content.
func (c *Client) LastLines(ctx context.Context, window string, lines int) ([]string, error) {
	out, err := c.CapturePane(ctx, window, lines)
	if err != nil {
		return nil, err
	}
	body := strings.TrimRight(out, "\n")
	if body == "" {
		return []string{}, nil
	}
	return strings.Split(body, "\n"), nil
}
