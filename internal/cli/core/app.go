// Package core holds helpers shared across the CLI subpackages.
package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/store"
)

// ErrNotInitialized is returned by every command but init when the repository
// has no .githints/store.db yet.
var ErrNotInitialized = errors.New("githints is not set up in this repository")

// OpenInitialized is OpenRootAndStore for every command except init. Those
// used to create .githints/store.db as a side effect, so `githints serve` in
// a fresh repository came up with no hooks, no salt and no explanation.
func OpenInitialized() (root string, st *store.Store, err error) {
	if _, err := InitializedRoot(); err != nil {
		return "", nil, err
	}
	return OpenRootAndStore()
}

// InitializedRoot resolves the repository and refuses one where `githints
// init` has not run. Commands that need only the index (not the store) call
// it before index.Open, which would otherwise create .githints/ itself.
func InitializedRoot() (string, error) {
	root, err := gitutil.RepoRoot()
	if err != nil {
		return "", fmt.Errorf("not inside a git repo: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".githints", "store.db")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w (%s); run `githints init` first", ErrNotInitialized, root)
		}
		return "", err
	}
	return root, nil
}

// OpenRootAndStore resolves the repository and opens (creating if needed)
// its store. Only init should call it directly; see OpenInitialized.
func OpenRootAndStore() (root string, st *store.Store, err error) {
	root, err = gitutil.RepoRoot()
	if err != nil {
		return "", nil, fmt.Errorf("not inside a git repo: %w", err)
	}
	st, err = store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		return "", nil, err
	}
	return root, st, nil
}

// OpenExistingIndex opens index.db if it exists, and returns nil if it does
// not: opening a missing one would create it, and the commands that only read
// the index treat it as optional.
func OpenExistingIndex(root string) *index.Store {
	path := lang.IndexDBPath(root)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	db, err := index.Open(path)
	if err != nil {
		return nil // a derived cache: unreadable is the same as absent here
	}
	return db
}
