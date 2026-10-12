package read

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/clitest"
)

// The CLI diff is what CLI-mode agents read, so it carries the same
// guarantee as get_diff: credential files are redacted on the default path.
func TestCLIDiffScrubsUnconditionally(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", ".env")
	clitest.RunGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "env")
	if err := os.WriteFile(env, []byte("A=1\nDB_PASSWORD=hunter2-very-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := clitest.CaptureStdout(t, func() error { return Diff([]string{"-file=.env"}) })
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "REDACTED") {
		t.Fatalf("diff was not scrubbed:\n%s", out)
	}
}

func TestCLIReadCommandsValidate(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"history without file": History(nil),
		"history traversal":    History([]string{"-file=../x"}),
		"recent limit 0":       Recent([]string{"-limit=0"}),
		"search empty":         Search(nil),
		"search too long":      Search([]string{"-query=" + strings.Repeat("a", 501)}),
		"diff flag as hash":    Diff([]string{"-file=a.go", "-hash=--output=/tmp/x"}),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := record.Record([]string{"-file=a.go", "-summary=rotate signing key", "-reason=security"}); err != nil {
		t.Fatal(err)
	}
	out, err := clitest.CaptureStdout(t, func() error { return Search([]string{"-query=signing"}) })
	if err != nil || !strings.Contains(out, "rotate signing key") || !strings.Contains(out, "why: security") {
		t.Fatalf("search: %v\n%s", err, out)
	}
}
