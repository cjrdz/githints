package index

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Notes are markdown files under .githints/notes/. The files are the source of
// truth; these tables are a search index over them and can be rebuilt from the
// directory at any time, like the rest of index.db.
//
// They are created with IF NOT EXISTS and deliberately sit outside
// schemaVersion: a structural-index reset (dropDataSchema) must not throw
// notes away, and Clear leaves them alone. Reading and writing the files, and
// everything about what a note may contain, is internal/notes; this file only
// stores what it is given.
const notesSchema = `
CREATE TABLE IF NOT EXISTS notes (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	name   TEXT NOT NULL UNIQUE,
	title  TEXT NOT NULL,
	body   TEXT NOT NULL,
	mtime  INTEGER NOT NULL,
	size   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS note_tags (
	note_id INTEGER NOT NULL,
	tag     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_note_tags ON note_tags(tag);
CREATE TABLE IF NOT EXISTS note_refs (
	note_id INTEGER NOT NULL,
	kind    TEXT NOT NULL,
	target  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_note_refs ON note_refs(kind, target);
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(title, tags, body);
`

func applyNotesSchema(db *sql.DB) error {
	if _, err := db.Exec(notesSchema); err != nil {
		return fmt.Errorf("apply notes schema: %w", err)
	}
	return nil
}

// Reference kinds stored for a note.
const (
	RefFile   = "file"   // front matter related_files
	RefChange = "change" // front matter related_changes (decimal id)
	RefLink   = "link"   // a [[wikilink]] in the body, as written
)

// Note is one indexed note.
type Note struct {
	Name    string // file name without .md
	Title   string
	Body    string
	Tags    []string
	Files   []string // related_files
	Changes []int64  // related_changes
	Links   []string // [[wikilink]] targets, as written
	MTime   int64    // of the file when it was indexed
	Size    int64
}

// noteSearchTimeout is the most a single search may take.
const noteSearchTimeout = 5 * time.Second

// NoteState is what the index remembers about a file, so an unchanged one is
// not read again.
type NoteState struct {
	MTime int64
	Size  int64
}

// NoteHit is one search or list result.
type NoteHit struct {
	Name    string
	Title   string
	Tags    []string
	Snippet string // from the body; empty for a list
	MTime   int64
}

// NoteQuery selects notes. Every set field narrows the result.
type NoteQuery struct {
	Text  string // full text; already reduced to safe FTS5 terms by the caller
	File  string // notes related to this file, by front matter or a wikilink
	Tag   string
	Limit int
}

// NoteStates returns the indexed state of every note, by name.
func (s *Store) NoteStates() (map[string]NoteState, error) {
	rows, err := s.db.Query(`SELECT name, mtime, size FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("note states: %w", err)
	}
	defer rows.Close()
	out := map[string]NoteState{}
	for rows.Next() {
		var name string
		var st NoteState
		if err := rows.Scan(&name, &st.MTime, &st.Size); err != nil {
			return nil, fmt.Errorf("scan note state: %w", err)
		}
		out[name] = st
	}
	return out, rows.Err()
}

// UpsertNote stores n, replacing any note of the same name.
func (s *Store) UpsertNote(n Note) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := deleteNoteTx(tx, n.Name); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO notes (name, title, body, mtime, size) VALUES (?, ?, ?, ?, ?)`,
			n.Name, n.Title, n.Body, n.MTime, n.Size)
		if err != nil {
			return fmt.Errorf("insert note: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, t := range n.Tags {
			if _, err := tx.Exec(`INSERT INTO note_tags (note_id, tag) VALUES (?, ?)`, id, t); err != nil {
				return fmt.Errorf("insert note tag: %w", err)
			}
		}
		ref := func(kind, target string) error {
			_, err := tx.Exec(`INSERT INTO note_refs (note_id, kind, target) VALUES (?, ?, ?)`, id, kind, target)
			return err
		}
		for _, f := range n.Files {
			if err := ref(RefFile, f); err != nil {
				return fmt.Errorf("insert note ref: %w", err)
			}
		}
		for _, c := range n.Changes {
			if err := ref(RefChange, strconv.FormatInt(c, 10)); err != nil {
				return fmt.Errorf("insert note ref: %w", err)
			}
		}
		for _, l := range n.Links {
			if err := ref(RefLink, l); err != nil {
				return fmt.Errorf("insert note ref: %w", err)
			}
		}
		// The rowid ties the full-text row to the note.
		if _, err := tx.Exec(`INSERT INTO notes_fts (rowid, title, tags, body) VALUES (?, ?, ?, ?)`,
			id, n.Title, strings.Join(n.Tags, " "), n.Body); err != nil {
			return fmt.Errorf("index note text: %w", err)
		}
		return nil
	})
}

// DeleteNote removes a note from the index. A missing note is not an error.
func (s *Store) DeleteNote(name string) error {
	return s.withTx(func(tx *sql.Tx) error { return deleteNoteTx(tx, name) })
}

