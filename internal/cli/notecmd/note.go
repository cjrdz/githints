// Package notecmd implements `githints note`: the shell twin of the note MCP
// tools. Notes are markdown files under .githints/notes/; see internal/notes.
package notecmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/notes"
	"github.com/cjrdz/githints/internal/textsafe"
)

const usage = `usage: githints note <command>

  list [-limit=N]                         every note, newest first
  search [-query=Q] [-file=F] [-tag=T] [-limit=N]
                                          find notes by words, related file or tag
  get NAME                                one note in full
  record -title=T [-body=TEXT|-] [-name=N] [-tags=a,b] [-files=a.go,b.go]
         [-changes=1,2] [-replace]        write a note (-body=- reads stdin)
  delete NAME                             remove a note
  render                                  rebuild .githints/notes.html`

// Run dispatches a note subcommand.
func Run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return list(rest)
	case "search":
		return search(rest)
	case "get":
		return get(rest)
	case "record":
		return record(rest)
	case "delete":
		return remove(rest)
	case "render":
		return render(rest)
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(os.Stderr, usage)
		return nil
	}
	return fmt.Errorf("unknown note command %q\n%s", sub, usage)
}

// open returns the repository root and the index, which holds the notes'
// search tables. Like every command but init, it refuses a repository
// githints was never set up in.
func open(fs *flag.FlagSet, args []string) (root string, db *index.Store, rest []string, err error) {
	if err := fs.Parse(args); err != nil {
		return "", nil, nil, err
	}
	root, err = core.InitializedRoot()
	if err != nil {
		return "", nil, nil, err
	}
	db, err = index.Open(lang.IndexDBPath(root))
	if err != nil {
		return "", nil, nil, fmt.Errorf("open notes index: %w", err)
	}
	return root, db, fs.Args(), nil
}

// reportProblems says, on stderr, which files in the notes directory were left
// out and why.
func reportProblems(res notes.SyncResult) {
	for _, p := range res.Problems {
		fmt.Fprintf(os.Stderr, "githints: notes/%s not indexed: %s\n",
			textsafe.OneLine(p.File), textsafe.OneLine(p.Reason))
	}
}

// refresh rebuilds the viewer page. The note itself is already safe on disk, so
// a failure here only warns.
func refresh(db *index.Store, root string) {
	if err := notes.WritePage(db, root); err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not refresh .githints/%s: %v\n", notes.PageName, err)
	}
}

func list(args []string) error {
	fs := flag.NewFlagSet("note list", flag.ExitOnError)
	limit := fs.Int("limit", 0, "max notes (1-100; default 20)")
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q", extra[0])
	}
	res, err := notes.Sync(db, root)
	if err != nil {
		return err
	}
	reportProblems(res)
	if res.Changed() {
		refresh(db, root) // a hand edit surfaced: the page is behind
	}
	hits, err := notes.Search(db, root, notes.SearchOptions{Limit: *limit})
	if err != nil {
		return err
	}
	fmt.Print(notes.FormatHits(hits, "no notes yet (githints note record -title=... -body=...)\n"))
	return nil
}

func search(args []string) error {
	fs := flag.NewFlagSet("note search", flag.ExitOnError)
	query := fs.String("query", "", "words to find, matched as prefixes, all required")
	file := fs.String("file", "", "notes related to this repo-relative file")
	tag := fs.String("tag", "", "notes with this tag")
	limit := fs.Int("limit", 0, "max notes (1-100; default 20)")
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q", extra[0])
	}
	hits, err := notes.Search(db, root, notes.SearchOptions{Query: *query, File: *file, Tag: *tag, Limit: *limit})
	if err != nil {
		return err
	}
	fmt.Print(notes.FormatHits(hits, "no matching notes\n"))
	return nil
}

func get(args []string) error {
	fs := flag.NewFlagSet("note get", flag.ExitOnError)
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) != 1 {
		return errors.New("usage: githints note get NAME")
	}
	v, ok, err := notes.Get(db, root, extra[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no note named %q", textsafe.OneLine(extra[0]))
	}
	fmt.Print(notes.FormatNote(v))
	return nil
}

func record(args []string) error {
	fs := flag.NewFlagSet("note record", flag.ExitOnError)
	title := fs.String("title", "", "title (required)")
	name := fs.String("name", "", "file name without .md; default: derived from the title")
	body := fs.String("body", "", "the note text; - reads standard input")
	tags := fs.String("tags", "", "comma-separated tags")
	files := fs.String("files", "", "comma-separated related repo-relative files")
	changes := fs.String("changes", "", "comma-separated related change ids")
	replace := fs.Bool("replace", false, "overwrite an existing note of that name")
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q", extra[0])
	}
	if *title == "" {
		return errors.New("-title is required")
	}

	text := *body
	if text == "-" {
		// One byte past the cap tells an oversized body from one that fits.
		b, err := io.ReadAll(io.LimitReader(os.Stdin, notes.MaxBodyBytes+1))
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		text = string(b)
	}
	ids, err := parseIDs(*changes)
	if err != nil {
		return err
	}
	got, err := notes.Record(db, root, notes.RecordInput{
		Name: *name, Title: *title, Body: text, Tags: split(*tags), Files: split(*files), Changes: ids, Replace: *replace,
	})
	if err != nil {
		return err
	}
	refresh(db, root)
	fmt.Printf("recorded note %s (.githints/notes/%s.md)\n", got, got)
	return nil
}

func remove(args []string) error {
	fs := flag.NewFlagSet("note delete", flag.ExitOnError)
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) != 1 {
		return errors.New("usage: githints note delete NAME")
	}
	if err := notes.Delete(db, root, extra[0]); err != nil {
		return err
	}
	refresh(db, root)
	fmt.Printf("deleted note %s\n", textsafe.OneLine(strings.TrimSuffix(extra[0], ".md")))
	return nil
}

func render(args []string) error {
	fs := flag.NewFlagSet("note render", flag.ExitOnError)
	root, db, extra, err := open(fs, args)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(extra) > 0 {
		return fmt.Errorf("unexpected argument %q", extra[0])
	}
	res, err := notes.Sync(db, root)
	if err != nil {
		return err
	}
	reportProblems(res)
	if err := notes.WritePage(db, root); err != nil {
		return err
	}
	fmt.Printf("wrote .githints/%s\n", notes.PageName)
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseIDs(s string) ([]int64, error) {
	var out []int64
	for _, p := range split(s) {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("-changes: %q is not a number", p)
		}
		out = append(out, n)
	}
	return out, nil
}
