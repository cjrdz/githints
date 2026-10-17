package notecmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func initRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return dir
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return capture(t, func() error { return Run(args) })
}

func TestNoteLifecycle(t *testing.T) {
	dir := initRepo(t)

	out, err := run(t, "record", "-title=Why we cache", "-body=Cache the token lookup, see [[internal/auth/token.go]].",
		"-tags=perf,auth", "-files=internal/auth/token.go", "-changes=3,4")
	if err != nil || !strings.Contains(out, "recorded note why-we-cache") {
		t.Fatalf("record: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints", "notes", "why-we-cache.md")); err != nil {
		t.Errorf("note file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints", "notes.html")); err != nil {
		t.Errorf("viewer not written: %v", err)
	}

	for _, args := range [][]string{
		{"list"},
		{"search", "-query=token"},
		{"search", "-file=internal/auth/token.go"},
		{"search", "-tag=perf"},
	} {
		out, err := run(t, args...)
		if err != nil || !strings.Contains(out, "why-we-cache") || !strings.Contains(out, "Why we cache") {
			t.Errorf("%v: %q, %v", args, out, err)
		}
	}
	out, err = run(t, "get", "why-we-cache")
	if err != nil || !strings.Contains(out, "Cache the token lookup") || !strings.Contains(out, "#3, #4") {
		t.Errorf("get: %q, %v", out, err)
	}
	if out, _ := run(t, "search", "-query=kubernetes"); !strings.Contains(out, "no matching notes") {
		t.Errorf("empty search: %q", out)
	}

	if _, err := run(t, "record", "-title=Why we cache", "-body=again"); err == nil {
		t.Error("recording over an existing note succeeded without -replace")
	}
	if _, err := run(t, "record", "-title=Why we cache", "-body=replaced", "-replace"); err != nil {
		t.Errorf("-replace: %v", err)
	}
	if out, _ := run(t, "get", "why-we-cache"); !strings.Contains(out, "replaced") {
		t.Errorf("replace did not take: %q", out)
	}

	if out, err := run(t, "delete", "why-we-cache"); err != nil || !strings.Contains(out, "deleted") {
		t.Errorf("delete: %q, %v", out, err)
	}
	if out, _ := run(t, "list"); !strings.Contains(out, "no notes yet") {
		t.Errorf("list after delete: %q", out)
	}
}

func TestNoteRecordReadsBodyFromStdin(t *testing.T) {
	initRepo(t)
	r, w, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() {
		io.WriteString(w, "body from a pipe\n")
		w.Close()
	}()
	if out, err := run(t, "record", "-title=Piped", "-body=-"); err != nil {
		t.Fatalf("record: %q, %v", out, err)
	}
	if out, _ := run(t, "get", "piped"); !strings.Contains(out, "body from a pipe") {
		t.Errorf("stdin body not stored: %q", out)
	}
}

func TestNoteCommandsRefuseBadInput(t *testing.T) {
	initRepo(t)
	for _, args := range [][]string{
		{},
		{"nope"},
		{"record"},
		{"record", "-title=T", "-name=../../x"},
		{"record", "-title=T", "-body=AKIAIOSFODNN7EXAMPLE"},
		{"record", "-title=T", "-changes=abc"},
		{"record", "-title=T", "-files=../../etc/passwd"},
		{"get"},
		{"get", "../../etc/passwd"},
		{"get", "missing"},
		{"delete"},
		{"delete", "missing"},
		{"list", "stray"},
		{"search", "-limit=5000"},
	} {
		if _, err := run(t, args...); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

// Only init creates .githints/: a note command in a repository githints was
// never set up in must refuse, and must not leave a directory behind.
func TestNoteCommandsRefuseAnUninitializedRepository(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	for _, args := range [][]string{{"list"}, {"record", "-title=T", "-body=x"}, {"search", "-query=x"}, {"render"}} {
		if _, err := run(t, args...); err == nil {
			t.Errorf("%v succeeded in an uninitialized repository", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints")); err == nil {
		t.Error("a note command created .githints/")
	}
}

// A hand-written note is picked up, and a bad one is reported rather than
// served.
func TestNoteListFollowsHandEditsAndReportsProblems(t *testing.T) {
	dir := initRepo(t)
	notes := filepath.Join(dir, ".githints", "notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(notes, "by-hand.md"), []byte("# Written by hand\n\ntext\n"), 0o600)
	os.WriteFile(filepath.Join(notes, "leaky.md"), []byte("key AKIAIOSFODNN7EXAMPLE\n"), 0o600)

	out, err := run(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "by-hand") || strings.Contains(out, "leaky") {
		t.Errorf("list = %q, want only the clean note", out)
	}
	if page, err := os.ReadFile(filepath.Join(dir, ".githints", "notes.html")); err != nil || !strings.Contains(string(page), "Written by hand") {
		t.Errorf("the viewer did not pick up the hand-written note: %v", err)
	}
}
