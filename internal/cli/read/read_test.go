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
		"traversal":         Log([]string{"-file=../x"}),
		"limit 0":           Log([]string{"-limit=0"}),
		"limit 501":         Log([]string{"-limit=501"}),
		"query too long":    Log([]string{"-query=" + strings.Repeat("a", 501)}),
		"bad since":         Log([]string{"-since=yesterday"}),
		"stray argument":    Log([]string{"a.go"}),
		"diff flag as hash": Diff([]string{"-file=a.go", "-hash=--output=/tmp/x"}),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := record.Record([]string{"-file=a.go", "-summary=rotate signing key", "-reason=security"}); err != nil {
		t.Fatal(err)
	}
	if err := record.Record([]string{"-file=b.go", "-summary=signing docs"}); err != nil {
		t.Fatal(err)
	}
	out, err := clitest.CaptureStdout(t, func() error { return Log([]string{"-query=signing", "-file=a.go"}) })
	if err != nil || !strings.Contains(out, "rotate signing key") || !strings.Contains(out, "why: security") {
		t.Fatalf("log: %v\n%s", err, out)
	}
	if strings.Contains(out, "signing docs") {
		t.Errorf("-file did not narrow the search:\n%s", out)
	}
}
