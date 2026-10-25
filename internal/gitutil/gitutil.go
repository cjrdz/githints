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
	out, err := execGit(ctx, "", nil, MaxOutputBytes, args)
	if err != nil {
		return "", err
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
	return pathList("diff", "--cached", "--name-only", "-z")
}

// StagedDiff is the staged change as a zero-context diff, for scanning what a
// commit would add. Rename detection is off so a moved file's lines count as
// added, and no repository driver runs.
func StagedDiff() (string, error) {
	return run("diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "-U0")
}

// StagedFilesNoRenames is StagedFiles with rename detection off, so a staged
// rename lists the old path (deleted) as well as the new one. Risk scoring
// needs both: moving internal/auth/token.go to util.go must still count as a
// change to auth code.
func StagedFilesNoRenames() ([]string, error) {
	return pathList("diff", "--cached", "--name-only", "--no-renames", "-z")
}

// pathList runs a git command that prints NUL-separated paths (-z) and returns
// them exactly as they are.
//
// Without -z git quotes any path with a non-ASCII or unusual character
// ("\303\251.txt" for "é.txt", with the quotes), and that quoted text is not a
// path: it matches no recorded row and no entry in any tree. Rows for such a
// file were never claimed by their commit, and a revert check comparing two
// lookups of the quoted text saw "absent" on both sides and called it restored.
// The output is not trimmed either: a path may begin or end with a space.
func pathList(args ...string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := runLimited(ctx, nil, MaxOutputBytes, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
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

// DiffHash returns the SHA-256 of FileDiff(hash, file): the diff of one file
// in one commit, or of the working tree vs HEAD when hash is empty.
func DiffHash(hash, file string) (string, error) {
	out, err := FileDiff(hash, file)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(out))
	return hex.EncodeToString(h[:]), nil
}

// ChangedFiles lists files touched by the given commit (vs its parent).
// Falls back to the diff against an empty tree for the very first commit.
func ChangedFiles(hash string) ([]string, error) {
	return changedFiles(hash, false)
}

// ChangedFilesNoRenames is ChangedFiles with rename detection off, so a rename
// lists both the old path (as deleted) and the new one (as added) instead of only
// the new. Revert detection needs that: reverting a rename shows up under the old
// name by default, while the row it undoes was recorded under the new one.
func ChangedFilesNoRenames(hash string) ([]string, error) {
	return changedFiles(hash, true)
}

func changedFiles(hash string, noRenames bool) ([]string, error) {
	// hash lands in argv before the "--" separator, so an unvalidated value
	// starting with "-" would be parsed as an option. FileDiff and DiffHash
	// already guard this; all four must.
	if !IsValidCommitish(hash) {
		return nil, fmt.Errorf("invalid commit hash %q", hash)
	}
	diff := []string{"diff", "--name-only", "-z"}
	tree := []string{"diff-tree", "-r", "--root", "--no-commit-id", "--name-only", "-z"}
	if noRenames {
		diff = append(diff, "--no-renames")
		tree = append(tree, "--no-renames")
	}
	paths, err := pathList(append(diff, hash+"^", hash)...)
	if err != nil {
		// likely the first commit in the repo: diff against the empty tree
		return pathList(append(tree, hash)...)
	}
	return paths, nil
}

// ShortHash is the 8-character prefix githints shows for a commit hash.
func ShortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// CommitDiffStats returns the compact "+N -M" stat of every file a commit
// touched, keyed by path, from one git process. A binary file maps to "".
// Rename detection is off, as it was when each file was diffed alone: a
// pathspec of one path cannot see the other side of a rename either.
//
// It replaced one `git diff hash^ hash -- file` per file, which also gave the
// repository's first commit no stats at all, since its hash^ does not exist.
func CommitDiffStats(hash string) (map[string]string, error) {
	// hash lands in argv before any separator, so it must be validated.
	if hash == "" || !IsValidCommitish(hash) {
		return nil, fmt.Errorf("invalid commit hash %q", hash)
	}
	opts := []string{"--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z"}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := runLimited(ctx, nil, MaxOutputBytes, append(append([]string{"diff"}, opts...), hash+"^", hash)...)
	if err != nil {
		// The first commit has no parent: diff it against the empty tree.
		out, err = runLimited(ctx, nil, MaxOutputBytes, append(append([]string{"diff-tree", "-r", "--root", "--no-commit-id"}, opts...), hash)...)
		if err != nil {
			return nil, err
		}
	}
	stats := map[string]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		add, rest, ok1 := strings.Cut(rec, "\t")
		del, path, ok2 := strings.Cut(rest, "\t")
		if !ok1 || !ok2 || path == "" {
			continue
		}
		stats[path] = numstat(add + "\t" + del)
	}
	return stats, nil
}

// WorktreeDiffStat returns a compact "+N -M" string for the current
// working-tree changes to file vs HEAD. Returns "" if the file is unchanged,
// untracked, or if there is no HEAD yet.
func WorktreeDiffStat(file string) string {
	out, err := run("diff", "--no-ext-diff", "--no-textconv", "--numstat", "HEAD", "--", file)
	if err != nil || out == "" {
		return ""
	}
	return numstat(out)
}

// numstat turns git's "ADD\tDEL..." counts into "+ADD -DEL", or "" for a
// binary file (git prints "-" for both) or anything else it cannot read.
func numstat(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return ""
	}
	add, errA := strconv.Atoi(f[0])
	del, errD := strconv.Atoi(f[1])
	if errA != nil || errD != nil {
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

	objs, err := catFileBatch(ctx, blobs, maxNotesBytes)
	if err != nil {
		return nil, err
	}
	for i, o := range objs {
		if o.missing {
			return nil, fmt.Errorf("cat-file: note %s is missing", blobs[i])
		}
		notes[commits[i]] = strings.TrimSpace(string(o.body))
	}
	return notes, nil
}

type catObject struct {
	kind    string
	body    []byte
	missing bool
}

// catFileBatch reads objects with one `git cat-file --batch`, in request
// order. Truncated output is an error, never a short answer.
func catFileBatch(ctx context.Context, ids []string, max int) ([]catObject, error) {
	out, err := runLimited(ctx, strings.NewReader(strings.Join(ids, "\n")+"\n"), max, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	objs := make([]catObject, 0, len(ids))
	// Each record is "<oid> <type> <size>\n<content>\n", or "<id> missing\n".
	for i := range ids {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("cat-file: short output reading object %d of %d", i+1, len(ids))
		}
		header := strings.Fields(string(out[:nl]))
		out = out[nl+1:]
		if len(header) == 2 && header[1] == "missing" {
			objs = append(objs, catObject{missing: true})
			continue
		}
		if len(header) != 3 {
			return nil, fmt.Errorf("cat-file: unexpected header %q", strings.Join(header, " "))
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || size+1 > len(out) {
			return nil, fmt.Errorf("cat-file: bad size in header %q", strings.Join(header, " "))
		}
		objs = append(objs, catObject{kind: header[1], body: out[:size]})
		out = out[size+1:]
	}
	return objs, nil
}

// IndexTree writes the index as a tree object and returns its hash: exactly
// the tree a commit made now would record. It honors GIT_INDEX_FILE, so in a
// pre-commit hook under `git commit -a` it is the temporary index git commits
// from. It fails while the index has unmerged entries.
func IndexTree() (string, error) {
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	if tree == "" || !IsValidCommitish(tree) {
		return "", fmt.Errorf("write-tree returned %q", tree)
	}
	return tree, nil
}

// CommitShape is what a commit was made of: its tree and first parent ("" for
// a root commit). Missing is true when the commit is not in the repository
// (rewritten away, or never fetched).
type CommitShape struct {
	Tree, Parent string
	Missing      bool
}

// CommitShapes reads the tree and first parent of every commit in one git
// process, however many there are.
func CommitShapes(hashes []string) (map[string]CommitShape, error) {
	for _, h := range hashes {
		if h == "" || !IsValidCommitish(h) {
			return nil, fmt.Errorf("invalid commit hash %q", h)
		}
	}
	out := make(map[string]CommitShape, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	objs, err := catFileBatch(ctx, hashes, MaxOutputBytes)
	if err != nil {
		return nil, err
	}
	for i, o := range objs {
		if o.missing || o.kind != "commit" {
			out[hashes[i]] = CommitShape{Missing: true}
			continue
		}
		var shape CommitShape
		for _, line := range strings.Split(string(o.body), "\n") {
			if line == "" {
				break // the headers end at the first blank line
			}
			if t, ok := strings.CutPrefix(line, "tree "); ok && shape.Tree == "" {
				shape.Tree = t
			} else if p, ok := strings.CutPrefix(line, "parent "); ok && shape.Parent == "" {
				shape.Parent = p
			}
		}
		out[hashes[i]] = shape
	}
	return out, nil
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
	out, err := execGit(ctx, dir, stdin, max, args)
	if err != nil {
		if out == nil {
			return nil, err
		}
		return out.buf.Bytes(), err
	}
	if out.truncated {
		return nil, fmt.Errorf("git %s: output exceeded %d bytes", strings.Join(args, " "), max)
	}
	return out.buf.Bytes(), nil
}

// execGit is the one place githints starts git. stdout is capped at max
// bytes (the caller decides whether passing it is a truncation or an error),
// stderr at 8 KiB for the error message. On a non-zero exit the error wraps
// *exec.ExitError and stdout is returned with it; when ctx ended the run,
// there is no output.
func execGit(ctx context.Context, dir string, stdin io.Reader, max int, args []string) (*capWriter, error) {
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
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.buf.String()))
	}
	return out, nil
}

