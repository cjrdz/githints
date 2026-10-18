// Package safefs confines githints' own file I/O to a directory it owns.
//
// Path validation elsewhere (recorder.ValidateFilePath) is lexical, so it
// cannot see a symlink or a Windows junction. A repository is attacker-
// controlled input -- shared mode commits the contents of .githints/, and any
// clone can ship links -- so every read and write of a githints-managed file
// goes through an os.Root, which resolves each component against the directory
// and refuses to traverse out of it. The directory itself is checked with
// Lstat first, because os.OpenRoot follows a link in its final component.
package safefs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrTooLarge is returned by ReadFile when the file exceeds its cap.
var ErrTooLarge = errors.New("file exceeds size limit")

// checkRealDir reports an error unless dir is a plain directory: not a
// symlink, and not a junction or other reparse point (which Go reports as
// irregular on Windows).
func checkRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a plain directory (symlink or junction?); refusing to use it", dir)
	}
	return nil
}

// EnsureDir creates dir if it is missing and then confirms it is a plain
// directory. MkdirAll alone would silently accept a symlink in its place.
func EnsureDir(dir string, perm fs.FileMode) error {
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	return checkRealDir(dir)
}

// Open returns an os.Root for dir, creating it first if needed.
func Open(dir string, perm fs.FileMode) (*os.Root, error) {
	if err := EnsureDir(dir, perm); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// OpenExisting returns an os.Root for a directory that must already exist and
// be a plain directory. Unlike Open it never creates anything, for readers that
// should find nothing rather than make something.
func OpenExisting(dir string) (*os.Root, error) {
	if err := checkRealDir(dir); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// WriteFile writes data to rel under dir, creating parent directories. It
// refuses to follow any link out of dir.
func WriteFile(dir, rel string, data []byte, dirPerm, filePerm fs.FileMode) error {
	r, err := Open(dir, dirPerm)
	if err != nil {
		return err
	}
	defer r.Close()
	return WriteFileIn(r, rel, data, dirPerm, filePerm)
}

// WriteFileIn is WriteFile against an already-open root, for callers writing
// many files in one pass.
func WriteFileIn(r *os.Root, rel string, data []byte, dirPerm, filePerm fs.FileMode) error {
	if err := refuseLinks(r, rel); err != nil {
		return err
	}
	if d := filepath.Dir(rel); d != "." {
		if err := r.MkdirAll(d, dirPerm); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := r.WriteFile(rel, data, filePerm); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// refuseLinks reports an error if rel, or any directory on the way to it, is a
// symlink or a Windows junction. os.Root follows a link that stays inside the
// root, so without this a link planted in .githints/ (shared mode commits that
// directory, and a clone can ship links) turns every rewrite of CHANGES.md, a
// hint or a viewer page into an overwrite of whatever it points at inside the
// directory: `CHANGES.md -> store.db` destroyed the change log on the next
// record. A path that does not exist yet is fine; it will be created plainly.
func refuseLinks(r *os.Root, rel string) error {
	cur := ""
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = path.Join(cur, part)
		fi, err := r.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("%s is a link; refusing to write through it", cur)
		}
	}
	return nil
}

// ReadFile reads rel under dir, refusing anything that is not a regular file
// (a FIFO would block, /dev/zero would never end) and anything larger than
// max bytes. dir must already exist; ReadFile never creates it.
func ReadFile(dir, rel string, max int64) ([]byte, error) {
	if err := checkRealDir(dir); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ReadFileIn(r, rel, max)
}

// ReadFileIn is ReadFile against an already-open root.
func ReadFileIn(r *os.Root, rel string, max int64) ([]byte, error) {
	return readFileIn(r, rel, max, false)
}

// ReadSingleLinkFileIn is ReadFileIn for a file that must have exactly one
// name. A hard link is a regular file, so the checks above cannot see one: a
// second name for .githints/.salt, made by anyone who can write the directory,
// would read as an ordinary file of whatever name it was given. Hard links do
// not travel in a git clone, so this guards the local directory only. Where the
// platform cannot report a link count the check is skipped.
func ReadSingleLinkFileIn(r *os.Root, rel string, max int64) ([]byte, error) {
	return readFileIn(r, rel, max, true)
}

func readFileIn(r *os.Root, rel string, max int64, singleLink bool) ([]byte, error) {
	// Stat before Open: opening a FIFO blocks until a writer appears, so the
	// regular-file check has to happen first. r.Stat follows a symlink only
	// while it stays inside the root. The second check after Open closes the
	// window where the path is swapped between the two calls.
	fi, err := r.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err = f.Stat(); err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: %w (%d > %d bytes)", rel, ErrTooLarge, fi.Size(), max)
	}
	if singleLink {
		if n, ok := linkCount(f); ok && n > 1 {
			return nil, fmt.Errorf("%s has %d hard links; refusing to read it", rel, n)
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: %w (> %d bytes)", rel, ErrTooLarge, max)
	}
	return data, nil
}

// ReadRepoFile reads rel under a repository root the user chose (the
// toplevel git reported, or -root). Unlike ReadFile it does not require root
// itself to be a plain directory -- a checkout reached through a symlinked
// path is normal -- but rel still cannot resolve outside it, and the same
// regular-file and size checks apply. For files a clone controls, such as
// go.mod and tsconfig.json.
func ReadRepoFile(root, rel string, max int64) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ReadFileIn(r, rel, max)
}

// RefuseTrackedState returns an error if the githints state file at path
// (<repo>/.githints/<name>) is tracked by git. Such a file arrived in a clone:
// a database somebody else wrote, possibly with rows, triggers or views of
// their choosing. Outside a git repository there is nothing to check.
func RefuseTrackedState(path string, isTracked func(root, rel string) (bool, error)) error {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != ".githints" {
		return nil
	}
	rel := filepath.Join(".githints", filepath.Base(path))
	tracked, err := isTracked(filepath.Dir(dir), rel)
	if err != nil || !tracked {
		return nil
	}
	return fmt.Errorf("%s is tracked by git, so it came from the repository rather than this machine; "+
		"run `git rm --cached %s` and delete the file (githints recreates it)", rel, filepath.ToSlash(rel))
}

// StateDirPerm is the mode for .githints/ itself. It holds the change log and
// the index, which can describe code that isn't public; on a shared host the
// default 0755 made every summary readable by every local user. Git does not
// track directory modes, so shared mode is unaffected.
const StateDirPerm fs.FileMode = 0o700

// PrepareDatabase readies path (a database inside .githints/) for opening:
// the directory must be a plain directory and is restricted to the owner, and
// a new database file is pre-created 0600 so SQLite -- which gives its WAL
// and SHM files the mode of the database -- never creates any of the three
// world-readable. An existing database with a looser mode is tightened.
func PrepareDatabase(path string) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir, StateDirPerm); err != nil {
		return err
	}
	if filepath.Base(dir) == ".githints" {
		if err := os.Chmod(dir, StateDirPerm); err != nil {
			return err
		}
	}
	// O_EXCL never follows a symlink and fails if anything is already there.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close() // empty file; SQLite initializes it on first use
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (symlink?); refusing to open it", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o600)
	}
	return nil
}

