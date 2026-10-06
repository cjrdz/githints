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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
