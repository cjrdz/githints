package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

// The staged change is the candidate: a staged README is low, and staging a
// line of auth code beside it makes the whole change high.
func TestReviewAssessesTheStagedChange(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		clitest.RunGit(t, dir, "add", rel)
	}

	out, err := clitest.CaptureStdout(t, func() error { return Status(nil) })
	if err != nil || !strings.Contains(out, "nothing staged") {
		t.Fatalf("empty index: %v\n%s", err, out)
	}
	write("README.md", "# hello\n")
	out, err = clitest.CaptureStdout(t, func() error { return Status(nil) })
	if err != nil || !strings.Contains(out, "Risk: low") {
		t.Fatalf("readme: %v\n%s", err, out)
	}
	write("internal/auth/token.go", "package auth\n")
	out, err = clitest.CaptureStdout(t, func() error { return Status(nil) })
	if err != nil || !strings.Contains(out, "Risk: high") || !strings.Contains(out, "all four lenses") {
		t.Fatalf("auth: %v\n%s", err, out)
	}
	if err := Status([]string{"../outside"}); err == nil {
		t.Error("a path outside the repository was accepted")
	}
}
