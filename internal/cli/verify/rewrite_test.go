package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildBinary compiles githints, so git can run the real hooks init installs.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "githints")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/cjrdz/githints")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// What a rewritten history does to the log, with the hooks git actually runs.
// verify stays clean through all of it: commit_hash is outside the HMAC, and
// each anchor names the commit it was written for, which a rewrite does not
// touch. What a rewrite does change is documented in docs/usage.md ("History
// rewrites"): rows keep the hash of the commit they were stamped with, and
// git replays commits through the post-commit hook, which adds a fallback row
// for each replayed commit since its records were already used up.
func TestHistoryRewritesKeepVerifyClean(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary and runs real git hooks")
	}
	bin := buildBinary(t)
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := t.TempDir()

	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gh := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	commit := func(file, summary string) string {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(summary + "\n") // a write error surfaces as an empty commit below
		f.Close()
		if out, err := gh("record", "-file="+file, "-summary="+summary); err != nil {
			t.Fatalf("record: %v\n%s", err, out)
		}
		git("add", file)
		git("commit", "-q", "-m", summary)
		return git("rev-parse", "HEAD")
	}
	verifyClean := func(after string) {
		t.Helper()
		if out, err := gh("verify"); err != nil {
			t.Errorf("verify after %s: %v\n%s", after, err, out)
		}
	}
	history := func(file string) string {
		t.Helper()
		out, err := gh("log", "-file="+file)
		if err != nil {
			t.Fatalf("log: %v\n%s", err, out)
		}
		return out
	}

	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test User")
	if out, err := gh("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	first := commit("a.go", "add a")
	verifyClean("a first commit")

	// amend: the new record goes to the amended commit; the earlier one keeps
	// the hash of the commit the amend replaced.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("amended\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gh("record", "-file=a.go", "-summary=amend a"); err != nil {
		t.Fatalf("record: %v\n%s", err, out)
	}
	git("add", "a.go")
	git("commit", "-q", "--amend", "-m", "add a (amended)")
	verifyClean("commit --amend")
	if h := history("a.go"); !strings.Contains(h, first[:8]) || strings.Contains(h, "fallback") {
		t.Errorf("after amend, a.go should keep its row on %s and gain no fallback row:\n%s", first[:8], h)
	}

	// rebase: each replayed commit runs post-commit and gets a fallback row.
	git("checkout", "-q", "-b", "topic")
	commit("b.go", "add b")
	commit("b.go", "grow b")
	git("checkout", "-q", "main")
	commit("c.go", "add c")
	git("checkout", "-q", "topic")
	git("rebase", "-q", "main")
	verifyClean("rebase")
	if h := history("b.go"); strings.Count(h, "(agent,") != 2 || strings.Count(h, "(fallback,") != 2 {
		t.Errorf("after rebase, b.go should have its 2 agent rows and 2 fallback rows:\n%s", h)
	}

	// cherry-pick onto another base, and an interactive squash.
	git("checkout", "-q", "-b", "side", "main")
	commit("e.go", "add e")
	git("checkout", "-q", "main")
	commit("f.go", "add f")
	git("cherry-pick", "side")
	verifyClean("cherry-pick")
	if h := history("e.go"); strings.Count(h, "(agent,") != 1 || strings.Count(h, "(fallback,") != 1 {
		t.Errorf("after cherry-pick, e.go should have its agent row and one fallback row:\n%s", h)
	}
	git("checkout", "-q", "topic")
	commit("d.go", "add d")
	commit("d.go", "grow d")
	cmd := exec.Command("git", "rebase", "-q", "-i", "HEAD~2")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR="+sequenceEditor(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rebase -i: %v\n%s", err, out)
	}
	verifyClean("an interactive squash")
	if n := strings.Count(git("log", "--oneline", "main..topic"), "\n") + 1; n != 3 {
		t.Fatalf("squash did not happen: %d commits on topic", n)
	}

	// gc: the replaced commits are gone; verify does not need them.
	git("reflog", "expire", "--expire=now", "--all")
	git("gc", "-q", "--prune=now")
	verifyClean("gc --prune=now")
	if out, err := gh("diff", "-file=a.go", "-hash="+first); err == nil {
		t.Errorf("diff of a commit gc removed should fail, got:\n%s", out)
	}
}

// sequenceEditor writes a script that turns the second pick of a todo list
// into a squash, as a person would in their editor.
func sequenceEditor(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "squash.sh")
	body := "#!/bin/sh\nawk 'NR==2 && /^pick/ {sub(/^pick/, \"squash\")} {print}' \"$1\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "sh " + filepath.ToSlash(script)
}