// AddNote adds a git note to HEAD. It uses --force so repeated commits or
// amends overwrite the previous note for that ref. This is used by the
// post-commit hook to anchor the per-commit Merkle root.
func AddNote(ref, message string) error {
	_, err := run("notes", "--ref="+ref, "add", "--force", "-m", message, "HEAD")
	return err
}

// DeleteRef deletes a ref, e.g. refs/notes/githints during uninstall. A
// missing ref is not an error.
func DeleteRef(ref string) error {
	_, err := run("update-ref", "-d", ref)
	return err
}

// AnchorsRef is where the post-commit hook writes Merkle anchors.
const AnchorsRef = "refs/notes/githints"

// networkTimeout bounds a push or fetch, which waits on a remote and maybe
// on a person typing a password.
const networkTimeout = 2 * time.Minute

// Remotes lists the configured remote names.
func Remotes() ([]string, error) {
	out, err := run("remote")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Fields(out), nil
}

// checkRemote accepts only the name of a configured remote. The name lands in
// argv, so anything else -- a URL, or "--upload-pack=..." -- is refused rather
// than passed to git.
func checkRemote(remote string) error {
	remotes, err := Remotes()
	if err != nil {
		return err
	}
	for _, r := range remotes {
		if r == remote && !strings.HasPrefix(r, "-") {
			return nil
		}
	}
	return fmt.Errorf("%q is not a configured remote (git remote lists: %s)", remote, strings.Join(remotes, ", "))
}

