package recorder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"githints/internal/store"
)

func TestRecordStoresAgentAsPending(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, Input{
		FilePath: "cmd/api/main.go",
		Summary:  "add handler",
		Reason:   "feature request",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("cmd/api/main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].CommitHash != "" {
		t.Errorf("agent row commit_hash = %q, want empty (pending)", rows[0].CommitHash)
	}
	if rows[0].Source != "agent" {
		t.Errorf("source = %q, want agent", rows[0].Source)
	}

	if _, err := os.Stat(filepath.Join(dir, ".githints", "cmd", "api", "main.go.md")); err != nil {
		t.Fatalf("per-file hint not rendered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints", "CHANGES.md")); err != nil {
		t.Fatalf("changelog not rendered: %v", err)
	}
}

func TestRecordStoresHookWithHash(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, Input{
		FilePath:   "cmd/api/main.go",
		Summary:    "hook fallback",
		Source:     "hook",
		DiffStat:   "+5 -2",
		CommitHash: "deadbeef",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("cmd/api/main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].CommitHash != "deadbeef" {
		t.Errorf("hook row commit_hash = %q, want deadbeef", rows[0].CommitHash)
	}
	if rows[0].DiffStat != "+5 -2" {
		t.Errorf("diff_stat = %q, want +5 -2", rows[0].DiffStat)
	}
}

func TestRecordCapturesAgentDiffStat(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create a real git repo so WorktreeDiffStat can measure the change.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, Input{
		FilePath: "main.go",
		Summary:  "add Run",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].DiffStat == "" {
		t.Errorf("agent row diff_stat is empty, expected working-tree stat")
	}
}

func TestRecordRejectsInvalidSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, Input{FilePath: "x.go", Summary: "x", Source: "other"}); err == nil {
		t.Fatal("expected error for invalid source")
	}
}
