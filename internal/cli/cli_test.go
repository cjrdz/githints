package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/doctor"
	"github.com/cjrdz/githints/internal/cli/indexcmd"
	"github.com/cjrdz/githints/internal/cli/notecmd"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/cli/verify"
)

func chdirTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	// CI machines have no global identity; commits in tests need one.
	runGit(t, dir, "config", "user.name", "Test User")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestUsageMatchesCommandTable enforces that every command advertised in
// usageText is dispatchable and every dispatchable command is advertised.
// `githints render` shipped broken because usage() promised a command the
// dispatch switch never handled.
func TestUsageMatchesCommandTable(t *testing.T) {
	advertised := map[string]bool{}
	for _, line := range strings.Split(usageText, "\n") {
		// Command lines look like "  githints <name> ...". Continuation
		// lines are indented further and carry no command name.
		if !strings.HasPrefix(line, "  githints ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "  githints "))
		if len(fields) == 0 {
			continue
		}
		advertised[fields[0]] = true
	}

	if len(advertised) == 0 {
		t.Fatal("parsed no commands out of usageText; the parser or the text changed shape")
	}

	for name := range advertised {
		if _, ok := commands[name]; !ok {
			t.Errorf("usage advertises %q but it is not in the command table", name)
		}
	}
	for name := range commands {
		if !advertised[name] && !hiddenCommands[name] {
			t.Errorf("command %q is dispatchable but missing from usage", name)
		}
	}
	// Hidden is a deliberate, listed exception, not a way to lose a command:
	// every hidden name must still dispatch and must not also be advertised.
	for name := range hiddenCommands {
		if _, ok := commands[name]; !ok {
			t.Errorf("hidden command %q is not in the command table", name)
		}
		if advertised[name] {
			t.Errorf("hidden command %q is advertised in usage", name)
		}
	}
}

// Every command but init used to create .githints/store.db as a side effect,
// so `githints serve` in a fresh repository came up with no hooks, no salt,
// and no explanation.
func TestCommandsRequireInit(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)
	for name, run := range map[string]func() error{
		"verify": verify.Run,
		"record": func() error { return record.Record([]string{"-file=a.go", "-summary=x"}) },
		// index.Open creates .githints/ by itself, so these once did.
		"index":        func() error { return indexcmd.Run(nil) },
		"index status": func() error { return indexcmd.Run([]string{"status"}) },
		"index verify": func() error { return indexcmd.Run([]string{"verify"}) },
		"index facets": func() error { return indexcmd.Run([]string{"facets"}) },
		"note list":    func() error { return notecmd.Run([]string{"list"}) },
	} {
		if err := run(); !errors.Is(err, core.ErrNotInitialized) {
			t.Errorf("%s before init: got %v, want ErrNotInitialized", name, err)
		}
	}
	// doctor (and status, its alias) reports a missing setup as a failed
	// check rather than an error value, but must create nothing either.
	_ = doctor.Run() // its verdict is printed; only the side effect matters here
	if _, err := os.Stat(filepath.Join(dir, ".githints")); err == nil {
		t.Error("a command other than init created .githints/")
	}
}
