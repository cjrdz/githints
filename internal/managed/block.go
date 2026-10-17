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
	// A cloned repository can commit CLAUDE.md -> ~/.bashrc. Writing through
	// it would append githints' block to a file outside the repo, so a link
	// (or a junction, or anything else that isn't a plain file) is refused.
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (symlink?); refusing to rewrite it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		// Do not fall through to writing on a permission or I/O error: the
		// file exists and we simply cannot see it, so writing would replace
		// the user's content with nothing but our own block.
		return fmt.Errorf("read %s: %w", path, err)
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

	var out []string
	switch {
	case start != -1 && end != -1:
		// Replace the managed block in place.
		out = append(lines[:start], append(newBlock, lines[end+1:]...)...)
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

	content := strings.Join(out, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
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
	return EnsureBlock(filepath.Join(root, ".gitignore"), GitignoreStart, GitignoreEnd, body)
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
