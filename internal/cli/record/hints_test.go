package record

import (
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

// The CLI shows the hints the MCP tool shows, after the line scripts read.
func TestRecordPrintsHints(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}

	out, err := clitest.CaptureStdout(t, func() error {
		return Record([]string{"-file=a.go", "-summary=Updated function"})
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "recorded change #1 for a.go" {
		t.Errorf("first line %q", lines[0])
	}
	for _, want := range []string{"no reason", "generic summary", "githints outcome -id=1 -state=worked|failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, err = clitest.CaptureStdout(t, func() error {
		return Record([]string{"-file=b.go", "-summary=Retry the token refresh once on a 401", "-reason=The gateway drops the first call after a deploy"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "recorded change #2 for b.go" {
		t.Errorf("a good record printed more than its id:\n%s", out)
	}
}
