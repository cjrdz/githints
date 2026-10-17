package index

import (
	"path/filepath"
	"reflect"
	"testing"
)

func openNotesStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".githints", "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sampleNote(name, body string) Note {
	return Note{
		Name: name, Title: "Title of " + name, Body: body, Tags: []string{"auth", "perf"},
		Files: []string{"a/b.go"}, Changes: []int64{3, 4}, Links: []string{"other", "a/b.go"}, MTime: 10, Size: 20,
	}
}

func TestNoteRoundTrip(t *testing.T) {
	s, _ := openNotesStore(t)
	want := sampleNote("plan", "rotate the tokens")
	if err := s.UpsertNote(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetNote("plan")
	if err != nil || !ok {
		t.Fatalf("GetNote = %v, %v", ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if _, ok, _ := s.GetNote("missing"); ok {
		t.Error("found a note that does not exist")
	}
}

func TestUpsertReplacesEverythingAboutANote(t *testing.T) {
	s, _ := openNotesStore(t)
	if err := s.UpsertNote(sampleNote("plan", "uses caching")); err != nil {
		t.Fatal(err)
	}
	next := Note{Name: "plan", Title: "New", Body: "uses queues", Tags: []string{"x"}, MTime: 11, Size: 5}
	if err := s.UpsertNote(next); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.SearchNotes(NoteQuery{Text: `"caching"*`}); len(hits) != 0 {
		t.Errorf("old text still searchable: %+v", hits)
	}
	if hits, _ := s.SearchNotes(NoteQuery{Tag: "auth"}); len(hits) != 0 {
		t.Errorf("old tag still attached: %+v", hits)
	}
	if hits, _ := s.SearchNotes(NoteQuery{File: "a/b.go"}); len(hits) != 0 {
		t.Errorf("old file ref still attached: %+v", hits)
	}
	if hits, _ := s.SearchNotes(NoteQuery{Text: `"queues"*`}); len(hits) != 1 {
		t.Errorf("new text not searchable: %+v", hits)
	}
	if n, _ := s.NoteCount(); n != 1 {
		t.Errorf("%d notes, want 1", n)
	}
}

func TestDeleteNoteRemovesItEverywhere(t *testing.T) {
	s, _ := openNotesStore(t)
	for _, n := range []string{"a", "b"} {
		if err := s.UpsertNote(sampleNote(n, "shared words")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteNote("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote("a"); err != nil {
		t.Errorf("deleting a missing note: %v", err)
	}
	hits, _ := s.SearchNotes(NoteQuery{Text: `"shared"*`})
	if len(hits) != 1 || hits[0].Name != "b" {
		t.Errorf("hits = %+v, want only b", hits)
	}
	for table, want := range map[string]int{"notes": 1, "note_tags": 2, "note_refs": 5, "notes_fts": 1} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Errorf("%s has %d rows (%v), want %d: a deleted note left rows behind", table, n, err, want)
		}
	}
}

func TestSearchNotesNarrowing(t *testing.T) {
	s, _ := openNotesStore(t)
	a := sampleNote("a", "alpha text")
	b := sampleNote("b", "beta text")
	b.Tags, b.Files, b.MTime = []string{"other"}, nil, 99
	b.Links = []string{"a/b.go"} // a wikilink to the file relates the note too
	for _, n := range []Note{a, b} {
		if err := s.UpsertNote(n); err != nil {
			t.Fatal(err)
		}
	}
	names := func(q NoteQuery) []string {
		hits, err := s.SearchNotes(q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Name)
		}
		return out
	}
	if got := names(NoteQuery{}); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("list order = %v, want newest first", got)
	}
	if got := names(NoteQuery{Tag: "other"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("tag = %v", got)
	}
	if got := names(NoteQuery{File: "a/b.go"}); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("file (front matter or wikilink) = %v", got)
	}
	if got := names(NoteQuery{Text: `"text"*`, Tag: "auth"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("text and tag = %v", got)
	}
	if got := names(NoteQuery{Limit: 1}); len(got) != 1 {
		t.Errorf("limit ignored: %v", got)
	}
	hits, _ := s.SearchNotes(NoteQuery{Text: `"alpha"*`})
	if len(hits) != 1 || hits[0].Snippet == "" || len(hits[0].Tags) != 2 {
		t.Errorf("hit = %+v, want a snippet and tags", hits)
	}
}

// The notes tables are not part of the structural index: resetting or clearing
// that must never cost a note.
func TestNotesSurviveIndexResetAndClear(t *testing.T) {
	s, path := openNotesStore(t)
	if err := s.UpsertNote(sampleNote("keep", "durable")); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.NoteCount(); n != 1 {
		t.Errorf("Clear dropped the notes: %d left", n)
	}

	// A schema version mismatch drops and recreates the structural tables.
	if _, err := s.db.Exec(`UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if hits, _ := s2.SearchNotes(NoteQuery{Text: `"durable"*`}); len(hits) != 1 {
		t.Errorf("a schema reset dropped the notes: %+v", hits)
	}
}

func TestNotesLinkingTo(t *testing.T) {
	s, _ := openNotesStore(t)
	target := Note{Name: "target", Title: "T", Body: "x", MTime: 1}
	l1 := Note{Name: "one", Title: "1", Body: "x", Links: []string{"target"}, MTime: 2}
	l2 := Note{Name: "two", Title: "2", Body: "x", Links: []string{"target.md", "target"}, MTime: 3}
	self := Note{Name: "self", Title: "s", Body: "x", Links: []string{"self"}, MTime: 4}
	for _, n := range []Note{target, l1, l2, self} {
		if err := s.UpsertNote(n); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NotesLinkingTo("target")
	if err != nil || !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Errorf("linking to target = %v, %v", got, err)
	}
	if got, _ := s.NotesLinkingTo("self"); len(got) != 0 {
		t.Errorf("a note counts as linking to itself: %v", got)
	}
}
