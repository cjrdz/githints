package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func chdirTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestInitHookScript(t *testing.T) {
	dir := chdirTempRepo(t)

	if err := cmdInit([]string{}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}

	hook, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatalf("read post-commit hook: %v", err)
	}
	script := string(hook)
	if !strings.Contains(script, managedHookMarker) {
		t.Errorf("hook missing managed marker")
	}
	if strings.Contains(script, "\\") {
		t.Errorf("hook contains backslashes; expected forward-slash path: %s", script)
	}
}

func TestInitGitignorePrivate(t *testing.T) {
	dir := chdirTempRepo(t)

	if err := cmdInit([]string{}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), gitignoreManagedStart) {
		t.Errorf(".gitignore missing managed block")
	}
	if !strings.Contains(string(data), ".githints/") {
		t.Errorf("private mode should ignore .githints/")
	}
}

func TestInitGitignoreShared(t *testing.T) {
	dir := chdirTempRepo(t)

	if err := cmdInit([]string{"-share"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, gitignoreManagedStart) {
		t.Errorf(".gitignore missing managed block")
	}

	lines := strings.Split(s, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == ".githints/" {
			t.Errorf("shared mode should not ignore .githints/ wholesale")
		}
	}
	for _, want := range []string{".githints/store.db*", ".githints/.salt", ".githints/config.json"} {
		found := false
		for _, line := range lines {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("shared mode should ignore %s", want)
		}
	}
}

func TestRenderCommand(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)

	if err := cmdInit([]string{}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdRecord([]string{"-file=main.go", "-summary=add main"}); err != nil {
		t.Fatalf("cmdRecord: %v", err)
	}

	changesPath := filepath.Join(dir, ".githints", "CHANGES.md")
	if _, err := os.Stat(changesPath); err != nil {
		t.Fatalf("CHANGES.md not rendered after record: %v", err)
	}
	if err := os.Remove(changesPath); err != nil {
		t.Fatalf("remove CHANGES.md: %v", err)
	}

	if err := cmdRender(); err != nil {
		t.Fatalf("cmdRender: %v", err)
	}
	if _, err := os.Stat(changesPath); err != nil {
		t.Fatalf("CHANGES.md not rendered after render command: %v", err)
	}
}
