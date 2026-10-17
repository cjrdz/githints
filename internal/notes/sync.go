package notes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/safefs"
	"github.com/cjrdz/githints/internal/secrets"
)

// Problem is a file in the notes directory that was not indexed, and why.
type Problem struct {
	File   string
	Reason string
}

// SyncResult reports what Sync did.
type SyncResult struct {
	Indexed   int // new or changed files read and indexed
	Unchanged int // files already indexed as they are
	Removed   int // indexed notes whose file is gone or no longer acceptable
	Problems  []Problem
}

// Changed reports whether the index differs from before the sync.
func (r SyncResult) Changed() bool { return r.Indexed+r.Removed > 0 }

// stateDir is the .githints directory under a repository root.
func stateDir(root string) string { return filepath.Join(root, ".githints") }

// Sync makes the index match the notes directory: new and changed files are
// read and indexed, removed ones dropped, and anything unacceptable is left out
// and reported. It is cheap when nothing changed (a directory listing and one
// stat per note), so every reader calls it first and hand edits show up without
// any other step.
//
// A file that fails a check is removed from the index, not left with its older
// content: an edit that adds a credential must stop the old text being served,
// not just refuse the new.
func Sync(db *index.Store, root string) (SyncResult, error) {
	var res SyncResult
	known, err := db.NoteStates()
	if err != nil {
		return res, err
	}

	var entries []fs.DirEntry
	r, err := safefs.OpenExisting(stateDir(root))
	switch {
	case err == nil:
		defer r.Close()
		if entries, err = listNotes(r); err != nil {
			return res, err
		}
	case errors.Is(err, fs.ErrNotExist):
		// No .githints yet: there are no notes.
	default:
		return res, err
	}

	keep := map[string]bool{}
	problem := func(file, reason string) {
		res.Problems = append(res.Problems, Problem{File: file, Reason: reason})
	}
	for _, e := range entries {
		file := e.Name()
		if !strings.HasSuffix(file, ".md") {
			continue
		}
		name := strings.TrimSuffix(file, ".md")
		if !ValidName(name) {
			problem(file, "file name must be lowercase letters, digits and hyphens, then .md")
			continue
		}
		if _, ok := secrets.Scan(name); ok {
			problem(file, "the file name matches a known secret pattern")
			continue
		}
		rel := Dir + "/" + file
		// Lstat, not Stat: ReadFileIn's own check follows a link that stays
		// inside .githints/, so notes/x.md -> ../store.db would read the store
		// as a note. A note is a plain file or it is not a note.
		fi, err := r.Lstat(rel)
		if err != nil {
			problem(file, err.Error())
			continue
		}
		if !fi.Mode().IsRegular() {
			problem(file, "not a regular file (a link?)")
			continue
		}
		if len(keep) >= MaxNotes {
			problem(file, fmt.Sprintf("more than %d notes; not indexed", MaxNotes))
			continue
		}
		state := index.NoteState{MTime: fi.ModTime().UnixNano(), Size: fi.Size()}
		if prev, ok := known[name]; ok && prev == state {
			keep[name] = true
			res.Unchanged++
			continue
		}
		data, err := safefs.ReadSingleLinkFileIn(r, rel, MaxFileBytes)
		if err != nil {
			problem(file, err.Error())
			continue
		}
		doc, err := Parse(name, data)
		if err != nil {
			problem(file, err.Error())
			continue
		}
		if err := db.UpsertNote(index.Note{
			Name: name, Title: doc.Title, Body: doc.Body, Tags: doc.Tags,
			Files: doc.Files, Changes: doc.Changes, Links: doc.Links(),
			MTime: state.MTime, Size: state.Size,
		}); err != nil {
			return res, err
		}
		keep[name] = true
		res.Indexed++
	}

	for name := range known {
		if !keep[name] {
			if err := db.DeleteNote(name); err != nil {
				return res, err
			}
			res.Removed++
		}
	}
	return res, nil
}

// listNotes returns the entries of .githints/notes, sorted, or nothing if the
// directory does not exist. The directory must be a plain directory.
func listNotes(r *os.Root) ([]fs.DirEntry, error) {
	fi, err := r.Lstat(Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, fmt.Errorf(".githints/%s is not a plain directory (a link?); refusing to read it", Dir)
	}
	d, err := r.Open(Dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
