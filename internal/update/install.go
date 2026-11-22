package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Decision is what update would do, from the running version and a target.
type Decision struct {
	Current, Target string
	// Install is false when nothing needs to change.
	Install bool
	// Reason says why, for the person reading the output.
	Reason string
}

// ErrNotNewer marks a refusal to move to an older (or the same) release.
var ErrNotNewer = errors.New("not newer")

// Decide compares the running version with target. A dev build and a
// downgrade need force; a downgrade also needs an explicit -version, so a
// "latest" that briefly points at an older tag cannot roll anyone back.
func Decide(current, target string, explicit, force bool) (Decision, error) {
	d := Decision{Current: current, Target: target}
	tv, ok := ParseVersion(target)
	if !ok {
		return d, fmt.Errorf("invalid release tag %q", target)
	}
	cv, ok := ParseVersion(current)
	if !ok {
		if !force {
			return d, fmt.Errorf("this is a development build (%s); pass -force to replace it with %s", current, target)
		}
		d.Install, d.Reason = true, "replacing a development build"
		return d, nil
	}
	switch c := Compare(tv, cv); {
	case c > 0:
		d.Install, d.Reason = true, "newer release"
	case c == 0 && force:
		d.Install, d.Reason = true, "reinstalling the same release (-force)"
	case c == 0:
		d.Reason = "already up to date"
	case explicit && force:
		d.Install, d.Reason = true, "downgrading (-version with -force)"
	default:
		return d, fmt.Errorf("%w: %s is older than the running %s; downgrading needs both -version=%s and -force", ErrNotNewer, target, cv, target)
	}
	return d, nil
}

