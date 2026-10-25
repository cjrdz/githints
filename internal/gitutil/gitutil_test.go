package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// ChangedFiles and CommitDiffStats place the commit-ish in argv before the "--"
// separator, so an unvalidated value beginning with "-" would be parsed as a
// git option. FileDiff and DiffHash always guarded this; these two did not.
func TestCommitishGuardOnAllCallers(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	for _, bad := range []string{"--output=/tmp/pwn", "--upload-pack=touch /tmp/x", "-x", "HEAD"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := ChangedFiles(bad); err == nil {
				t.Errorf("ChangedFiles(%q) = nil error, want rejection", bad)
			}
			if _, err := CommitDiffStats(bad); err == nil {
				t.Errorf("CommitDiffStats(%q) = nil error, want rejection", bad)
			}
			if _, err := FileDiff(bad, "main.go"); err == nil {
				t.Errorf("FileDiff(%q) = nil error, want rejection", bad)
			}
			if _, err := DiffHash(bad, "main.go"); err == nil {
				t.Errorf("DiffHash(%q) = nil error, want rejection", bad)
			}
		})
	}
}

func TestRunCapsOutput(t *testing.T) {
	w := &capWriter{n: 10}
	if _, err := w.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := w.buf.String(); got != "0123456789" {
		t.Errorf("buffered %q, want %q", got, "0123456789")
	}
	if !w.truncated {
		t.Error("expected truncated flag")
	}
	// A further write must not grow the buffer or fail.
	n, err := w.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Errorf("Write after cap = (%d, %v), want (4, nil)", n, err)
	}
	if w.buf.Len() != 10 {
		t.Errorf("buffer grew past cap to %d", w.buf.Len())
	}
}

func TestFileDiffCtxHonorsCancellation(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "main.go")
	runGit(t, dir, "commit", "-m", "initial")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FileDiffCtx(ctx, "", "main.go"); err == nil {
		t.Error("expected error from cancelled context")
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

	h, err := DiffHash("", "main.go")
	if err != nil {
		t.Fatalf("DiffHash worktree: %v", err)
	}
	if h == "" {
		t.Fatal("DiffHash worktree returned empty")
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

// A repository's .gitattributes can name a diff driver, and config decides
// what that driver runs. githints must get git's own diff and never run it.
func TestDiffDoesNotRunExternalDiffOrTextconv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the driver")
	}
	dir := makeRepo(t)
	restore := chdir(t, dir)
	defer restore()

	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "driver.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\ncat \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".gitattributes", "*.go diff=evil\n")
	runGit(t, dir, "config", "diff.evil.textconv", script)
	runGit(t, dir, "config", "diff.external", script)
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	writeFile(t, dir, "main.go", "package main\n\nfunc Run() {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second")

	hash, err := LastCommitHash()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "main.go", "package main\n\nfunc Run() { _ = 1 }\n")
	if _, err := FileDiff(hash, "main.go"); err != nil {
		t.Fatalf("FileDiff: %v", err)
	}
	if _, err := FileDiff("", "main.go"); err != nil {
		t.Fatalf("FileDiff worktree: %v", err)
	}
	_, _ = DiffHash(hash, "main.go")
	_, _ = CommitDiffStats(hash)
	_ = WorktreeDiffStat("main.go")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository-configured diff driver was executed")
	}
}

// The per-file form diffed against hash^, which a repository's first commit
// does not have, so every file in it was recorded with no stat.
func TestCommitDiffStats(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "main.go", "package main\n\nfunc A() {}\n")
	writeFile(t, dir, "blob.bin", "\x00\x01\x02")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	first, err := LastCommitHash()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := CommitDiffStats(first)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if got := stats["main.go"]; got != "+3 -0" {
		t.Errorf("first commit main.go = %q, want +3 -0", got)
	}
	if got, ok := stats["blob.bin"]; !ok || got != "" {
		t.Errorf("binary file = %q (present %v), want an empty stat", got, ok)
	}

	writeFile(t, dir, "main.go", "package main\n\nfunc B() {}\n")
	runGit(t, dir, "mv", "blob.bin", "moved.bin")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second")
	second, err := LastCommitHash()
	if err != nil {
		t.Fatal(err)
	}
	stats, err = CommitDiffStats(second)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats["main.go"]; got != "+1 -1" {
		t.Errorf("second commit main.go = %q, want +1 -1", got)
	}
	for _, p := range []string{"blob.bin", "moved.bin"} {
		if _, ok := stats[p]; !ok {
			t.Errorf("rename side %s missing: %v", p, stats)
		}
	}
}

