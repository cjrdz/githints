package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/integrity"
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