// ManagedBy names the tool that owns the binary at exe, and the command to
// update it with, or returns "" when githints may replace it itself. Replacing
// a packaged file behind the package manager's back leaves its database wrong
// and the next upgrade fighting with this one.
func ManagedBy(exe, gopath, gobin string) (owner, command string) {
	// Backslashes too, by hand: filepath.ToSlash leaves them alone off Windows.
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, `\`, "/")) }
	p := norm(exe)
	dir := path.Dir(p)
	under := func(root string) bool {
		root = path.Clean(norm(root))
		return root != "." && root != "/" && (dir == root || strings.HasPrefix(dir, root+"/"))
	}
	switch {
	case strings.Contains(p, "/cellar/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return "Homebrew", "brew upgrade githints"
	case strings.HasPrefix(p, "/nix/store/"):
		return "Nix", "update it through your Nix configuration"
	case strings.HasPrefix(p, "/snap/"):
		return "snap", "snap refresh githints"
	case strings.Contains(p, "/scoop/apps/"):
		return "Scoop", "scoop update githints"
	case strings.Contains(p, "/chocolatey/"):
		return "Chocolatey", "choco upgrade githints"
	case strings.Contains(p, "/winget/packages/"):
		return "winget", "winget upgrade githints"
	case gobin != "" && under(gobin), gopath != "" && under(filepath.Join(gopath, "bin")):
		return "go install", "go install github.com/cjrdz/githints@latest"
	}
	for _, sys := range []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/lib", "/usr/libexec"} {
		if dir == sys || strings.HasPrefix(dir, sys+"/") {
			return "your system package manager", "upgrade the githints package with apt, dnf, pacman, apk or zypper"
		}
	}
	return "", ""
}

// Options configure an install. Zero values mean the running binary on this
// platform.
type Options struct {
	Current  string // the running version
	Tag      string // a release to install; empty for the latest
	Force    bool
	Target   string // the binary to replace; empty for os.Executable
	GOOS     string
	GOARCH   string
	CheckRun func(path string) (string, error) // runs `path version`; tests replace it
}

// Result describes a finished (or skipped) update.
type Result struct {
	Decision
	Path    string
	Archive string
}

// Install replaces the running binary with a verified release.
func (c *Client) Install(ctx context.Context, o Options) (Result, error) {
	if o.GOOS == "" {
		o.GOOS, o.GOARCH = runtime.GOOS, runtime.GOARCH
	}
	if o.CheckRun == nil {
		o.CheckRun = runVersion
	}
	target := o.Target
	if target == "" {
		var err error
		if target, err = RunningBinary(); err != nil {
			return Result{}, err
		}
	}
	if owner, cmd := Owner(target); owner != "" {
		return Result{}, fmt.Errorf("%s was installed by %s; update it there: %s", target, owner, cmd)
	}

	tag := o.Tag
	if tag == "" {
		var err error
		if tag, err = c.LatestTag(ctx); err != nil {
			return Result{}, err
		}
	}
	d, err := Decide(o.Current, tag, o.Tag != "", o.Force)
	res := Result{Decision: d, Path: target, Archive: ArchiveName(tag, o.GOOS, o.GOARCH)}
	if err != nil || !d.Install {
		return res, err
	}

	archive, err := c.download(ctx, tag, res.Archive, maxArchiveBytes)
	if err != nil {
		return res, err
	}
	sums, err := c.download(ctx, tag, "checksums.txt", maxChecksumsBytes)
	if err != nil {
		return res, fmt.Errorf("%w; refusing to install unverified", err)
	}
	if err := verifySum(archive, sums, res.Archive); err != nil {
		return res, err
	}
	bin, err := extractBinary(archive, o.GOOS)
	if err != nil {
		return res, err
	}
	return res, replaceBinary(target, bin, strings.TrimPrefix(tag, "v"), o.GOOS, o.CheckRun, osFiles{})
}

// files is the filesystem replaceBinary needs, so the Windows sequence can be
// tested on any OS and a failing rename injected.
type files interface {
	Rename(from, to string) error
	Remove(path string) error
}

type osFiles struct{}

func (osFiles) Rename(from, to string) error { return os.Rename(from, to) }
func (osFiles) Remove(path string) error     { return os.Remove(path) }

// replaceBinary writes bin next to target, runs its `version` and only then
// swaps it in by rename, so target is never half-written and the running
// binary stays until the new one has proved it starts. Windows cannot rename
// over a running executable but can rename it away, so there the old binary
// moves to a free name from asideName first (removed by CleanupOld on a later
// run) and comes back if the second rename fails.
func replaceBinary(target string, bin []byte, want, goos string, checkRun func(string) (string, error), fsys files) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".githints-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w%s", target, err, permissionHint(err, target, goos))
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = fsys.Remove(tmpPath) } // best effort; the error being returned matters more
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write the new binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("make the new binary executable: %w", err)
	}
	got, err := checkRun(tmpPath)
	if err != nil || strings.TrimSpace(got) != want {
		cleanup()
		return fmt.Errorf("the downloaded binary did not run as version %s (got %q, %v); %s is unchanged", want, strings.TrimSpace(got), err, target)
	}

	if goos != "windows" {
		if err := fsys.Rename(tmpPath, target); err != nil {
			cleanup()
			return fmt.Errorf("replace %s: %w%s", target, err, permissionHint(err, target, goos))
		}
		return nil
	}
	old, err := asideName(target, fsys)
	if err != nil {
		cleanup()
		return err
	}
	if err := fsys.Rename(target, old); err != nil {
		cleanup()
		return fmt.Errorf("move the running binary aside: %w%s", err, permissionHint(err, target, goos))
	}
	if err := fsys.Rename(tmpPath, target); err != nil {
		if rerr := fsys.Rename(old, target); rerr != nil {
			return fmt.Errorf("replace %s: %w; restoring it also failed (%v): the previous binary is at %s", target, err, rerr, old)
		}
		cleanup()
		return fmt.Errorf("replace %s: %w; the previous binary is back", target, err)
	}
	return nil
}

// permissionHint says what to do when err is a permission failure on the
// directory holding target, which is where a binary installed system-wide
// (/usr/local/bin, Program Files) ends up: githints can do nothing about it
// itself, and the bare "permission denied" left people guessing.
func permissionHint(err error, target, goos string) string {
	if !errors.Is(err, fs.ErrPermission) {
		return ""
	}
	if goos == "windows" {
		return fmt.Sprintf("\n%s is in a folder this account cannot write to. Run `githints update` from an elevated (administrator) terminal, or reinstall into a folder you own with install.ps1 (it defaults to %%LOCALAPPDATA%%\\Programs\\githints; GITHINTS_BIN_DIR picks another).", target)
	}
	return fmt.Sprintf("\n%s is in a directory this user cannot write to. Run `sudo %s update`, or reinstall into a directory you own:\n  curl -fsSL https://raw.githubusercontent.com/cjrdz/githints/main/install.sh | GITHINTS_BIN_DIR=\"$HOME/.local/bin\" sh", target, shellQuote(target))
}

// shellQuote quotes s for a POSIX shell, so a suggested command still works
// for a path with spaces or quotes in it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// maxAside bounds the names asideName tries: one per githints process still
// running an older binary from this directory, which is a handful at most.
const maxAside = 32

// asideName returns where the running binary can be moved on Windows:
// target.old, or target.old-1, target.old-2... when an earlier one is still
// running. An MCP server keeps the binary it started with, so after one update
// target.old is often locked, and renaming onto it fails with "Access is
// denied" (seen on a real second update of 0.5.0). Leftovers that are no
// longer running are removed on the way.
func asideName(target string, fsys files) (string, error) {
	for i := range maxAside {
		name := target + ".old"
		if i > 0 {
			name = fmt.Sprintf("%s.old-%d", target, i)
		}
		_ = fsys.Remove(name) // absent, or still running: the Lstat below decides
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
	}
	return "", fmt.Errorf("move the running binary aside: %d earlier githints binaries next to %s are still running; stop githints MCP servers and try again", maxAside, target)
}

// isAsideName reports whether name is one asideName gives for base.
func isAsideName(name, base string) bool {
	if name == base+".old" {
		return true
	}
	n, ok := strings.CutPrefix(name, base+".old-")
	if !ok || n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CleanupOld removes the binaries a Windows update moved aside, once they are
// no longer running. Elsewhere there is nothing to clean up.
func CleanupOld() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := RunningBinary()
	if err != nil {
		return
	}
	cleanupOld(exe, osFiles{})
}

// cleanupOld removes every asideName leftover next to exe. One still running
// fails to delete and is retried on a later run.
func cleanupOld(exe string, fsys files) {
	dir, base := filepath.Split(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && isAsideName(e.Name(), base) {
			_ = fsys.Remove(filepath.Join(dir, e.Name())) // locked: retried next run
		}
	}
}

func runVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	return string(out), err
}

// RunningBinary is the binary update would replace: this process's
// executable with links resolved.
func RunningBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	return target, nil
}

// Owner is ManagedBy with this environment's GOPATH and GOBIN.
func Owner(path string) (owner, command string) {
	return ManagedBy(path, goEnv("GOPATH"), goEnv("GOBIN"))
}

// goEnv reads a Go setting from the environment, falling back to the default
// GOPATH. It does not run `go env`: update must not depend on a toolchain.
func goEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if key == "GOPATH" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "go")
		}
	}
	return ""
}
