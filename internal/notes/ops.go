package notes

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/safefs"
	"github.com/cjrdz/githints/internal/secrets"
	"github.com/cjrdz/githints/internal/textsafe"
)

const (
	notePerm = 0o600
	dirPerm  = 0o700

	// MaxQueryLen and the token limits keep a search an ordinary lookup.
	MaxQueryLen    = 500
	maxQueryTokens = 10
	maxTokenLen    = 64
	// A word shorter than this is matched exactly rather than as a prefix: a
	// one-letter prefix matches most of the vocabulary, and a few of them over
	// many large notes cost seconds of CPU.
	minPrefixLen   = 3
	maxSearchLimit = 100
)

// RecordInput describes a note to write.
type RecordInput struct {
	Name    string // optional; derived from the title when empty
	Title   string
	Body    string
	Tags    []string
	Files   []string
	Changes []int64
	Replace bool // overwrite an existing note of that name
}

// Record writes a note and indexes it, returning its name.
//
// The note is built, written out as a file, and parsed back before anything
// touches the disk, so what is stored is exactly what a later read will accept.
// Without Replace an existing note is never overwritten, atomically: the file
// is created exclusively.
func Record(db *index.Store, root string, in RecordInput) (string, error) {
	doc := Doc{Title: in.Title, Body: in.Body, Tags: in.Tags, Files: in.Files, Changes: in.Changes}
	name := in.Name
	if name == "" {
		if name = Slug(in.Title); name == "" {
			return "", errors.New("the title has no letters or digits to make a name from; pass a name")
		}
	}
	name, err := NormalizeName(name)
	if err != nil {
		return "", err
	}
	// The name is shown in listings, becomes a file name, and travels into
	// export packs, so it is scanned like any other text. (Only some patterns
	// fit the name alphabet, but one does.)
	if pattern, ok := secrets.Scan(name); ok {
		return "", fmt.Errorf("refusing: the note name matches a known secret pattern (%s)", pattern)
	}
	if err := doc.Check(""); err != nil {
		return "", err
	}
	data := doc.Marshal()
	if _, err := Parse(name, data); err != nil {
		return "", fmt.Errorf("the note would not read back: %w", err)
	}

	// Pick up hand-made notes first, so the count and the name check see them.
	if _, err := Sync(db, root); err != nil {
		return "", err
	}
	_, exists, err := db.GetNote(name)
	if err != nil {
		return "", err
	}
	if !exists {
		if n, err := db.NoteCount(); err != nil {
			return "", err
		} else if n >= MaxNotes {
			return "", fmt.Errorf("there are already %d notes (max %d); delete one first", n, MaxNotes)
		}
	}

	r, err := safefs.Open(stateDir(root), safefs.StateDirPerm)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err := r.MkdirAll(Dir, dirPerm); err != nil {
		return "", fmt.Errorf("create notes directory: %w", err)
	}
	if _, err := listNotes(r); err != nil { // refuses a link in its place
		return "", err
	}

	rel := Dir + "/" + name + ".md"
	if in.Replace {
		if fi, err := r.Lstat(rel); err == nil && !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file; refusing to overwrite it", rel)
		}
		// Write beside it and rename over it: a reader never sees half a note,
		// and a rename replaces what is at the path instead of following it.
		var rnd [6]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", err
		}
		tmp := fmt.Sprintf("%s/.%s.%x.tmp", Dir, name, rnd)
		f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, notePerm)
		if err != nil {
			return "", fmt.Errorf("create %s: %w", tmp, err)
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = r.Rename(tmp, rel)
		}
		if werr != nil {
			_ = r.Remove(tmp) // best effort: a leftover .tmp is ignored by Sync anyway
			return "", fmt.Errorf("write %s: %w", rel, werr)
		}
	} else {
		f, err := r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, notePerm)
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("a note named %q already exists; choose another name or replace it", name)
		}
		if err != nil {
			return "", fmt.Errorf("create %s: %w", rel, err)
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", fmt.Errorf("write %s: %w", rel, werr)
		}
	}

	if _, err := Sync(db, root); err != nil {
		return "", err
	}
	return name, nil
}

