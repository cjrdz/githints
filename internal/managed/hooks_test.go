package managed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/clitest"
)

// A hook githints installed but that is out of date is upgrade's job: upgrade
// repoints and rewrites every hook carrying the marker, while init is for a
// repository without hooks. doctor used to send people to init for all of them.
func TestCheckHooksSendsStaleHooksToUpgrade(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	// PATH holds git and nothing else, so githints is not on it: the variant
	// where the hook has nothing to fall back to.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, filepath.Base(git))); err != nil {
		t.Skipf("cannot link git: %v", err)
	}
	t.Setenv("PATH", bin)
	gone := filepath.Join(t.TempDir(), "githints")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("post-commit", HookScriptFor(gone, "hook-run"))
	write("pre-commit", "#!/bin/sh\n# "+HookMarker+"\ngithints hook-precommit\n") // no githints_bin line: an older githints

	got, err := CheckHooks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"post-commit": "which is gone", "pre-commit": "older githints"}
	for _, h := range got {
		detail, ok := want[h.Name]
		if !ok {
			continue
		}
		if !strings.Contains(h.Detail, detail) {
			t.Errorf("%s: detail %q, want %q", h.Name, h.Detail, detail)
		}
		if !strings.HasPrefix(h.Fix, "githints upgrade") {
			t.Errorf("%s: fix %q, want githints upgrade", h.Name, h.Fix)
		}
		delete(want, h.Name)
	}
	if len(want) != 0 {
		t.Errorf("no status for %v in %+v", want, got)
	}

	// Not installed at all stays init's: upgrade skips a missing hook.
	if err := os.Remove(filepath.Join(hooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	got, _ = CheckHooks(dir)
	for _, h := range got {
		if h.Name == "pre-commit" && h.Fix != "githints init" {
			t.Errorf("missing pre-commit: fix %q, want githints init", h.Fix)
		}
	}
}