func deleteNoteTx(tx *sql.Tx, name string) error {
	var id int64
	err := tx.QueryRow(`SELECT id FROM notes WHERE name = ?`, name).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find note: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM notes_fts WHERE rowid = ?`,
		`DELETE FROM note_tags WHERE note_id = ?`,
		`DELETE FROM note_refs WHERE note_id = ?`,
		`DELETE FROM notes WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return fmt.Errorf("delete note: %w", err)
		}
	}
	return nil
}

// GetNote returns one note by name.
func (s *Store) GetNote(name string) (Note, bool, error) {
	var n Note
	var id int64
	err := s.db.QueryRow(`SELECT id, name, title, body, mtime, size FROM notes WHERE name = ?`, name).
		Scan(&id, &n.Name, &n.Title, &n.Body, &n.MTime, &n.Size)
	if err == sql.ErrNoRows {
		return Note{}, false, nil
	}
	if err != nil {
		return Note{}, false, fmt.Errorf("get note: %w", err)
	}
	if n.Tags, err = s.noteTags(id); err != nil {
		return Note{}, false, err
	}
	rows, err := s.db.Query(`SELECT kind, target FROM note_refs WHERE note_id = ? ORDER BY rowid`, id)
	if err != nil {
		return Note{}, false, fmt.Errorf("get note refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, target string
		if err := rows.Scan(&kind, &target); err != nil {
			return Note{}, false, fmt.Errorf("scan note ref: %w", err)
		}
		switch kind {
		case RefFile:
			n.Files = append(n.Files, target)
		case RefChange:
			if v, err := strconv.ParseInt(target, 10, 64); err == nil {
				n.Changes = append(n.Changes, v)
			}
		case RefLink:
			n.Links = append(n.Links, target)
		}
	}
	return n, true, rows.Err()
}

func (s *Store) noteTags(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT tag FROM note_tags WHERE note_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, fmt.Errorf("note tags: %w", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// SearchNotes returns notes matching q, best match first for a text query and
// newest first otherwise.
func (s *Store) SearchNotes(q NoteQuery) ([]NoteHit, error) {
	if q.Limit < 1 {
		q.Limit = 20
	}
	var (
		sel   = `SELECT n.id, n.name, n.title, n.mtime, ''`
		from  = ` FROM notes n`
		where []string
		args  []any
		order = ` ORDER BY n.mtime DESC, n.name ASC`
	)
	if q.Text != "" {
		sel = `SELECT n.id, n.name, n.title, n.mtime, snippet(notes_fts, 2, '[', ']', '...', 12)`
		from = ` FROM notes_fts JOIN notes n ON n.id = notes_fts.rowid`
		where = append(where, `notes_fts MATCH ?`)
		args = append(args, q.Text)
		order = ` ORDER BY notes_fts.rank, n.name ASC`
	}
	if q.Tag != "" {
		where = append(where, `EXISTS (SELECT 1 FROM note_tags t WHERE t.note_id = n.id AND t.tag = ?)`)
		args = append(args, q.Tag)
	}
	if q.File != "" {
		where = append(where, `EXISTS (SELECT 1 FROM note_refs r WHERE r.note_id = n.id AND r.kind IN ('file', 'link') AND r.target = ?)`)
		args = append(args, q.File)
	}
	query := sel + from
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += order + ` LIMIT ?`
	args = append(args, q.Limit)

	// A search over many large notes can be slow however it is phrased, and
	// the MCP server answers requests concurrently: bound the work.
	ctx, cancel := context.WithTimeout(context.Background(), noteSearchTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	type hitRow struct {
		id  int64
		hit NoteHit
	}
	var found []hitRow
	for rows.Next() {
		var h hitRow
		if err := rows.Scan(&h.id, &h.hit.Name, &h.hit.Title, &h.hit.MTime, &h.hit.Snippet); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan note hit: %w", err)
		}
		found = append(found, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]NoteHit, 0, len(found))
	for _, h := range found {
		if h.hit.Tags, err = s.noteTags(h.id); err != nil {
			return nil, err
		}
		out = append(out, h.hit)
	}
	return out, nil
}

// NotesLinkingTo returns the names of notes whose body links to the note called
// name, with or without the .md suffix.
func (s *Store) NotesLinkingTo(name string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT n.name FROM note_refs r JOIN notes n ON n.id = r.note_id
		 WHERE r.kind = 'link' AND r.target IN (?, ?) AND n.name != ? ORDER BY n.name`,
		name, name+".md", name)
	if err != nil {
		return nil, fmt.Errorf("notes linking to %s: %w", name, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AllNotes returns every indexed note, newest first.
func (s *Store) AllNotes() ([]Note, error) {
	rows, err := s.db.Query(`SELECT name FROM notes ORDER BY mtime DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	out := make([]Note, 0, len(names))
	for _, name := range names {
		n, ok, err := s.GetNote(name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// NoteCount returns how many notes are indexed.
func (s *Store) NoteCount() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count notes: %w", err)
	}
	return n, nil
}
