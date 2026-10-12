// Package core holds helpers shared across the CLI subpackages.
package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
)

// ErrNotInitialized is returned by every command but init when the repository
// has no .githints/store.db yet.
var ErrNotInitialized = errors.New("githints is not set up in this repository")

// OpenInitialized is OpenRootAndStore for every command except init. Those
// used to create .githints/store.db as a side effect, so `githints serve` in
// a fresh repository came up with no hooks, no salt and no explanation.
func OpenInitialized() (root string, st *store.Store, err error) {
	root, err = gitutil.RepoRoot()
	if err != nil {
		return "", nil, fmt.Errorf("not inside a git repo: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".githints", "store.db")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("%w (%s); run `githints init` first", ErrNotInitialized, root)
		}
		return "", nil, err
	}
	return OpenRootAndStore()
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