// PushAnchors pushes refs/notes/githints to a configured remote. It never
// forces: anchors written by another clone are not overwritten.
func PushAnchors(remote string) error {
	if err := checkRemote(remote); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	_, err := runCtx(ctx, "push", "--", remote, AnchorsRef+":"+AnchorsRef)
	return err
}

// FetchAnchors fetches refs/notes/githints from a configured remote into the
// local ref, only as a fast-forward: local anchors are never discarded.
func FetchAnchors(remote string) error {
	if err := checkRemote(remote); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	_, err := runCtx(ctx, "fetch", "--", remote, AnchorsRef+":"+AnchorsRef)
	return err
}

// Blamed is the commit that last changed one line of a file, with the file's
// name in that commit (blame follows renames) and the commit's subject.
type Blamed struct {
	Commit, File, Subject string
	Uncommitted           bool // the line is changed in the working tree
}

// BlameLine asks which commit last changed line n of file. No textconv
// filter runs, and the path is after "--".
func BlameLine(file string, n int) (Blamed, error) {
	if n < 1 {
		return Blamed{}, fmt.Errorf("line must be 1 or more, got %d", n)
	}
	out, err := run("blame", "--porcelain", "--no-textconv", "-L", fmt.Sprintf("%d,%d", n, n), "--", file)
	if err != nil {
		return Blamed{}, err
	}
	var b Blamed
	for i, l := range strings.Split(out, "\n") {
		if i == 0 {
			f := strings.Fields(l)
			if len(f) == 0 || !IsValidCommitish(f[0]) || f[0] == "" {
				return Blamed{}, fmt.Errorf("blame: unexpected output %q", l)
			}
			b.Commit = f[0]
			b.Uncommitted = strings.Trim(b.Commit, "0") == ""
			continue
		}
		if strings.HasPrefix(l, "\t") {
			break // the line's content: never parsed as a header
		}
		if v, ok := strings.CutPrefix(l, "filename "); ok {
			b.File = v
		} else if v, ok := strings.CutPrefix(l, "summary "); ok {
			b.Subject = v
		}
	}
	return b, nil
}