func TestRevertedCommits(t *testing.T) {
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	sha256 := strings.Repeat("c", 64)
	cases := map[string]struct {
		msg  string
		want []string
	}{
		"git revert":      {"Revert \"add x\"\n\nThis reverts commit " + a + ".\n", []string{a}},
		"two":             {"Revert two\n\nThis reverts commit " + a + ".\nThis reverts commit " + b + ".\n", []string{a, b}},
		"repeated":        {"This reverts commit " + a + ".\nThis reverts commit " + a + ".\n", []string{a}},
		"sha-256":         {"This reverts commit " + sha256 + ".\n", []string{sha256}},
		"abbreviated":     {"This reverts commit " + a[:12] + ".\n", nil},
		"mid line":        {"we said: This reverts commit " + a + "\n", nil},
		"indented":        {"  This reverts commit " + a + "\n", nil},
		"uppercase":       {"This reverts commit " + strings.ToUpper(a) + "\n", nil},
		"too long":        {"This reverts commit " + a + "abc\n", nil},
		"unrelated":       {"fix: handle reverts commit\n", nil},
		"empty":           {"", nil},
		"flag-like claim": {"This reverts commit --output=/tmp/pwn\n", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RevertedCommits(tc.msg)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("RevertedCommits = %v, want %v", got, tc.want)
			}
		})
	}

	var many strings.Builder
	for i := 0; i < maxRevertRefs+10; i++ {
		fmt.Fprintf(&many, "This reverts commit %040x\n", i+1)
	}
	if got := RevertedCommits(many.String()); len(got) != maxRevertRefs {
		t.Errorf("a message naming %d commits yielded %d, want the cap %d", maxRevertRefs+10, len(got), maxRevertRefs)
	}
}

func TestCommitTreeHelpers(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "a.go", "one\n")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "commit", "-m", "first commit")
	first, err := LastCommitHash()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.go", "two\n")
	writeFile(t, dir, "b.go", "new\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second commit")
	second, _ := LastCommitHash()

	msg, err := CommitMessage(second)
	if err != nil || msg != "second commit" {
		t.Errorf("CommitMessage = %q, %v", msg, err)
	}
	if full, ok := ResolveCommit(second[:10]); !ok || full != second {
		t.Errorf("ResolveCommit(abbrev) = %q, %v", full, ok)
	}
	if _, ok := ResolveCommit(strings.Repeat("0", 40)); ok {
		t.Error("ResolveCommit accepted a commit that does not exist")
	}
	if p, ok := ParentCommit(second); !ok || p != first {
		t.Errorf("ParentCommit = %q, %v, want %q", p, ok, first)
	}
	if _, ok := ParentCommit(first); ok {
		t.Error("a root commit has a parent")
	}

	// Identity: same content equal, different content different, absent empty.
	a1, err1 := PathAt(first, "a.go")
	a2, err2 := PathAt(second, "a.go")
	if err1 != nil || err2 != nil || a1 == "" || a2 == "" || a1 == a2 {
		t.Errorf("PathAt a.go: %q (%v) vs %q (%v), want two different identities", a1, err1, a2, err2)
	}
	if b, err := PathAt(first, "b.go"); err != nil || b != "" {
		t.Errorf("PathAt of a file that did not exist = %q, %v, want empty", b, err)
	}
	if b, err := PathAt(second, "b.go"); err != nil || b == "" {
		t.Errorf("PathAt of an existing file = %q, %v", b, err)
	}
	// A path that looks like an option is a path.
	if got, err := PathAt(second, "--output=/tmp/pwn"); err != nil || got != "" {
		t.Errorf("PathAt(option-looking path) = %q, %v", got, err)
	}
}

