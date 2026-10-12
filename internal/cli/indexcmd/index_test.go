package indexcmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/index/lang"
)

func TestCmdIndexLanguagesListsEveryRegisteredLanguage(t *testing.T) {
	out, err := clitest.CaptureStdout(t, func() error { return Languages(nil) })
	if err != nil {
		t.Fatalf("Languages: %v", err)
	}

	r := lang.NewRegistry()
	for _, name := range r.Languages() {
		if !strings.Contains(out, name) {
			t.Errorf("output does not mention language %q:\n%s", name, out)
		}
		for _, ext := range r.ForLanguage(name).Extensions() {
			if !strings.Contains(out, ext) {
				t.Errorf("output does not mention %s extension %q:\n%s", name, ext, out)
			}
		}
	}
}

func TestCmdIndexLanguagesRejectsExtraArgs(t *testing.T) {
	if _, err := clitest.CaptureStdout(t, func() error {
		return Languages([]string{"go"})
	}); err == nil {
		t.Error("expected an error for an unexpected argument")
	}
}

// TestCmdIndexRejectsUnknownSubcommand guards a real footgun: flag.Parse stops
// at the first non-flag argument, so before this check a mistyped subcommand
// fell through to a full rebuild, which clears the index and VACUUMs the file.
func TestCmdIndexRejectsUnknownSubcommand(t *testing.T) {
	err := Run([]string{"langauges"})
	if err == nil {
		t.Fatal("expected an error for a mistyped subcommand")
	}
	if !strings.Contains(err.Error(), "langauges") {
		t.Errorf("error should name the bad subcommand, got: %v", err)
	}
}

func TestIndexDBPath(t *testing.T) {
	got := lang.IndexDBPath("/repo")
	want := filepath.Join("/repo", ".githints", "index.db")
	if got != want {
		t.Errorf("IndexDBPath = %q, want %q", got, want)
	}
}

func TestIndexNotePath(t *testing.T) {
	root := "/repo"
	got, collision, err := lang.IndexNotePath(root, "cmd/api/main.go")
	if err != nil {
		t.Fatalf("IndexNotePath: %v", err)
	}
	if collision {
		t.Fatal("expected no collision")
	}
	want := filepath.Join("/repo", ".githints", "index", "cmd", "api", "main.go.md")
	if got != want {
		t.Errorf("IndexNotePath = %q, want %q", got, want)
	}
}

func TestIndexNotePathCollision(t *testing.T) {
	_, collision, err := lang.IndexNotePath("/repo", "index/sneaky.go")
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

func TestIndexNotePathAbsoluteRejected(t *testing.T) {
	_, collision, err := lang.IndexNotePath("/repo", "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if collision {
		t.Fatal("expected collision=false for absolute path")
	}
}

func TestIndexNotePathTraversalRejected(t *testing.T) {
	_, collision, err := lang.IndexNotePath("/repo", "../escape.go")
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if collision {
		t.Fatal("expected collision=false for traversal path")
	}
}
