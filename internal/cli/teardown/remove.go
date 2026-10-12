// Package teardown implements the `githints remove` and `githints uninstall`
// commands.
package teardown

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/managed"
)

// Result is what happened for one teardown step.
type Result struct {
	name    string
	detail  string
	err     error
	skipped bool
}

func printRemoveResult(root string, r Result, dryRun bool) {
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	if r.skipped {
		fmt.Printf("  %-20s skipped: %s\n", r.name, r.detail)
		return
	}
	if r.err != nil {
		fmt.Printf("  %-20s FAILED: %v\n", r.name, r.err)
		return
	}
	if r.detail != "" {
		fmt.Printf("  %-20s %s %s\n", r.name, verb, r.detail)
	} else {
		fmt.Printf("  %-20s %s\n", r.name, verb)
	}
}

// removeHooks deletes githints-managed hooks and restores any hook that init
// -chain moved aside. Hooks not written by githints are left alone.
func removeHooks(root string, dryRun bool) ([]Result, error) {
	hooksDir, err := gitutil.HooksDir(root)
	if err != nil {
		return nil, fmt.Errorf("locate git hooks directory: %w", err)
	}
	var out []Result
	for _, name := range []string{"post-commit", "pre-commit"} {
		path := filepath.Join(hooksDir, name)
		r := Result{name: name}
		exists, isManaged, err := managed.HookExistsAndManaged(path)
		if err != nil {
			r.err = err
			out = append(out, r)
			continue
		}
		if !exists {
			r.skipped = true
			r.detail = "not installed"
			out = append(out, r)
			continue
		}
		if !isManaged {
			r.skipped = true
			r.detail = "not installed by githints"
			out = append(out, r)
			continue
		}
		if dryRun {
			r.detail = path
			out = append(out, r)
			continue
		}
		if err := os.Remove(path); err != nil {
			r.err = err
			out = append(out, r)
			continue
		}
		// Restore a chained hook that was moved aside by init -chain.
		aside := path + managed.ChainedSuffix
		if _, err := os.Lstat(aside); err == nil {
			if err := os.Rename(aside, path); err != nil {
				r.err = fmt.Errorf("restore chained hook: %w", err)
				out = append(out, r)
				continue
			}
			r.detail = path + " (restored " + filepath.Base(aside) + ")"
		} else {
			r.detail = path
		}
		out = append(out, r)
	}
	return out, nil
}

// removeAnchors drops refs/notes/githints. Missing notes are not an error.
func removeAnchors(dryRun bool) (bool, error) {
	if !gitutil.RefExists("refs/notes/githints") {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := gitutil.DeleteRef("refs/notes/githints"); err != nil {
		return false, err
	}
	return true, nil
}

// removeSalt deletes the integrity salt. The salt lives outside the repo
// unless this is a legacy install; either way SaltPath knows where it is.
func removeSalt(root string, dryRun bool) (bool, error) {
	path := integrity.SaltPath(root)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if dryRun {
		return true, nil
	}
	return true, os.Remove(path)
}

// removeGithintsDir deletes .githints/ and everything under it.
func removeGithintsDir(root string, dryRun bool) (bool, error) {
	dir := filepath.Join(root, ".githints")
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if dryRun {
		return true, nil
	}
	return true, os.RemoveAll(dir)
}