// Delete removes a note's file and its index entry.
func Delete(db *index.Store, root, name string) error {
	name, err := NormalizeName(name)
	if err != nil {
		return err
	}
	r, err := safefs.OpenExisting(stateDir(root))
	if err != nil {
		return err
	}
	defer r.Close()
	rel := Dir + "/" + name + ".md"
	fi, err := r.Lstat(rel)
	if err != nil {
		return fmt.Errorf("no note named %q", name)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing to delete it", rel)
	}
	if err := r.Remove(rel); err != nil {
		return err
	}
	return db.DeleteNote(name)
}

// View is a note with what links to it.
type View struct {
	Note       index.Note
	LinkedFrom []string // names of notes whose body links here
}

// Get returns one note, after syncing the index with the directory.
func Get(db *index.Store, root, name string) (View, bool, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return View{}, false, err
	}
	if _, err := Sync(db, root); err != nil {
		return View{}, false, err
	}
	n, ok, err := db.GetNote(name)
	if err != nil || !ok {
		return View{}, false, err
	}
	if _, err := Recheck(n); err != nil {
		// The row does not meet the rules the file did when it was indexed, so
		// it was altered. Drop it and read the file again: Sync indexes a note
		// only from the file, never from a row it finds.
		if err := db.DeleteNote(name); err != nil {
			return View{}, false, err
		}
		if _, err := Sync(db, root); err != nil {
			return View{}, false, err
		}
		if n, ok, err = db.GetNote(name); err != nil || !ok {
			return View{}, false, err
		}
	}
	from, err := db.NotesLinkingTo(name)
	if err != nil {
		return View{}, false, err
	}
	return View{Note: n, LinkedFrom: from}, true, nil
}

// SearchOptions narrows a search; with none set it lists, newest first.
type SearchOptions struct {
	Query string // words, matched as prefixes, all required
	File  string // notes related to this repo-relative file
	Tag   string
	Limit int
}

// Search returns matching notes, after syncing the index with the directory.
func Search(db *index.Store, root string, o SearchOptions) ([]index.NoteHit, error) {
	q := index.NoteQuery{File: o.File, Tag: strings.ToLower(strings.TrimSpace(o.Tag)), Limit: o.Limit}
	if q.Limit < 1 || q.Limit > maxSearchLimit {
		if o.Limit != 0 {
			return nil, fmt.Errorf("limit must be between 1 and %d, got %d", maxSearchLimit, o.Limit)
		}
		q.Limit = 20
	}
	if q.File != "" {
		if err := recorder.ValidateFilePath(q.File); err != nil {
			return nil, err
		}
	}
	if q.Tag != "" && !tagRe.MatchString(q.Tag) {
		return nil, fmt.Errorf("invalid tag %q", o.Tag)
	}
	var err error
	if q.Text, err = ftsTerms(o.Query); err != nil {
		return nil, err
	}
	if _, err := Sync(db, root); err != nil {
		return nil, err
	}
	return db.SearchNotes(q)
}

// ftsTerms turns what a person or agent typed into FTS5 terms. The words are
// quoted, so an operator, a column filter or stray punctuation in the query is
// just a word and cannot be a syntax error or reach other columns; each is a
// prefix, and all are required.
func ftsTerms(query string) (string, error) {
	if len(query) > MaxQueryLen {
		return "", fmt.Errorf("query too long: %d bytes (max %d)", len(query), MaxQueryLen)
	}
	words := strings.Fields(textsafe.OneLine(query))
	if len(words) > maxQueryTokens {
		return "", fmt.Errorf("too many words in the query: %d (max %d)", len(words), maxQueryTokens)
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) > maxTokenLen {
			return "", fmt.Errorf("a query word is too long (max %d bytes)", maxTokenLen)
		}
		term := `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
		if utf8.RuneCountInString(w) >= minPrefixLen {
			term += "*"
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, " "), nil
}
