// Package export writes a self-contained pack of the change log for people and
// notebooks: JSON Lines and CSV of every row, a full changelog, the per-file
// hints, the notes, a manifest with a hash of every file and the integrity
// status at the time, and optionally a script that loads it all into DuckDB.
//
// Everything is built from explicit queries on the store and the notes index,
// never by walking .githints/: that directory also holds the integrity salt, the
// repository id and the databases, and a copy of "whatever is in there" is how a
// salt ends up in an attachment. The pack contains no salt, no key and no path
// of this machine, and nothing in it can be verified without the salt, which is
// the point of the manifest recording the status instead.
//
// It is a snapshot for reading, not an audit record: the pack is not signed, so
// whoever holds a copy can change it. `githints verify` against the repository
// is the check.
package export

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/notes"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

// Bounds. A pack is built in one pass over the log, so these keep a runaway
// store from producing a pack nobody can open.
const (
	MaxRows        = 1_000_000
	maxHintHistory = 100_000
	dirPerm        = 0o700
	filePerm       = 0o600
)

// Options configures Run.
type Options struct {
	Dir     string // output directory: must not exist, or be an empty plain directory
	DuckDB  bool   // also write githints.duckdb.sql
	NoNotes bool   // leave the notes out
	Version string // githints version, for the manifest
	Now     time.Time
}

// Result summarizes a finished export.
type Result struct {
	Dir      string
	Manifest Manifest
}

// Manifest describes the pack. It is written last, so a directory without one
// is an export that did not finish.
type Manifest struct {
	Format     int            `json:"format"`
	Tool       string         `json:"tool"`
	Version    string         `json:"version"`
	ExportedAt string         `json:"exported_at"`
	Counts     map[string]int `json:"counts"`
	Verify     VerifyStatus   `json:"verify"`
	Log        LogSummary     `json:"log"`
	Files      []FileEntry    `json:"files"`
	Skipped    []string       `json:"skipped,omitempty"`
	Caveats    []string       `json:"caveats"`
}

// LogSummary identifies the log the pack was cut from. Root is the version 2
// Merkle root of the whole log up to LastID, the value a reader can compare with
// the newest githints-log-root in refs/notes/githints.
type LogSummary struct {
	LastID int64  `json:"last_id"`
	Root   string `json:"root,omitempty"`
}

// FileEntry is one file in the pack.
type FileEntry struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// VerifyStatus is what `githints verify` would have said at export time. The
// salt is needed to check the chain, so "unavailable" is a real answer.
type VerifyStatus struct {
	Chain           string `json:"chain"` // ok, failed or unavailable
	ChainProblems   int    `json:"chain_problems"`
	ClockTamperRows int    `json:"clock_tamper_rows"`
	Anchors         string `json:"anchors"` // ok, failed, none or unavailable
	AnchorProblems  int    `json:"anchor_problems"`
	Detail          string `json:"detail,omitempty"`
}

// OK reports whether nothing was found wrong. Unavailable is not wrong, but it
// is not OK either.
func (v VerifyStatus) OK() bool {
	return v.Chain == "ok" && v.ClockTamperRows == 0 && (v.Anchors == "ok" || v.Anchors == "none")
}

