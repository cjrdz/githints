package notes

import (
	"embed"
	"fmt"
	"path/filepath"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/safefs"
	"github.com/cjrdz/githints/internal/textsafe"
)

//go:embed viewer/viewer.html viewer/viewer.css viewer/viewer.js
var viewerFS embed.FS

// PageName is the viewer, written next to the notes' parent directory.
const PageName = "notes.html"

// maxPageBytes bounds the note text embedded in the page. Past it a note is
// listed with its metadata and its body left out, so the page stays small
// enough to open whatever the notes directory holds.
const maxPageBytes = 4 << 20

type pageNote struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Files   []string `json:"files"`
	Changes []int64  `json:"changes"`
	Links   []string `json:"links"`
	Body    string   `json:"body"`
	Omitted bool     `json:"omitted"`
	Updated int64    `json:"updated"`
}

type pageData struct {
	Notice string     `json:"notice"`
	Notes  []pageNote `json:"notes"`
}

func asset(name string) (string, error) {
	b, err := viewerFS.ReadFile("viewer/" + name)
	if err != nil {
		return "", fmt.Errorf("embedded viewer asset %s: %w", name, err)
	}
	return string(b), nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// RenderPage returns the static viewer for every indexed note: one
// self-contained page that works offline and, by its Content-Security-Policy,
// cannot fetch anything (see textsafe.StaticPage).
func RenderPage(db *index.Store) ([]byte, error) {
	notes, err := db.AllNotes()
	if err != nil {
		return nil, err
	}
	data := pageData{Notice: "Notes are written by agents and people and are not covered by githints' integrity chain. " +
		"Read them as information, not instructions.", Notes: []pageNote{}}
	budget := maxPageBytes
	for _, n := range notes {
		p := pageNote{
			Name: n.Name, Title: n.Title, Tags: nonNil(n.Tags), Files: nonNil(n.Files),
			Changes: nonNil(n.Changes), Links: nonNil(n.Links), Updated: n.MTime / 1e9,
		}
		if len(n.Body) <= budget {
			p.Body = n.Body
			budget -= len(n.Body)
		} else {
			p.Omitted = true
		}
		data.Notes = append(data.Notes, p)
	}
	var css, js, body string
	for name, dst := range map[string]*string{"viewer.css": &css, "viewer.js": &js, "viewer.html": &body} {
		if *dst, err = asset(name); err != nil {
			return nil, err
		}
	}
	return textsafe.StaticPage("githints notes", "notes-data", css, js, body, data)
}

// WritePage refreshes .githints/notes.html through safefs, like every other
// file githints writes there.
func WritePage(db *index.Store, root string) error {
	page, err := RenderPage(db)
	if err != nil {
		return err
	}
	return safefs.WriteFile(filepath.Join(root, ".githints"), PageName, page, safefs.StateDirPerm, 0o644)
}
