package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/managed"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

func doctorLevels(results []Result) map[string]Level {
	worst := map[string]Level{}
	for _, r := range results {
		if r.Level > worst[r.Name] {
			worst[r.Name] = r.Level
		}
	}
	return worst
}

func TestDoctorBeforeInit(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	clitest.ChdirTempRepo(t)
	if got := doctorLevels(runChecks()); got["init"] != LevelFail {
		t.Fatalf("doctor before init: %v", got)
	}
}

func TestDoctorHealthyRepo(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := integrity.KeyFromRepo(dir)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := recorder.Record(st, dir, key, recorder.Input{FilePath: "a.go", Summary: "x", Source: "agent"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	for name, level := range doctorLevels(runChecks()) {
		if level == LevelFail {
			t.Errorf("%s failed on a freshly initialized repo", name)
		}
	}

	// A hook that lost its execute bit is skipped by git without a word.
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := doctorLevels(runChecks()); got["hooks"] != LevelFail && runtime.GOOS != "windows" {
		t.Errorf("non-executable hook not reported: %v", got)
	}
}

func TestDoctorPointsStaleSetupAtUpgrade(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	if got := doctorLevels(runChecks()); got["hooks"] != LevelOK || got["blocks"] != LevelOK {
		t.Fatalf("fresh init: %v", got)
	}

	other := filepath.Join(t.TempDir(), "githints")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte(managed.HookScriptFor(other, "hook-run")), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(dir, "AGENTS.md")
	data, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agents, []byte(strings.Replace(string(data), "## Change tracking with githints", "## Change tracking (old text)", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var details []string
	for _, r := range runChecks() {
		if r.Level == LevelWarn && (r.Name == "hooks" || r.Name == "blocks") {
			if r.Fix != "githints upgrade" && r.Fix != "githints upgrade (repoints the hooks)" {
				t.Errorf("%s warning %q points at %q, not githints upgrade", r.Name, r.Detail, r.Fix)
			}
			details = append(details, r.Detail)
		}
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{"post-commit runs " + other, "block in AGENTS.md differs", ".gitignore has no githints block"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning containing %q in:\n%s", want, joined)
		}
	}
}

// Only a full scan stamps the resolver version, so an index the hook alone
// built reads 0; that is "never fully built", not "an older githints".
func TestIndexBuiltOnlyByCommitsIsNotCalledOld(t *testing.T) {
	dir := t.TempDir()
	if got := IndexRebuildReason(dir, []string{"auto"}); got != "" {
		t.Errorf("no index: %q", got)
	}
	if _, err := os.Stat(lang.IndexDBPath(dir)); err == nil {
		t.Error("IndexRebuildReason created index.db")
	}
	db, err := index.Open(lang.IndexDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := IndexRebuildReason(dir, []string{"auto"}); !strings.Contains(got, "never a full scan") || strings.Contains(got, "older") {
		t.Errorf("reason = %q", got)
	}
}

// doctor is a diagnostic: it must not create a salt as a side effect.
func TestDoctorIsReadOnly(t *testing.T) {
	saltDir := t.TempDir()
	t.Setenv("GITHINTS_SALT_DIR", saltDir)
	clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(saltDir)
	for _, e := range entries {
		if err := os.Remove(filepath.Join(saltDir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	runChecks()
	if entries, _ := os.ReadDir(saltDir); len(entries) != 0 {
		t.Fatalf("doctor created %d salt file(s)", len(entries))
	}
}
