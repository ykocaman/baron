// Package tool provides uniform subprocess execution for BARON.
package tool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Result captures subprocess output.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// Options configures subprocess execution.
type Options struct {
	Dir      string            // Working directory
	Env      map[string]string // Additional env vars (merged with parent)
	Timeout  time.Duration     // 0 = no timeout
	CleanEnv bool              // If true, strip credential-bearing env vars
	// OnOutput, if set, is called once for every stdout/stderr write the
	// subprocess makes. Used to track liveness for long-running processes
	// (e.g. the silent-death monitor) without buffering or parsing output.
	OnOutput func()
	// Stdin, if non-empty, is written to the subprocess's standard input.
	Stdin string
}

// Run executes a subprocess and captures output.
func Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = outputWriter(&stdout, opts.OnOutput)
	cmd.Stderr = outputWriter(&stderr, opts.OnOutput)

	if opts.CleanEnv {
		cmd.Env = cleanEnv()
	}
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
		Duration: duration,
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			// Killed by signal or failed to start.
			res.ExitCode = -1
		}
	}

	return res, err
}

// isNotInstalled reports whether a Run error means the binary itself was
// never found on PATH, as opposed to a command that ran and failed. Callers
// use it to say "X is not installed" instead of blaming X's output.
func isNotInstalled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	// Fake runners in tests (and any Runner not backed by os/exec) can only
	// report this as text.
	return strings.Contains(err.Error(), "executable file not found")
}

// outputWriter wraps buf so every Write also calls onOutput, when set.
func outputWriter(buf *bytes.Buffer, onOutput func()) io.Writer {
	if onOutput == nil {
		return buf
	}
	return &notifyWriter{buf: buf, onOutput: onOutput}
}

// notifyWriter calls onOutput on every Write, in addition to buffering.
type notifyWriter struct {
	buf      *bytes.Buffer
	onOutput func()
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if n > 0 {
		w.onOutput()
	}
	return n, err
}

// cleanEnv returns the parent environment with credential-bearing variables
// stripped, keeping only safe, non-secret variables.
func cleanEnv() []string {
	base := []string{}
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USER": true,
		"SHELL": true, "TERM": true, "LANG": true,
		"LC_ALL": true, "TMPDIR": true,
	}
	// Keep XDG_* and TERM_PROGRAM
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if keep[k] || strings.HasPrefix(k, "XDG_") || k == "TERM_PROGRAM" {
			base = append(base, e)
		}
	}
	return base
}