// Every hash must be hex and non-empty: "" would mean the working tree, which
// none of these can answer, and anything else lands in argv.
func TestCommitTreeHelpersGuardTheHash(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "a.go", "x\n")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "commit", "-m", "first")

	for _, bad := range []string{"", "--output=/tmp/pwn", "-x", "HEAD", "main"} {
		if _, err := CommitMessage(bad); err == nil {
			t.Errorf("CommitMessage(%q) accepted", bad)
		}
		if _, ok := ResolveCommit(bad); ok {
			t.Errorf("ResolveCommit(%q) accepted", bad)
		}
		if _, ok := ParentCommit(bad); ok {
			t.Errorf("ParentCommit(%q) accepted", bad)
		}
		if _, err := PathAt(bad, "a.go"); err == nil {
			t.Errorf("PathAt(%q) accepted", bad)
		}
	}
}

// File names come back exactly as they are: git quotes anything unusual in
// --name-only output ("\303\251.txt" for "é.txt") unless told not to, and a
// quoted name matches no recorded row and no tree entry.
func TestChangedAndStagedFilesAreNotQuoted(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	names := []string{"é.txt", "日本語.txt", "has space.txt", "plain.txt"}
	if runtime.GOOS != "windows" { // '"' is not legal in a Windows file name
		names = append(names, `quo"te.txt`)
	}
	for _, n := range names {
		writeFile(t, dir, n, "one\n")
	}
	runGit(t, dir, "add", "--", ".")

	staged, err := StagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-m", "first") // the root commit: the fallback path
	first, _ := LastCommitHash()
	for _, n := range names {
		writeFile(t, dir, n, "two\n")
	}
	runGit(t, dir, "add", "--", ".")
	runGit(t, dir, "commit", "-m", "second")
	second, _ := LastCommitHash()

	for desc, hash := range map[string]string{"root commit": first, "ordinary commit": second} {
		got, err := ChangedFiles(hash)
		if err != nil {
			t.Fatal(err)
		}
		if !sameSet(got, names) {
			t.Errorf("ChangedFiles(%s) = %q, want %q", desc, got, names)
		}
	}
	if !sameSet(staged, names) {
		t.Errorf("StagedFiles = %q, want %q", staged, names)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

func TestChangedFilesNoRenamesListsBothPaths(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "old.go", "package a\n\nfunc F() {}\n\nfunc G() {}\n")
	runGit(t, dir, "add", "--", ".")
	runGit(t, dir, "commit", "-m", "first")
	runGit(t, dir, "mv", "old.go", "new.go")
	runGit(t, dir, "commit", "-m", "rename")
	h, _ := LastCommitHash()

	with, err := ChangedFiles(h)
	if err != nil || !sameSet(with, []string{"new.go"}) {
		t.Errorf("ChangedFiles = %v, %v, want only the new path", with, err)
	}
	without, err := ChangedFilesNoRenames(h)
	if err != nil || !sameSet(without, []string{"old.go", "new.go"}) {
		t.Errorf("ChangedFilesNoRenames = %v, %v, want both paths", without, err)
	}
	if _, err := ChangedFilesNoRenames("--output=/tmp/pwn"); err == nil {
		t.Error("an option-looking hash was accepted")
	}
}

// The tree write-tree gives for the index is the tree the next commit gets,
// and CommitShapes reads it back with the parent, for any number of commits
// in one process.
func TestIndexTreeAndCommitShapes(t *testing.T) {
	dir := makeRepo(t)
	defer chdir(t, dir)()
	writeFile(t, dir, "a.txt", "one\n")
	runGit(t, dir, "add", "a.txt")
	staged, err := IndexTree()
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-m", "first")
	first, _ := LastCommitHash()
	writeFile(t, dir, "a.txt", "two\n")
	runGit(t, dir, "commit", "-am", "second")
	second, _ := LastCommitHash()

	shapes, err := CommitShapes([]string{first, second, strings.Repeat("0", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if got := shapes[first]; got.Tree != staged || got.Parent != "" || got.Missing {
		t.Errorf("first = %+v, want tree %s and no parent", got, staged)
	}
	if got := shapes[second]; got.Parent != first || got.Tree == staged {
		t.Errorf("second = %+v, want parent %s and a new tree", got, first)
	}
	if !shapes[strings.Repeat("0", 40)].Missing {
		t.Error("an unknown commit was not reported missing")
	}
	if _, err := CommitShapes([]string{"--output=/tmp/x"}); err == nil {
		t.Error("an option-shaped hash was accepted")
	}
}
