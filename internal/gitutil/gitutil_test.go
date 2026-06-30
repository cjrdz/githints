package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	return func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}
}

func TestChangedFiles(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string) string
		wantFile string
	}{
		{
			name: "first commit falls back to empty tree",
			setup: func(t *testing.T, dir string) string {
				writeFile(t, dir, "main.go", "package main\n")
				runGit(t, dir, "add", "main.go")
				runGit(t, dir, "commit", "-m", "initial")
				return LastCommitHash()
			},
			wantFile: "main.go",
		},
		{
			name: "second commit diff against parent",
			setup: func(t *testing.T, dir string) string {
				writeFile(t, dir, "main.go", "package main\n")
				runGit(t, dir, "add", "main.go")
				runGit(t, dir, "commit", "-m", "initial")
				writeFile(t, dir, "auth.go", "package auth\n")
				runGit(t, dir, "add", "auth.go")
				runGit(t, dir, "commit", "-m", "add auth")
				return LastCommitHash()
			},
			wantFile: "auth.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := makeRepo(t)
			restore := chdir(t, dir)
			defer restore()

			hash := tt.setup(t, dir)
			if hash == "" {
				t.Fatal("empty commit hash")
			}

			files, err := ChangedFiles(hash)
			if err != nil {
				t.Fatalf("ChangedFiles: %v", err)
			}
			found := false
			for _, f := range files {
				if f == tt.wantFile {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected %q in %v", tt.wantFile, files)
			}
		})
	}
}

func TestParseDiffStat(t *testing.T) {
	tests := []struct {
		stat    string
		wantAdd int
		wantDel int
		wantOK  bool
	}{
		{"+5 -2", 5, 2, true},
		{"+0 -0", 0, 0, true},
		{"", 0, 0, false},
		{"+5", 0, 0, false},
		{"binary", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.stat, func(t *testing.T) {
			add, del, ok := ParseDiffStat(tt.stat)
			if ok != tt.wantOK {
				t.Fatalf("ParseDiffStat(%q) ok = %v, want %v", tt.stat, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if add != tt.wantAdd || del != tt.wantDel {
				t.Errorf("got +%d -%d, want +%d -%d", add, del, tt.wantAdd, tt.wantDel)
			}
		})
	}
}

func TestWorktreeDiffStat(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")

	stat := WorktreeDiffStat("main.go")
	if stat == "" {
		t.Fatal("expected non-empty worktree diff stat")
	}
	add, _, ok := ParseDiffStat(stat)
	if !ok {
		t.Fatalf("could not parse stat %q", stat)
	}
	if add < 1 {
		t.Errorf("add = %d, want >= 1", add)
	}
}

func TestRepoRoot(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if root != dir {
		t.Errorf("RepoRoot = %q, want %q", root, dir)
	}
}
