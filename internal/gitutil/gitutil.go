// Package gitutil shells out to the local git binary for the few things
// githints needs: the current commit, which files it touched, and a
// compact diff stat per file.
package gitutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxOutputBytes caps how much of git's stdout we will buffer. A diff on a
// generated or vendored file can be arbitrarily large, and this runs inside a
// long-lived stdio server, so an uncapped buffer is a memory-exhaustion vector.
const MaxOutputBytes = 1 << 20

// defaultTimeout bounds a git invocation that was not given a context. git can
// block indefinitely on a credential-helper prompt, a network filesystem, or
// index-lock contention; every caller would rather have an error.
const defaultTimeout = 30 * time.Second

// capWriter buffers up to n bytes and silently discards the rest, recording
// that it did so.
type capWriter struct {
	buf       bytes.Buffer
	n         int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.n - w.buf.Len(); room > 0 {
		if len(p) <= room {
			return w.buf.Write(p)
		}
		if _, err := w.buf.Write(p[:room]); err != nil {
			return 0, err
		}
	}
	w.truncated = true
	// Report a full write so git is not handed a short-write error.
	return len(p), nil
}

// run invokes git with a default timeout. Use runCtx when the caller has a
// context to honor (every MCP tool handler does).
func run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return runCtx(ctx, args...)
}

// Diff-producing calls pass --no-ext-diff --no-textconv --no-color: a
// repository's .gitattributes can name a diff driver or textconv filter, and
// the user's config decides what that runs. githints wants git's own diff,
// and wants it the same on every machine (it is hashed into diff_hash).
func runCtx(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out := &capWriter{n: MaxOutputBytes}
	stderr := &capWriter{n: 8 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.buf.String())
	}
	s := strings.TrimSpace(out.buf.String())
	if out.truncated {
		s += fmt.Sprintf("\n[truncated: git output exceeded %d bytes]", MaxOutputBytes)
	}
	return s, nil
}

// commitishRe matches a hex commit-ish. We require at least 4 characters to
// avoid treating branch names as hashes, and at most 64 to cover SHA-256
// git objects and the full 40-character SHA-1 hashes.
var commitishRe = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// IsValidCommitish reports whether hash is a non-flag, hex-shaped commit
// reference. An empty hash is allowed and means "working tree vs HEAD".
func IsValidCommitish(hash string) bool {
	return hash == "" || commitishRe.MatchString(hash)
}

// RepoRoot returns the absolute path to the top of the working tree.
func RepoRoot() (string, error) {
	return run("rev-parse", "--show-toplevel")
}

// LastCommitHash returns the hash of HEAD. It returns an error rather than
// a silent empty string when HEAD can't be resolved, so callers can tell
// "no commits yet" (a legitimate state in pre-commit contexts) apart from
// "git broke". An empty hash with a nil error means there is no commit yet.
func LastCommitHash() (string, error) {
	hash, err := run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return hash, nil
}

// CurrentBranch returns the checked-out branch name, or "" when HEAD is
// detached. Used to stamp each change row so history can be filtered per
// branch — useful for incident response ("which branch introduced this?").
func CurrentBranch() (string, error) {
	return run("branch", "--show-current")
}

