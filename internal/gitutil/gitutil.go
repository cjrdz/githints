// Package gitutil shells out to the local git binary for the few things
// githints needs: the current commit, which files it touched, and a
// compact diff stat per file.
package gitutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(out.String()), nil
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
	if hash == "" {
		return run("diff", "HEAD", "--", file)
	}
	return run("show", "--pretty=format:", hash, "--", file)
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
	var out string
	var err error
	if hash == "" {
		out, err = run("diff", "HEAD", "--", file)
	} else {
		out, err = run("show", "--pretty=format:", hash, "--", file)
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

// CommitMessage returns the subject line of the given commit.
func CommitMessage(hash string) (string, error) {
	return run("log", "-1", "--pretty=%s", hash)
}

// ChangedFiles lists files touched by the given commit (vs its parent).
// Falls back to the diff against an empty tree for the very first commit.
func ChangedFiles(hash string) ([]string, error) {
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

// DiffStat returns a compact "+N -M" string for one file in one commit.
func DiffStat(hash, file string) string {
	out, err := run("diff", "--numstat", hash+"^", hash, "--", file)
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
	out, err := run("diff", "--numstat", "HEAD", "--", file)
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

// AddNote adds a git note to HEAD. It uses --force so repeated commits or
// amends overwrite the previous note for that ref. This is used by the
// post-commit hook to anchor the per-commit Merkle root.
func AddNote(ref, message string) error {
	_, err := run("notes", "--ref="+ref, "add", "--force", "-m", message, "HEAD")
	return err
}
