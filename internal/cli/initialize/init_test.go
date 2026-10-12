package initialize

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/managed"
)

func TestInitHookScript(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)

	if err := Init([]string{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	hook, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatalf("read post-commit hook: %v", err)
	}
	script := string(hook)
	if !strings.Contains(script, managed.HookMarker) {
		t.Errorf("hook missing managed marker")
	}
	if strings.Contains(script, "\\") {
		t.Errorf("hook contains backslashes; expected forward-slash path: %s", script)
	}
}

func TestInitGitignorePrivate(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)

	if err := Init([]string{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), managed.GitignoreStart) {
		t.Errorf(".gitignore missing managed block")
	}
	if !strings.Contains(string(data), ".githints/") {
		t.Errorf("private mode should ignore .githints/")
	}
}

func TestInitGitignoreShared(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)

	if err := Init([]string{"-share"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, managed.GitignoreStart) {
		t.Errorf(".gitignore missing managed block")
	}

	lines := strings.Split(s, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == ".githints/" {
			t.Errorf("shared mode should not ignore .githints/ wholesale")
		}
	}
	for _, want := range []string{".githints/store.db*", ".githints/index.db*", ".githints/.salt", ".githints/config.json"} {
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

func TestInitWritesAgentFiles(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)

	if err := Init([]string{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	for _, want := range []string{"record_change", "get_session_context", "get_file_history"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, agents)
		}
	}

	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claude), "@AGENTS.md") {
		t.Errorf("CLAUDE.md should import AGENTS.md:\n%s", claude)
	}
}

// Re-running init must update only its own block.
func TestInitPreservesHandWrittenContent(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)

	handWritten := "# My project\n\nDo not delete this line.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(handWritten), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := Init([]string{"-force"}); err != nil {
			t.Fatalf("Init run %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "Do not delete this line.") {
		t.Errorf("init clobbered hand-written content:\n%s", body)
	}
	if n := strings.Count(body, managed.MarkdownStart); n != 1 {
		t.Errorf("expected exactly 1 managed block after 2 runs, got %d:\n%s", n, body)
	}
}

// The error from ReadFile used to be discarded, so any failure other than
// not-exist produced an empty buffer and the write replaced the user's file
// with nothing but our own block.
func TestEnsureManagedBlockFailsClosedOnReadError(t *testing.T) {
	dir := t.TempDir()
	// A directory is readable by Stat but not by ReadFile, which gives us a
	// non-NotExist error on every platform.
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := managed.EnsureBlock(path, managed.MarkdownStart, managed.MarkdownEnd, []string{"body"}); err == nil {
		t.Fatal("expected an error rather than a silent overwrite")
	}
}

// A brand-new file should not open with blank lines before the marker.
func TestEnsureManagedBlockNewFileHasNoLeadingBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := managed.EnsureBlock(path, managed.MarkdownStart, managed.MarkdownEnd, managed.ClaudeBlock()); err != nil {
		t.Fatalf("EnsureBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(data), managed.MarkdownStart) {
		t.Errorf("new file should start with the marker, got:\n%q", string(data))
	}
}

// TestHookSurvivesBinaryMoving pins the fallback that makes package managers
// usable. os.Executable resolves symlinks, so `githints init` records the
// versioned target a Homebrew or Scoop shim points at, not the stable shim.
// Upgrading replaces that directory, and without a fallback every hook in
// every tracked repo would point at a deleted file and silently stop
// recording.
func TestHookSurvivesBinaryMoving(t *testing.T) {
	dir := t.TempDir()
	recorded := filepath.Join(dir, "versioned", "githints")

	hook := managed.HookScriptFor(recorded, "hook-run")

	// The recorded path wins while it exists, so a local dev build is not
	// displaced by an unrelated githints on PATH.
	// The path is held in a shell variable, single-quoted (see
	// TestHookScriptQuotesHostilePath), and tested before anything else.
	if !strings.Contains(hook, `githints_bin='`+filepath.ToSlash(recorded)+`'`) ||
		!strings.Contains(hook, `[ -x "$githints_bin" ]`) {
		t.Errorf("hook does not test the recorded path first:\n%s", hook)
	}
	idxRecorded := strings.Index(hook, `[ -x "$githints_bin" ]`)
	idxPath := strings.Index(hook, "command -v githints")
	if idxRecorded < 0 || idxPath < 0 || idxRecorded > idxPath {
		t.Errorf("the recorded path must be tried before PATH:\n%s", hook)
	}

	// PATH is the fallback.
	if !strings.Contains(hook, "exec githints hook-run") {
		t.Errorf("hook has no PATH fallback:\n%s", hook)
	}

	// Neither available must not fail a commit: post-commit's status is
	// ignored, but pre-commit's is not, and a missing binary is not a reason
	// to block someone's work.
	if !strings.Contains(hook, "exit 0") {
		t.Errorf("hook must exit 0 when the binary is missing:\n%s", hook)
	}
	if !strings.Contains(hook, "githints init") {
		t.Errorf("hook should say how to repair itself:\n%s", hook)
	}
}

