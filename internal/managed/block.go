package managed

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnsureBlock inserts or replaces githints' own block in a text file, leaving
// everything around it untouched. body is the block's content, without the
// start/end markers.
//
// Used for .gitignore, AGENTS.md, and CLAUDE.md so re-running init updates its
// own block and never clobbers hand-written content.
func EnsureBlock(path, startMarker, endMarker string, body []string) error {
	want, have, _, err := renderBlock(path, startMarker, endMarker, body)
	if err != nil {
		return err
	}
	if want == have {
		return nil
	}
	return os.WriteFile(path, []byte(want), 0o644)
}

// renderBlock returns what path should hold with the managed block set to
// body, what it holds now, and whether it has a block at all.
func renderBlock(path, startMarker, endMarker string, body []string) (want, have string, found bool, err error) {
	// A cloned repository can commit CLAUDE.md -> ~/.bashrc. Writing through
	// it would append githints' block to a file outside the repo, so a link
	// (or a junction, or anything else that isn't a plain file) is refused.
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return "", "", false, fmt.Errorf("%s is not a regular file (symlink?); refusing to rewrite it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		// Do not fall through to writing on a permission or I/O error: the
		// file exists and we simply cannot see it, so writing would replace
		// the user's content with nothing but our own block.
		return "", "", false, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")

	newBlock := append(append([]string{startMarker}, body...), endMarker)

	start, end := -1, -1
	for i, line := range lines {
		if line == startMarker {
			start = i
		}
		if line == endMarker && start != -1 {
			end = i
			break
		}
	}
	found = start != -1 && end != -1

	var out []string
	switch {
	case found:
		// Replace the managed block in place.
		out = append(lines[:start:start], append(newBlock, lines[end+1:]...)...)
	case len(data) == 0:
		// Brand-new file: no separator, or it opens with blank lines.
		out = newBlock
	default:
		// No managed block yet. Append it, keeping any existing content.
		out = append(lines, "")
		out = append(out, newBlock...)
	}

	// Trim trailing blank lines so re-runs don't add infinite whitespace.
	for len(out) > 0 && out[len(out)-1] == "" && (len(out) == 1 || out[len(out)-2] == "") {
		out = out[:len(out)-1]
	}

	want = strings.Join(out, "\n")
	if want != "" && !strings.HasSuffix(want, "\n") {
		want += "\n"
	}
	return want, string(data), found, nil
}

// Block is one managed block githints keeps in a repository file.
type Block struct {
	Path, Start, End string
	Body             []string
}

// BlockState is how a file's managed block compares with what this binary
// writes.
type BlockState int

const (
	// BlockCurrent: the block is what init would write now.
	BlockCurrent BlockState = iota
	// BlockStale: the block exists and differs.
	BlockStale
	// BlockMissing: the file, or the block in it, is absent.
	BlockMissing
)

// State compares the block on disk with b.
func (b Block) State() (BlockState, error) {
	want, have, found, err := renderBlock(b.Path, b.Start, b.End, b.Body)
	switch {
	case err != nil:
		return BlockMissing, err
	case !found:
		return BlockMissing, nil
	case want != have:
		return BlockStale, nil
	}
	return BlockCurrent, nil
}

// Ensure writes b.
func (b Block) Ensure() error { return EnsureBlock(b.Path, b.Start, b.End, b.Body) }

// Blocks are the managed blocks of a repository as this binary writes them.
// The .gitignore block keeps the mode (private or -share) it was written in.
func Blocks(root string) []Block {
	return []Block{
		{filepath.Join(root, "AGENTS.md"), MarkdownStart, MarkdownEnd, AgentsBlock()},
		{filepath.Join(root, "CLAUDE.md"), MarkdownStart, MarkdownEnd, ClaudeBlock()},
		{filepath.Join(root, ".gitignore"), GitignoreStart, GitignoreEnd, gitignoreBody(GitignoreShared(root))},
	}
}

// GitignoreShared reports whether .gitignore's managed block is the -share
// form, which ignores only the state files.
func GitignoreShared(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == GitignoreStart:
			in = true
		case line == GitignoreEnd:
			return false
		case in && line == ".githints/store.db*":
			return true
		}
	}
	return false
}

// RemoveBlock deletes the block bracketed by startMarker/endMarker, leaving
// everything else untouched. Missing markers or a missing file are not errors.
// It returns whether a block was removed.
func RemoveBlock(path, startMarker, endMarker string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	lines := strings.Split(string(data), "\n")
	start, end := -1, -1
	for i, line := range lines {
		if line == startMarker {
			start = i
		}
		if line == endMarker && start != -1 {
			end = i
			break
		}
	}
	if start == -1 || end == -1 {
		return false, nil
	}
	out := append(lines[:start], lines[end+1:]...)
	// Trim the blank line EnsureBlock left before the block.
	for len(out) > 0 && out[len(out)-1] == "" && (len(out) == 1 || out[len(out)-2] == "") {
		out = out[:len(out)-1]
	}
	content := strings.Join(out, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// EnsureGitignore writes a managed .gitignore block for githints' output.
// In private mode the whole .githints/ directory is ignored; in shared mode
// only the state files (store.db, index.db, .salt, config.json) are ignored so
// that rendered markdown can be committed.
func EnsureGitignore(root string, share bool) error {
	return EnsureBlock(filepath.Join(root, ".gitignore"), GitignoreStart, GitignoreEnd, gitignoreBody(share))
}

func gitignoreBody(share bool) []string {
	body := []string{".githints/"}
	if share {
		body = []string{
			".githints/store.db*",
			".githints/index.db*",
			".githints/.salt",
			".githints/.salt.new", // left behind if a legacy-location rotation fails
			".githints/repo-id",   // keys this machine's salt; a shared one could steer it
			".githints/config.json",
			".githints/notes/",     // private by decision: notes can hold anything an agent was told
			".githints/notes.html", // the viewer is rebuilt from them
		}
	}
	return body
}

// EnsureAgentFiles writes the instruction blocks agents actually read.
// Without these, an agent in a freshly initialized repo has no idea the
// githints tools exist or when to call them.
func EnsureAgentFiles(root string) error {
	if err := EnsureBlock(filepath.Join(root, "AGENTS.md"), MarkdownStart, MarkdownEnd, AgentsBlock()); err != nil {
		return err
	}
	return EnsureBlock(filepath.Join(root, "CLAUDE.md"), MarkdownStart, MarkdownEnd, ClaudeBlock())
}
