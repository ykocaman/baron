package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baron-cli/baron/internal/tool"
)

// forbiddenMergeRunner wraps mergeTestRunner, serving canned `git config
// --get` values; unlisted keys behave like git does for an unset key (exit
// nonzero).
type forbiddenMergeRunner struct {
	*mergeTestRunner
	gitConfig map[string]string
}

func (r *forbiddenMergeRunner) Run(ctx context.Context, name string, args []string, opts tool.Options) (tool.Result, error) {
	if name == "git" && len(args) > 1 && args[0] == "config" {
		if v, ok := r.gitConfig[args[len(args)-1]]; ok {
			return tool.Result{Stdout: v + "\n"}, nil
		}
		return tool.Result{}, errors.New("exit status 1")
	}
	return r.mergeTestRunner.Run(ctx, name, args, opts)
}

func TestMergeForbiddenGitConfig(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"rerere.autoUpdate", "rerere.autoUpdate", "true", "rerere.autoUpdate"},
		{"merge.renormalize", "merge.renormalize", "true", "merge.renormalize"},
		{"merge.strategy ours", "merge.strategy", "ours", "merge.strategy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &forbiddenMergeRunner{
				mergeTestRunner: &mergeTestRunner{},
				gitConfig:       map[string]string{tc.key: tc.val},
			}
			a := newTestApp(t, r)
			a.yes = true
			err := a.runMerge(context.Background(), "baron-a1b2c3", true)
			if err == nil || !strings.Contains(err.Error(), "merge forbidden") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runMerge() error = %v, want a forbidden-config error naming %s", err, tc.want)
			}
			if len(r.mergeCalls) != 0 {
				t.Error("git merge ran despite a forbidden merge config")
			}
		})
	}
}

func TestMergeForbiddenGitAttributes(t *testing.T) {
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	if err := os.WriteFile(filepath.Join(a.dir, ".gitattributes"), []byte("*.lock merge=ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.yes = true
	err := a.runMerge(context.Background(), "baron-a1b2c3", true)
	if err == nil || !strings.Contains(err.Error(), "merge=ours") {
		t.Fatalf("runMerge() error = %v, want the forbidden attribute named", err)
	}
	if len(r.mergeCalls) != 0 {
		t.Error("git merge ran despite merge=ours in .gitattributes")
	}
}

func TestMergeCleanConfigProceeds(t *testing.T) {
	// No forbidden config anywhere: the pre-flight check passes and the merge
	// runs exactly as before.
	r := &mergeTestRunner{}
	a := newTestApp(t, r)
	a.yes = true
	if err := a.runMerge(context.Background(), "baron-a1b2c3", true); err != nil {
		t.Fatalf("merge error: %v; stderr: %s", err, stderr(t, a))
	}
	if len(r.mergeCalls) != 1 {
		t.Fatalf("git merge calls = %d, want 1", len(r.mergeCalls))
	}
}