// TestHookIsPosixSh guards the shell the hook is written in. Git for Windows
// runs hooks through its bundled sh, so anything bash-only would break there
// and nowhere else.
func TestHookIsPosixSh(t *testing.T) {
	hook := managed.HookScriptFor("/usr/local/bin/githints", "hook-precommit")
	if !strings.HasPrefix(hook, "#!/bin/sh\n") {
		t.Errorf("hook must start with #!/bin/sh:\n%s", hook)
	}
	for _, bashism := range []string{"[[", "==", "function ", "$(<"} {
		if strings.Contains(hook, bashism) {
			t.Errorf("hook uses the bash-only %q:\n%s", bashism, hook)
		}
	}
}

// The HMAC chain cannot see rows deleted from its tail, and a same-user
// attacker can re-sign the whole table anyway. verify used to print a Merkle
// root and never compare it with the one anchored in refs/notes/githints, so
// this deletion verified clean.
func TestInitInstallsIntoCoreHooksPath(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	hooks := t.TempDir()
	clitest.RunGit(t, dir, "config", "core.hooksPath", hooks)
	if err := Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, h := range []string{"post-commit", "pre-commit"} {
		if _, err := os.Stat(filepath.Join(hooks, h)); err != nil {
			t.Errorf("%s not installed in core.hooksPath: %v", h, err)
		}
	}
}

// A hooks path inside the work tree is committed (husky, lefthook); init must
// not write this machine's binary path into it.
func TestInitRefusesHooksPathInsideWorkTree(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	clitest.RunGit(t, dir, "config", "core.hooksPath", ".husky")
	err := Init(nil)
	if err == nil || !strings.Contains(err.Error(), "githints hook-run") {
		t.Fatalf("expected a refusal naming the lines to add, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".husky", "post-commit")); err == nil {
		t.Fatal("hook written into the committed hooks directory")
	}
}

// In a linked worktree .git is a file, so "<root>/.git/hooks" does not exist.
func TestInitWorksInLinkedWorktree(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	main := t.TempDir()
	clitest.RunGit(t, main, "init", "-q")
	clitest.RunGit(t, main, "config", "user.email", "t@example.com")
	clitest.RunGit(t, main, "config", "user.name", "T")
	clitest.RunGit(t, main, "commit", "-q", "--allow-empty", "-m", "root")
	wt := filepath.Join(t.TempDir(), "wt")
	clitest.RunGit(t, main, "worktree", "add", "-q", wt)

	cwd, _ := os.Getwd()
	if err := os.Chdir(wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if err := Init(nil); err != nil {
		t.Fatalf("Init in a worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, ".git", "hooks", "post-commit")); err != nil {
		t.Fatalf("hook not installed in the shared hooks directory: %v", err)
	}
}

// -chain keeps a user's hook: it is moved aside and runs before githints'.
func TestInitChainKeepsExistingHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs the hook with sh")
	}
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	pre := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(pre, []byte("#!/bin/sh\n: > '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Init(nil); err == nil || !strings.Contains(err.Error(), "-chain") {
		t.Fatalf("init over a foreign hook should suggest -chain, got %v", err)
	}
	if err := Init([]string{"-chain"}); err != nil {
		t.Fatalf("Init -chain: %v", err)
	}
	if _, err := os.Stat(pre + managed.ChainedSuffix); err != nil {
		t.Fatalf("existing hook not kept: %v", err)
	}

	// init recorded the test binary as githints; point the hook at a missing
	// binary so running it exercises only the chain and the exit-0 fallback.
	if err := os.WriteFile(pre, []byte(managed.HookScriptFor(filepath.Join(t.TempDir(), "githints"), "hook-precommit")), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", pre)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("chained hook run: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the chained hook did not run")
	}

	// Re-running init leaves the chain intact.
	if err := Init([]string{"-chain"}); err != nil {
		t.Fatalf("second Init -chain: %v", err)
	}
}

// A failing chained hook fails the githints hook too, so a user's own
// pre-commit check still blocks.
func TestChainedHookFailurePropagates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs the hook with sh")
	}
	dir := t.TempDir()
	hook := filepath.Join(dir, "pre-commit")
	if err := os.WriteFile(hook+managed.ChainedSuffix, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(managed.HookScriptFor(filepath.Join(dir, "missing"), "hook-precommit")), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", hook)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("chained failure not propagated: %v", err)
	}
}

// When every signed row fails, verify says why that usually happens instead of
// leaving the user to conclude the whole log was tampered with.
// The install path is data. With Go's %q it landed inside sh double quotes,
// where "$(...)" still ran -- in the -x test and again in the echo line.
func TestHookScriptQuotesHostilePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	exe := filepath.Join(dir, "it's $(touch "+marker+") `touch "+marker+"`", "githints")
	hook := filepath.Join(dir, "hook")
	if err := os.WriteFile(hook, []byte(managed.HookScriptFor(exe, "hook-run")), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", hook)
	cmd.Env = append(os.Environ(), "PATH=/nonexistent") // so the PATH fallback cannot fire either
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook should exit 0 when the binary is missing: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("hook executed part of the install path:\n%s", out)
	}
	if !strings.Contains(string(out), "it's $(touch") {
		t.Errorf("hook did not print the path verbatim: %s", out)
	}
}
