package anchors

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/doctor"
	"github.com/cjrdz/githints/internal/cli/hooks"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
)

// repoWithAnchor is an initialized repository with one anchored commit and a
// bare repository as its origin.
func repoWithAnchor(t *testing.T) (dir, bare string) {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	bare = t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	dir = clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"post-commit", "pre-commit"} {
		_ = os.Remove(filepath.Join(dir, ".git", "hooks", name)) // run by hand below
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "a.go")
	clitest.RunGit(t, dir, "commit", "-m", "first")
	if err := hooks.HookRun(); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "remote", "add", "origin", bare)
	return dir, bare
}

func remoteAnchors(t *testing.T, bare string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", bare, "rev-parse", "--verify", "--quiet", gitutil.AnchorsRef).Output()
	return strings.TrimSpace(string(out))
}

func TestPushAndFetchAnchors(t *testing.T) {
	dir, bare := repoWithAnchor(t)
	local := gitutil.RefHash(gitutil.AnchorsRef)
	if local == "" {
		t.Fatal("the hook wrote no anchor")
	}
	out, _ := clitest.CaptureStdout(t, func() error { return doctor.Run() })
	if !strings.Contains(out, "exist only on this machine") {
		t.Errorf("doctor did not warn about unpushed anchors:\n%s", out)
	}

	if err := Run([]string{"push"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := remoteAnchors(t, bare); got != local {
		t.Fatalf("remote anchors = %q, want %s", got, local)
	}
	out, _ = clitest.CaptureStdout(t, func() error { return doctor.Run() })
	if strings.Contains(out, "exist only on this machine") {
		t.Errorf("doctor still warns after a push:\n%s", out)
	}

	// A second clone of the store's repository fetches them.
	clitest.RunGit(t, dir, "update-ref", "-d", gitutil.AnchorsRef)
	if err := Run([]string{"fetch", "origin"}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := gitutil.RefHash(gitutil.AnchorsRef); got != local {
		t.Errorf("fetched anchors = %q, want %s", got, local)
	}
}

// The remote lands in argv, so only a configured remote name is accepted.
func TestAnchorsRefusesAnythingButARemoteName(t *testing.T) {
	_, bare := repoWithAnchor(t)
	for _, remote := range []string{"--upload-pack=touch /tmp/x", "-v", bare, "nosuchremote"} {
		if err := Run([]string{"push", remote}); err == nil {
			t.Errorf("push to %q was attempted", remote)
		}
		if err := Run([]string{"fetch", remote}); err == nil {
			t.Errorf("fetch from %q was attempted", remote)
		}
	}
	if err := Run([]string{"pull"}); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
}
