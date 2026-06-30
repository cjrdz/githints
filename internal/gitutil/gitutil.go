// Package gitutil shells out to the local git binary for the few things
// githints needs: the current commit, which files it touched, and a
// compact diff stat per file.
package gitutil

import (
	"bytes"
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

// LastCommitHash returns the hash of HEAD, or "" if there is no commit yet
// (e.g. called from a pre-commit-style context before any commit exists).
func LastCommitHash() string {
	hash, err := run("rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return hash
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