// Writer writes many files under one directory through a single os.Root, for
// a pass that writes hundreds of them (the index notes). Each write keeps
// WriteFileIn's guarantee, no link is written through, but checks each
// directory on the way once per pass rather than once per file, and leaves a
// file whose content is already what would be written untouched: no write,
// no new modification time for an editor or a vault to react to.
//
// A directory checked earlier in the pass is not checked again, so a link
// swapped in mid-pass by a process with write access to the directory is
// not seen; such a process can already write the files directly.
//
// A Writer is safe for concurrent use: directory checks and creation are
// serialized, file writes are not.
//
// Keeping each checked directory open as its own os.Root, so that a file's
// operations resolve one name, made an isolated render benchmark 46% faster
// but a real full index about 30 ms slower: opening and closing a root per
// directory, under the lock, costs more than it saves. Measured, reverted.
type Writer struct {
	root     *os.Root
	dirPerm  fs.FileMode
	filePerm fs.FileMode
	mu       sync.Mutex
	verified map[string]bool // slash paths of directories checked this pass

	written, unchanged atomic.Int64
}

// Written and Unchanged count the files written and the files left as they
// were.
func (w *Writer) Written() int   { return int(w.written.Load()) }
func (w *Writer) Unchanged() int { return int(w.unchanged.Load()) }

// NewWriter opens dir (creating it) for a pass of writes.
func NewWriter(dir string, dirPerm, filePerm fs.FileMode) (*Writer, error) {
	r, err := Open(dir, dirPerm)
	if err != nil {
		return nil, err
	}
	return &Writer{root: r, dirPerm: dirPerm, filePerm: filePerm, verified: map[string]bool{}}, nil
}

// Close releases the root.
func (w *Writer) Close() error { return w.root.Close() }

// WriteFile writes data to rel, creating parent directories, unless rel
// already holds exactly data. It refuses a link at rel or on the way to it.
func (w *Writer) WriteFile(rel string, data []byte) error {
	rel = path.Clean(filepath.ToSlash(rel))
	w.mu.Lock()
	err := w.ensureDir(path.Dir(rel))
	w.mu.Unlock()
	if err != nil {
		return err
	}
	fi, err := w.root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
		return fmt.Errorf("%s is a link; refusing to write through it", rel)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file; refusing to write it", rel)
	case fi.Size() == int64(len(data)):
		// Same size: compare before rewriting. Reading costs less than a
		// write, and an unchanged note keeps its modification time.
		if old, err := readFileIn(w.root, rel, int64(len(data))+1, false); err == nil && bytes.Equal(old, data) {
			w.unchanged.Add(1)
			return nil
		}
	}
	if err := w.root.WriteFile(rel, data, w.filePerm); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	w.written.Add(1)
	return nil
}

// ensureDir makes sure dir is a real directory, creating it and its parents
// as needed, checking each one once per pass. Called with w.mu held.
func (w *Writer) ensureDir(dir string) error {
	if dir == "." || dir == "/" || dir == "" || w.verified[dir] {
		return nil
	}
	if err := w.ensureDir(path.Dir(dir)); err != nil {
		return err
	}
	fi, err := w.root.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := w.root.Mkdir(dir, w.dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		// Created here, or by a racing writer: check what is there now.
		if fi, err = w.root.Lstat(dir); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is a link or not a directory; refusing to write through it", dir)
	}
	w.verified[dir] = true
	return nil
}