type pack struct {
	r       *os.Root
	entries []FileEntry
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// create writes one new file. O_EXCL means it can never replace anything, and
// the os.Root means a path cannot leave the pack directory.
func (p *pack) create(rel string, fn func(w io.Writer) error) error {
	if d := path.Dir(rel); d != "." {
		if err := p.r.MkdirAll(d, dirPerm); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	f, err := p.r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	h := sha256.New()
	cw := &countWriter{}
	bw := bufio.NewWriter(io.MultiWriter(f, h, cw))
	err = fn(bw)
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	p.entries = append(p.entries, FileEntry{Path: rel, Bytes: cw.n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// prepareDir creates dir, or accepts an existing empty plain directory, and
// refuses anything else: a pack is never written over or among other files.
func prepareDir(root, dir string) (*os.Root, string, error) {
	if dir == "" {
		return nil, "", errors.New("an output directory is required (-o DIR)")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	state, err := filepath.Abs(filepath.Join(root, ".githints"))
	if err != nil {
		return nil, "", err
	}
	// Compare real locations: `-o link/pack`, where link points at .githints,
	// is inside it however the path is spelled. Case is ignored because the
	// filesystems that matter most here are case-insensitive, and a refusal
	// that is too eager costs one rename.
	if inside(resolve(abs), resolve(state)) || underSameFile(abs, state) {
		return nil, "", errors.New("the output directory cannot be inside .githints/: that directory is githints' own state")
	}

	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(abs, dirPerm); err != nil {
			return nil, "", err
		}
	case err != nil:
		return nil, "", err
	case fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !fi.IsDir():
		return nil, "", fmt.Errorf("%s exists and is not a plain directory; refusing to write into it", dir)
	default:
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, "", err
		}
		if len(entries) > 0 {
			return nil, "", fmt.Errorf("%s is not empty; export into a new or empty directory", dir)
		}
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, "", err
	}
	return r, abs, nil
}

// resolve returns p with every symlink on its existing part resolved, and the
// rest (the part that does not exist yet) appended as written.
func resolve(p string) string {
	rest := ""
	cur := p
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// underSameFile reports whether dir, or any directory above it that exists, is
// the same directory as state, by file identity rather than by path. Comparing
// paths misses a Windows junction: since Go 1.23 filepath.EvalSymlinks no longer
// resolves mount points, so `-o junction\pack`, where the junction points at
// .githints, looked like an ordinary path. os.Stat follows a symlink or junction
// to what it names and os.SameFile compares the volume and file index.
func underSameFile(dir, state string) bool {
	want, err := os.Stat(state)
	if err != nil {
		return false
	}
	for cur := dir; ; {
		if fi, err := os.Stat(cur); err == nil && os.SameFile(fi, want) {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// inside reports whether p is dir or below it.
func inside(p, dir string) bool {
	p, dir = strings.ToLower(p), strings.ToLower(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// Run writes the pack. db holds the notes' search tables and may be nil, in
// which case the notes are left out.
func Run(root string, st *store.Store, db *index.Store, opt Options) (Result, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	rows, err := st.AllChanges()
	if err != nil {
		return Result{}, fmt.Errorf("load changes: %w", err)
	}
	if len(rows) > MaxRows {
		return Result{}, fmt.Errorf("the log has %d rows; an export is limited to %d", len(rows), MaxRows)
	}
	files, err := st.FilesWithHistory()
	if err != nil {
		return Result{}, fmt.Errorf("list files: %w", err)
	}

	r, abs, err := prepareDir(root, opt.Dir)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	p := &pack{r: r}
	m := Manifest{
		Format: 1, Tool: "githints", Version: opt.Version,
		ExportedAt: opt.Now.UTC().Format(time.RFC3339),
		Counts:     map[string]int{},
		Caveats: []string{
			"A snapshot for reading. The pack is not signed; run `githints verify` in the repository to check the log.",
			"changes.csv prefixes cells that would run as a spreadsheet formula with a single quote; changes.jsonl is exact.",
			"Notes are not covered by the integrity chain.",
			"notes/ holds each note's markdown exactly as written. It is text from agents and people: open it as text, not in a markdown previewer, unless you trust who wrote it.",
		},
	}
	for _, c := range rows {
		if c.Kind == store.KindOutcome {
			m.Counts["outcomes"]++
		} else {
			m.Counts["changes"]++
		}
	}
	m.Counts["files_with_history"] = len(files)

	steps := []struct {
		rel string
		fn  func(io.Writer) error
	}{
		{"changes.jsonl", func(w io.Writer) error { return writeJSONL(w, rows) }},
		{"changes.csv", func(w io.Writer) error { return writeCSV(w, rows) }},
		{"CHANGES-full.md", func(w io.Writer) error { return writeChangelog(w, rows) }},
	}
	for _, s := range steps {
		if err := p.create(s.rel, s.fn); err != nil {
			return Result{}, err
		}
	}

	for _, f := range files {
		// Rows are validated when recorded, but the store is a file anyone who
		// can write it can edit: a path that is not repo-relative never becomes
		// a path under the pack, however it got there.
		if err := recorder.ValidateFilePath(f); err != nil {
			m.Skipped = append(m.Skipped, fmt.Sprintf("hints/%q: not a valid repo-relative path", f))
			continue
		}
		history, err := st.FileHistory(f, maxHintHistory)
		if err != nil {
			return Result{}, fmt.Errorf("load history for %s: %w", f, err)
		}
		rel := "hints/" + f + ".md"
		err = p.create(rel, func(w io.Writer) error {
			_, err := w.Write(hint.FileMarkdown(history, f))
			return err
		})
		if errors.Is(err, fs.ErrExist) {
			// Two paths that differ only in case, on a case-insensitive disk.
			m.Skipped = append(m.Skipped, rel+": a file of that name already exists in the pack")
			continue
		}
		if err != nil {
			return Result{}, err
		}
	}

	noteCount := 0
	if !opt.NoNotes && db != nil {
		if noteCount, err = writeNotes(p, db, root, &m); err != nil {
			return Result{}, err
		}
	}
	m.Counts["notes"] = noteCount

	if opt.DuckDB {
		if err := p.create("githints.duckdb.sql", func(w io.Writer) error { return writeDuckDB(w, noteCount > 0) }); err != nil {
			return Result{}, err
		}
	}

	m.Verify, m.Log = verifyStatus(root, rows)
	sort.Slice(p.entries, func(i, j int) bool { return p.entries[i].Path < p.entries[j].Path })
	m.Files = p.entries
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := p.r.WriteFile("manifest.json", append(manifest, '\n'), filePerm); err != nil {
		return Result{}, fmt.Errorf("write manifest.json: %w", err)
	}
	return Result{Dir: abs, Manifest: m}, nil
}

// writeNotes copies the notes the index accepted, in their canonical form. A
// file that failed a check (a credential, a link) is not in the index, so it is
// not in the pack either.
func writeNotes(p *pack, db *index.Store, root string, m *Manifest) (int, error) {
	if _, err := notes.Sync(db, root); err != nil {
		// Fixed text: an error message can carry a path of this machine.
		m.Skipped = append(m.Skipped, "notes: the notes directory could not be read (see `githints note list`)")
		return 0, nil
	}
	all, err := db.AllNotes()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, note := range all {
		// The index is a cache anyone who can write it can edit: a note is
		// exported only if it still meets every rule.
		doc, err := notes.Recheck(note)
		if err != nil {
			m.Skipped = append(m.Skipped, fmt.Sprintf("notes/%q: failed its checks", note.Name))
			continue
		}
		data := doc.Marshal()
		if err := p.create("notes/"+note.Name+".md", func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// verifyStatus runs the checks `githints verify` runs, without printing and
// without creating a salt. Whatever cannot be checked is reported as such.
func verifyStatus(root string, rows []store.Change) (VerifyStatus, LogSummary) {
	var v VerifyStatus
	var log LogSummary
	if len(rows) > 0 {
		log.LastID = rows[len(rows)-1].ID
		if a, err := integrity.BuildAnchor(rows, nil); err == nil {
			log.Root = a.LogRoot
		}
	}
	for _, c := range rows {
		if c.ClockTamperWarning {
			v.ClockTamperRows++
		}
	}

	key, _, err := integrity.ExistingKey(root)
	if err != nil {
		// Fixed text: an error message names the salt's path on this machine.
		v.Chain, v.Detail = "unavailable", "the integrity salt could not be read on the machine that made this pack"
		if errors.Is(err, integrity.ErrSaltMissing) {
			v.Detail = "the integrity salt is missing on the machine that made this pack"
		}
	} else if errs := integrity.VerifyChain(key, rows); len(errs) == 0 {
		v.Chain = "ok"
	} else {
		v.Chain, v.ChainProblems = "failed", len(errs)
	}

	noted, err := gitutil.ReadNotes("refs/notes/githints")
	switch {
	case err != nil:
		v.Anchors = "unavailable"
	default:
		report := integrity.VerifyAnchors(rows, noted)
		switch {
		case report.Notes == 0:
			v.Anchors = "none"
		case len(report.Problems) == 0:
			v.Anchors = "ok"
		default:
			v.Anchors, v.AnchorProblems = "failed", len(report.Problems)
		}
	}
	return v, log
}
