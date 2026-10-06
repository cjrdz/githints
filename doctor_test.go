package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func doctorLevels(results []checkResult) map[string]checkLevel {
	worst := map[string]checkLevel{}
	for _, r := range results {
		if r.level > worst[r.name] {
			worst[r.name] = r.level
		}
	}
	return worst
}

func TestDoctorBeforeInit(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	chdirTempRepo(t)
	if got := doctorLevels(runDoctor()); got["init"] != levelFail {
		t.Fatalf("doctor before init: %v", got)
	}
}

func TestDoctorHealthyRepo(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	if err := cmdRecord([]string{"-file=a.go", "-summary=x"}); err != nil {
		t.Fatal(err)
	}
	for name, level := range doctorLevels(runDoctor()) {
		if level == levelFail {
			t.Errorf("%s failed on a freshly initialized repo", name)
		}
	}

	// A hook that lost its execute bit is skipped by git without a word.
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := doctorLevels(runDoctor()); got["hooks"] != levelFail && runtime.GOOS != "windows" {
		t.Errorf("non-executable hook not reported: %v", got)
	}
}

// doctor is a diagnostic: it must not create a salt as a side effect.
func TestDoctorIsReadOnly(t *testing.T) {
	saltDir := t.TempDir()
	t.Setenv("GITHINTS_SALT_DIR", saltDir)
	chdirTempRepo(t)
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(saltDir)
	for _, e := range entries {
		if err := os.Remove(filepath.Join(saltDir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	runDoctor()
	if entries, _ := os.ReadDir(saltDir); len(entries) != 0 {
		t.Fatalf("doctor created %d salt file(s)", len(entries))
	}
}