// StagedFiles lists files currently staged for commit (the union of the
// index vs HEAD). Used by the pre-commit hook to know what an agent is
// about to commit.
func StagedFiles() ([]string, error) {
	out, err := run("diff", "--cached", "--name-only")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// FileDiff returns the unified diff for one file. If hash is empty it
// returns the working-tree diff vs HEAD (staged + unstaged); otherwise it
// returns the diff introduced by that commit, restricted to the file. The
// committed form uses `git show` so it also works for a repo's first
// commit, where `hash^` does not exist.
func FileDiff(hash, file string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return FileDiffCtx(ctx, hash, file)
}

// FileDiffCtx is FileDiff bound to a caller-supplied context. MCP tool
// handlers use this so a hung git call fails with the request instead of
// blocking the handler indefinitely.
func FileDiffCtx(ctx context.Context, hash, file string) (string, error) {
	if !IsValidCommitish(hash) {
		return "", fmt.Errorf("invalid commit hash %q", hash)
	}
	if hash == "" {
		return runCtx(ctx, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", file)
	}
	return runCtx(ctx, "show", "--no-ext-diff", "--no-textconv", "--no-color", "--pretty=format:", hash, "--", file)
}

// HooksDir returns the absolute directory git runs hooks from for the
// repository at root. It honors core.hooksPath and linked worktrees (where
// .git is a file), neither of which "<root>/.git/hooks" does.
func HooksDir(root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := runCtx(ctx, "-C", root, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		// Relative output is relative to the directory git ran in.
		out = filepath.Join(root, out)
	}
	return filepath.Clean(out), nil
}

// IsTracked reports whether rel (relative to root) is in git's index. Used to
// refuse state files -- a salt, a database -- that arrived in a clone rather
// than being created on this machine.
func IsTracked(root, rel string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := runCtx(ctx, "-C", root, "ls-files", "--", rel)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// UserEmail returns the git user.email config, or "" if not set.
func UserEmail() (string, error) {
	return run("config", "--get", "user.email")
}

// DiffHash returns the SHA-256 of the unified diff for one file in one
// commit. If hash is empty it hashes the working-tree diff vs HEAD.
// It uses git show for committed diffs so it also works for a repo's first
// commit, where hash^ does not exist.
func DiffHash(hash, file string) (string, error) {
	if !IsValidCommitish(hash) {
		return "", fmt.Errorf("invalid commit hash %q", hash)
	}
	var out string
	var err error
	if hash == "" {
		out, err = run("diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", file)
	} else {
		out, err = run("show", "--no-ext-diff", "--no-textconv", "--no-color", "--pretty=format:", hash, "--", file)
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(out))
	return hex.EncodeToString(h[:]), nil
}

// WorktreeDiffHash is the working-tree variant of DiffHash.
func WorktreeDiffHash(file string) (string, error) {
	return DiffHash("", file)
}

// ChangedFiles lists files touched by the given commit (vs its parent).
// Falls back to the diff against an empty tree for the very first commit.
func ChangedFiles(hash string) ([]string, error) {
	// hash lands in argv before the "--" separator, so an unvalidated value
	// starting with "-" would be parsed as an option. FileDiff and DiffHash
	// already guard this; all four must.
	if !IsValidCommitish(hash) {
		return nil, fmt.Errorf("invalid commit hash %q", hash)
	}
	out, err := run("diff", "--name-only", hash+"^", hash)
	if err != nil {
		// likely the first commit in the repo: diff against the empty tree
		out, err = run("show", "--name-only", "--pretty=format:", hash)
		if err != nil {
			return nil, err
		}
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// DiffStat returns a compact "+N -M" string for one file in one commit. An
// invalid commit-ish yields "" rather than an error, matching the existing
// "unknown stat" contract — but it must never reach argv, since hash sits
// before the "--" separator.
func DiffStat(hash, file string) string {
	if !IsValidCommitish(hash) {
		return ""
	}
	out, err := run("diff", "--no-ext-diff", "--no-textconv", "--numstat", hash+"^", hash, "--", file)
	if err != nil || out == "" {
		return ""
	}
	add, del, ok := parseNumstat(out)
	if !ok {
		return ""
	}
	return fmt.Sprintf("+%d -%d", add, del)
}

// WorktreeDiffStat returns a compact "+N -M" string for the current
// working-tree changes to file vs HEAD. Returns "" if the file is unchanged,
// untracked, or if there is no HEAD yet.
func WorktreeDiffStat(file string) string {
	out, err := run("diff", "--no-ext-diff", "--no-textconv", "--numstat", "HEAD", "--", file)
	if err != nil || out == "" {
		return ""
	}
	add, del, ok := parseNumstat(out)
	if !ok {
		return ""
	}
	return fmt.Sprintf("+%d -%d", add, del)
}

// ParseDiffStat parses a "+N -M" string into its add/delete counts.
func ParseDiffStat(stat string) (add, del int, ok bool) {
	stat = strings.TrimSpace(stat)
	if stat == "" {
		return 0, 0, false
	}
	parts := strings.Fields(stat)
	if len(parts) != 2 {
		return 0, 0, false
	}
	addStr := strings.TrimPrefix(parts[0], "+")
	delStr := strings.TrimPrefix(parts[1], "-")
	add, errA := strconv.Atoi(addStr)
	del, errD := strconv.Atoi(delStr)
	if errA != nil || errD != nil {
		return 0, 0, false
	}
	return add, del, true
}

func parseNumstat(out string) (add, del int, ok bool) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, 0, false
	}
	add, errA := strconv.Atoi(fields[0])
	del, errD := strconv.Atoi(fields[1])
	if errA != nil || errD != nil {
		return 0, 0, false
	}
	return add, del, true
}

// maxNotesBytes bounds ReadNotes. A note is about 300 bytes, so this allows
// tens of thousands of anchored commits while still capping memory.
const maxNotesBytes = 64 << 20

// ReadNotes returns every note under ref, keyed by the annotated commit hash.
// A missing ref is not an error: it means nothing has been anchored yet.
//
// It runs two git processes regardless of how many notes exist: one to list
// them and one cat-file --batch to read every blob. Truncated output is an
// error here rather than a marker, since a silently short read would make
// verify report anchors as missing.
func ReadNotes(ref string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := runCtx(ctx, "rev-parse", "--verify", "--quiet", ref); err != nil {
		return map[string]string{}, nil
	}
	list, err := runLimited(ctx, nil, maxNotesBytes, "notes", "--ref="+ref, "list")
	if err != nil {
		return nil, err
	}
	var blobs, commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		blobs = append(blobs, f[0])
		commits = append(commits, f[1])
	}
	notes := make(map[string]string, len(blobs))
	if len(blobs) == 0 {
		return notes, nil
	}

	out, err := runLimited(ctx, strings.NewReader(strings.Join(blobs, "\n")+"\n"), maxNotesBytes, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	// Each record is "<oid> <type> <size>\n<content>\n", in request order.
	for i := range blobs {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("cat-file: short output reading note %d of %d", i+1, len(blobs))
		}
		header := strings.Fields(string(out[:nl]))
		out = out[nl+1:]
		if len(header) != 3 {
			return nil, fmt.Errorf("cat-file: unexpected header %q", strings.Join(header, " "))
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || size+1 > len(out) {
			return nil, fmt.Errorf("cat-file: bad size in header %q", strings.Join(header, " "))
		}
		notes[commits[i]] = strings.TrimSpace(string(out[:size]))
		out = out[size+1:]
	}
	return notes, nil
}

// runLimited runs git with optional stdin and returns raw stdout, failing
// rather than truncating if it exceeds max bytes.
func runLimited(ctx context.Context, stdin io.Reader, max int, args ...string) ([]byte, error) {
	return RunIn(ctx, "", stdin, max, args...)
}

// RunIn runs git in dir (the process's working directory when empty) with
// optional stdin, bounded by ctx -- or by the default timeout when ctx has no
// deadline -- and by max bytes of stdout. Exceeding max is an error, not a
// truncation. On a non-zero exit the error wraps *exec.ExitError, so a caller
// for whom exit 1 is an answer (check-ignore) can tell, and stdout is still
// returned.
func RunIn(ctx context.Context, dir string, stdin io.Reader, max int, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out := &capWriter{n: max}
	stderr := &capWriter{n: 8 << 10}
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return out.buf.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.buf.String()))
	}
	if out.truncated {
		return nil, fmt.Errorf("git %s: output exceeded %d bytes", strings.Join(args, " "), max)
	}
	return out.buf.Bytes(), nil
}

// AddNote adds a git note to HEAD. It uses --force so repeated commits or
// amends overwrite the previous note for that ref. This is used by the
// post-commit hook to anchor the per-commit Merkle root.
func AddNote(ref, message string) error {
	_, err := run("notes", "--ref="+ref, "add", "--force", "-m", message, "HEAD")
	return err
}
