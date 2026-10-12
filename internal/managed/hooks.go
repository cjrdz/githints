package managed

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/cjrdz/githints/internal/gitutil"
)

// ChainedSuffix names where init -chain moves a pre-existing hook.
const ChainedSuffix = ".pre-githints"

// HookExistsAndManaged reports whether path is an existing file that was
// written by githints (and may safely be re-initialized).
func HookExistsAndManaged(path string) (exists bool, managed bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	if info.IsDir() {
		return true, false, fmt.Errorf("%s is a directory, not a hook", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true, false, err
	}
	return true, strings.Contains(string(data), HookMarker), nil
}

// HookScriptFor renders a managed hook.
//
// The recorded path is tried first, so a developer running a local build keeps
// using it even with another githints on PATH, and nothing changes for an
// existing install. PATH is the fallback for when that path stops existing --
// which is exactly what a package manager does on upgrade, since os.Executable
// resolves symlinks and records the versioned target (Homebrew's Cellar,
// Scoop's apps directory) rather than the stable shim.
//
// If neither resolves, the hook says so and exits 0: a missing binary must not
// block a commit, and the pre-commit gate is a warning by design.
//
// Written in POSIX sh: Git for Windows runs hooks through its bundled sh, and
// forward-slash paths work there too.
func HookScriptFor(exe, cmd string) string {
	// The path is assigned once, single-quoted, and only ever expanded inside
	// double quotes. It used to be interpolated with Go's %q, which is not
	// shell quoting: inside sh double quotes a "$(...)" or backtick in the
	// install path still ran, in the test and in the echo line alike.
	return fmt.Sprintf(`#!/bin/sh
# %s — do not edit by hand
# A hook that was here before githints (moved aside by init -chain) runs first;
# if it fails, so does this hook.
chained="$0%s"
if [ -x "$chained" ]; then
	"$chained" "$@" || exit $?
fi
githints_bin=%s
if [ -x "$githints_bin" ]; then
	exec "$githints_bin" %s "$@"
fi
if command -v githints >/dev/null 2>&1; then
	exec githints %s "$@"
fi
echo "githints: not found at $githints_bin and not on PATH; run 'githints init' to repoint this hook" >&2
exit 0
`, HookMarker, ChainedSuffix, shellQuote(filepath.ToSlash(exe)), cmd, cmd)
}

// WriteHook replaces the hook at path. It removes what is there first rather
// than writing through it: os.WriteFile follows a symlink and keeps an
// existing file's mode, so a hook that was a link, or not executable, stayed
// that way.
func WriteHook(path, script string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return err
	}
	return f.Close()
}

// shellQuote returns s as a single POSIX sh word that expands to exactly s.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HookLevel classifies the outcome of checking one hook.
type HookLevel int

const (
	HookOK HookLevel = iota
	HookWarn
	HookFail
)

func (l HookLevel) String() string {
	return [...]string{"ok", "warn", "fail"}[l]
}

// HookStatus is the result of checking one installed hook.
type HookStatus struct {
	Name   string
	Level  HookLevel
	Detail string
	Fix    string
}

// hookBinRe extracts the recorded binary path from a current hook script.
var hookBinRe = regexp.MustCompile(`(?m)^githints_bin='((?:[^']|'\\'')*)'$`)

// CheckHooks reports whether the post-commit and pre-commit hooks are
// installed and executable. It is exported so the status and doctor commands
// can reuse it without either depending on the other.
func CheckHooks(root string) ([]HookStatus, error) {
	dir, err := gitutil.HooksDir(root)
	if err != nil {
		return nil, err
	}
	var out []HookStatus
	for _, name := range []string{"post-commit", "pre-commit"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, HookStatus{name, HookFail, name + " is not installed in " + dir, "githints init"})
			continue
		case err != nil:
			out = append(out, HookStatus{name, HookFail, err.Error(), ""})
			continue
		case !strings.Contains(string(data), HookMarker):
			out = append(out, HookStatus{name, HookWarn, name + " exists but was not installed by githints", "githints init -chain (keeps it and runs it first)"})
			continue
		}
		m := hookBinRe.FindStringSubmatch(string(data))
		if m == nil {
			out = append(out, HookStatus{name, HookWarn, name + " was written by an older githints", "githints init (rewrites it)"})
			continue
		}
		bin := strings.ReplaceAll(m[1], `'\''`, `'`)
		// Windows has no execute bits; there, existing is enough.
		if fi, err := os.Stat(filepath.FromSlash(bin)); err != nil || (runtime.GOOS != "windows" && fi.Mode()&0o111 == 0) {
			if _, perr := exec.LookPath("githints"); perr == nil {
				out = append(out, HookStatus{name, HookWarn, name + " points at " + bin + ", which is gone; it falls back to githints on PATH", "githints init (repoints it)"})
			} else {
				out = append(out, HookStatus{name, HookFail, name + " points at " + bin + ", which is gone, and githints is not on PATH", "githints init"})
			}
			continue
		}
		if runtime.GOOS == "windows" {
			// Execute bits mean nothing on Windows; Git for Windows runs hooks through sh.
			out = append(out, HookStatus{name, HookOK, name + " -> " + bin, ""})
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.Mode()&0o111 == 0 {
			out = append(out, HookStatus{name, HookFail, name + " is not executable, so git skips it", "chmod +x " + path})
			continue
		}
		out = append(out, HookStatus{name, HookOK, name + " -> " + bin, ""})
	}
	return out, nil
}
