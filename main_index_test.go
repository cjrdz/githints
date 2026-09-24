package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/index/lang"
)

// captureStdout redirects os.Stdout for the duration of fn and returns what
// it wrote. Safe only for small outputs: fn runs to completion before the
// pipe is drained, so anything past the pipe buffer would deadlock.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	runErr := fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(out), runErr
}

// TestCmdIndexLanguagesListsEveryRegisteredLanguage pins the property that
// makes the command worth having: whatever the registry knows, the command
// prints. A language added without appearing here would leave users no way to
// discover it.
func TestCmdIndexLanguagesListsEveryRegisteredLanguage(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdIndexLanguages(nil) })
	if err != nil {
		t.Fatalf("cmdIndexLanguages: %v", err)
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
	if _, err := captureStdout(t, func() error {
		return cmdIndexLanguages([]string{"go"})
	}); err == nil {
		t.Error("expected an error for an unexpected argument")
	}
}

// TestCmdIndexRejectsUnknownSubcommand guards a real footgun: flag.Parse stops
// at the first non-flag argument, so before this check a mistyped subcommand
// fell through to a full rebuild, which clears the index and VACUUMs the file.
func TestCmdIndexRejectsUnknownSubcommand(t *testing.T) {
	err := cmdIndex([]string{"langauges"})
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