// RefHash returns the object a ref points at, or "" when it does not exist.
func RefHash(ref string) string {
	out, err := run("rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return out
}

// RefExists reports whether a ref exists in the current repository.
func RefExists(ref string) bool {
	_, err := run("show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// requireHash is IsValidCommitish for callers where "" (the working tree) is
// not an answer: the value lands in argv, so it must be hex and non-empty.
func requireHash(hash string) error {
	if hash == "" || !IsValidCommitish(hash) {
		return fmt.Errorf("invalid commit hash %q", hash)
	}
	return nil
}

// CommitMessage returns the raw message of one commit. It is untrusted text:
// whoever made the commit wrote it, which may be a clone's author. Callers
// look for one fixed line shape in it and never record or display the text.
func CommitMessage(hash string) (string, error) {
	if err := requireHash(hash); err != nil {
		return "", err
	}
	return run("show", "-s", "--no-notes", "--format=%B", hash)
}

// maxRevertRefs bounds how many reverted commits one message can name.
const maxRevertRefs = 20

// revertedRe is the line `git revert` writes. Only a full 40- or 64-hex hash
// is accepted: git never abbreviates it, and an abbreviation could name a
// different commit once the repository grows.
var revertedRe = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{40}|[0-9a-f]{64})\b`)

// RevertedCommits returns the commits a message says it reverts, in order and
// without repeats, at most maxRevertRefs of them. A message is a claim, not
// proof: callers must check the tree before believing it.
func RevertedCommits(message string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range revertedRe.FindAllStringSubmatch(message, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
		if len(out) == maxRevertRefs {
			break
		}
	}
	return out
}

// ResolveCommit returns the full hash of the commit that hash names, and false
// if there is no such commit (a shallow clone, or a message naming nothing).
func ResolveCommit(hash string) (string, bool) {
	if requireHash(hash) != nil {
		return "", false
	}
	out, err := run("rev-parse", "--verify", "--quiet", hash+"^{commit}")
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// ParentCommit returns the first parent of a commit, and false for a root
// commit.
func ParentCommit(hash string) (string, bool) {
	if requireHash(hash) != nil {
		return "", false
	}
	out, err := run("rev-parse", "--verify", "--quiet", hash+"^1")
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// PathAt identifies what path is in the tree of one commit: "<mode> <type>
// <object>" as `git ls-tree` prints it, or "" when the path does not exist
// there. Two commits hold identical content at a path exactly when the values
// are equal. An error means git failed, which is not the same as absent.
func PathAt(hash, path string) (string, error) {
	if err := requireHash(hash); err != nil {
		return "", err
	}
	out, err := run("ls-tree", hash, "--", path)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	// "<mode> <type> <object>\t<path>": the path is not part of the identity.
	ident, _, _ := strings.Cut(strings.SplitN(out, "\n", 2)[0], "\t")
	return ident, nil
}
