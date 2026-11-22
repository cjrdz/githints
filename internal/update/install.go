package update

import (
	"context"
	"errors"
	"fmt"
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
// becomes target.old first (removed by CleanupOld on a later run) and comes
// back if the second rename fails.
func replaceBinary(target string, bin []byte, want, goos string, checkRun func(string) (string, error), fsys files) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".githints-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", target, err)
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
			return fmt.Errorf("replace %s: %w", target, err)
		}
		return nil
	}
	old := target + ".old"
	_ = fsys.Remove(old) // a leftover from an earlier update; absent is fine
	if err := fsys.Rename(target, old); err != nil {
		cleanup()
		return fmt.Errorf("move the running binary aside: %w", err)
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

// CleanupOld removes the binary a Windows update moved aside, once it is no
// longer running. Elsewhere there is nothing to clean up.
func CleanupOld() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old") // usually absent; a failure is retried next run
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
