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
				hash, err := LastCommitHash()
				if err != nil {
					t.Fatalf("LastCommitHash: %v", err)
				}
				return hash
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
				hash, err := LastCommitHash()
				if err != nil {
					t.Fatalf("LastCommitHash: %v", err)
				}
				return hash
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
	// macOS temp directories are symlinked via /private and git on Windows
	// returns forward slashes while EvalSymlinks returns backslashes, so
	// normalize both before comparing.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if filepath.ToSlash(root) != filepath.ToSlash(want) {
		t.Errorf("RepoRoot = %q, want %q", root, want)
	}
}

func TestDiffHashAtCommit(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "add Run")

	hash, err := LastCommitHash()
	if err != nil {
		t.Fatalf("LastCommitHash: %v", err)
	}
	h1, err := DiffHash(hash, "main.go")
	if err != nil {
		t.Fatalf("DiffHash: %v", err)
	}
	if h1 == "" {
		t.Fatal("DiffHash returned empty")
	}

	h2, err := DiffHash(hash, "main.go")
	if err != nil {
		t.Fatalf("DiffHash second: %v", err)
	}
	if h1 != h2 {
		t.Fatal("DiffHash not stable")
	}
}

func TestDiffHashWorkingTree(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")

	h, err := WorktreeDiffHash("main.go")
	if err != nil {
		t.Fatalf("WorktreeDiffHash: %v", err)
	}
	if h == "" {
		t.Fatal("WorktreeDiffHash returned empty")
	}
}

func TestAddNote(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	if err := AddNote("refs/notes/githints", "githints-root: abc123"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	out, err := run("notes", "--ref=refs/notes/githints", "show", "HEAD")
	if err != nil {
		t.Fatalf("show note: %v", err)
	}
	if !strings.Contains(out, "abc123") {
		t.Errorf("note missing expected content: %q", out)
	}

	// Overwriting must succeed because we use --force.
	if err := AddNote("refs/notes/githints", "githints-root: def456"); err != nil {
		t.Fatalf("AddNote overwrite: %v", err)
	}
}

func TestCurrentBranch(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		wantOK bool
	}{
		{
			name: "branch after checkout",
			setup: func(t *testing.T, dir string) {
				runGit(t, dir, "checkout", "-b", "feature/x")
			},
			wantOK: true,
		},
		{
			name: "detached head returns empty branch",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "main.go", "package main\n")
				runGit(t, dir, "add", "main.go")
				runGit(t, dir, "commit", "-m", "initial")
				hash, err := LastCommitHash()
				if err != nil {
					t.Fatalf("LastCommitHash: %v", err)
				}
				runGit(t, dir, "checkout", "--detach", hash)
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := makeRepo(t)
			restore := chdir(t, dir)
			defer restore()
			tt.setup(t, dir)

			branch, err := CurrentBranch()
			if err != nil {
				t.Fatalf("CurrentBranch: %v", err)
			}
			if tt.wantOK && branch == "" {
				t.Fatal("expected non-empty branch, got empty")
			}
			if !tt.wantOK && branch != "" {
				t.Errorf("expected empty branch in detached HEAD, got %q", branch)
			}
			if tt.wantOK && branch != "feature/x" {
				t.Errorf("branch = %q, want feature/x", branch)
			}
		})
	}
}

func TestStagedFiles(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	writeFile(t, dir, "a.go", "package a\n")
	writeFile(t, dir, "b.go", "package b\n")
	runGit(t, dir, "add", "a.go", "b.go")

	staged, err := StagedFiles()
	if err != nil {
		t.Fatalf("StagedFiles: %v", err)
	}
	want := map[string]bool{"a.go": true, "b.go": true}
	for _, f := range staged {
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("missing staged files: %v; got %v", want, staged)
	}
}

func TestFileDiffWorkingTree(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")
	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")

	diff, err := FileDiff("", "main.go")
	if err != nil {
		t.Fatalf("FileDiff working tree: %v", err)
	}
	if !strings.Contains(diff, "func Run()") {
		t.Errorf("working-tree diff missing added line:\n%s", diff)
	}
}

func TestIsValidCommitish(t *testing.T) {
	tests := []struct {
		hash string
		want bool
	}{
		{"", true},
		{"abc1234", true},
		{"0123456789abcdef0123456789abcdef01234567", true},
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{"-s", false},
		{"--output=/tmp/pwn", false},
		{"HEAD", false},
		{"HEAD~1", false},
		{"abc", false},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", false},
	}
	for _, tt := range tests {
		t.Run(tt.hash, func(t *testing.T) {
			if got := IsValidCommitish(tt.hash); got != tt.want {
				t.Errorf("IsValidCommitish(%q) = %v, want %v", tt.hash, got, tt.want)
			}
		})
	}
}

func TestFileDiffRejectsFlagHash(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	_, err := FileDiff("--output=/tmp/githints-test-output", "main.go")
	if err == nil {
		t.Fatal("expected error for flag-like hash")
	}
	if !strings.Contains(err.Error(), "invalid commit hash") {
		t.Fatalf("expected invalid commit hash error, got %v", err)
	}
}

func TestFileDiffAtCommit(t *testing.T) {
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")
	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "add Run")

	hash, err := LastCommitHash()
	if err != nil {
		t.Fatalf("LastCommitHash: %v", err)
	}
	diff, err := FileDiff(hash, "main.go")
	if err != nil {
		t.Fatalf("FileDiff at commit: %v", err)
	}
	if !strings.Contains(diff, "func Run()") {
		t.Errorf("commit diff missing added line:\n%s", diff)
	}
}
