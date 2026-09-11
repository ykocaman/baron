package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunBasicCapture(t *testing.T) {
	res, err := Run(context.Background(), "sh", []string{"-c", "echo out; echo err >&2; exit 3"}, Options{})
	if err == nil {
		t.Fatal("expected error for exit 3")
	}
	if res.Stdout != "out\n" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "out\n")
	}
	if res.Stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "err\n")
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestRunStdin(t *testing.T) {
	res, err := Run(context.Background(), "cat", nil, Options{Stdin: "hello from stdin"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "hello from stdin" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "hello from stdin")
	}
}

func TestRunExitCodeZero(t *testing.T) {
	res, err := Run(context.Background(), "sh", []string{"-c", "exit 0"}, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	res, err := Run(context.Background(), "sh", []string{"-c", "sleep 10"}, Options{Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if res.ExitCode != -1 && res.ExitCode != 124 {
		t.Errorf("exit code = %d, want -1 or 124", res.ExitCode)
	}
	if res.Duration >= 10*time.Second {
		t.Errorf("duration = %v, expected to be killed well before 10s", res.Duration)
	}
}

func TestRunCleanEnv(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "xxx")
	t.Setenv("GH_TOKEN", "xxx")
	t.Setenv("PATH", "/bin:/usr/bin")

	res, err := Run(context.Background(), "sh", []string{"-c", "printf '%s' \"$PATH\"; printf '%s' \"$AWS_SECRET_ACCESS_KEY\"; printf '%s' \"$GH_TOKEN\""}, Options{CleanEnv: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "/bin:/usr/bin" {
		t.Errorf("PATH = %q, want %q", res.Stdout, "/bin:/usr/bin")
	}
	if strings.Contains(res.Stdout, "xxx") {
		t.Errorf("credential env leaked into child: %q", res.Stdout)
	}
}

func TestRunWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), "pwd", nil, Options{Dir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.TrimSpace(res.Stdout)
	want := filepath.Clean(dir)
	if got != want {
		t.Errorf("pwd = %q, want %q", got, want)
	}
}

func TestRunEnvMerge(t *testing.T) {
	res, err := Run(context.Background(), "sh", []string{"-c", "printf '%s' \"$FOO\""}, Options{Env: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "bar" {
		t.Errorf("FOO = %q, want %q", res.Stdout, "bar")
	}
}

func TestCleanEnvStripsCredentials(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_CONFIG_HOME", "/home/test/.config")

	env := cleanEnv()
	joined := strings.Join(env, "\n")

	for _, secret := range []string{"AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if strings.Contains(joined, secret) {
			t.Errorf("cleanEnv leaked %q", secret)
		}
	}
	for _, keep := range []string{"PATH", "HOME", "XDG_CONFIG_HOME"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("cleanEnv dropped %q", keep)
		}
	}
}
