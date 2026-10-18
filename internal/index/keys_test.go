package index

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

// keyRepo is a repository of two Go modules, a Python project and a C++
// tree, so keys come from go.mod, pyproject.toml and plain paths.
func keyRepo(t *testing.T) (*Store, string, lang.ScanOptions) {
	t.Helper()
	st, dir := tempStore(t)
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	for _, d := range []string{"svc/api", "lib/util", "py/src/app", "cpp/include/kit", "cpp/src"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"svc/go.mod":                 "module example.com/svc\n\ngo 1.23\n",
		"svc/api/api.go":             "package api\n\nimport \"example.com/svc/store\"\n\nfunc Serve() { store.Open() }\n",
		"lib/go.mod":                 "module example.com/lib\n\ngo 1.23\n",
		"lib/util/util.go":           "package util\n\nfunc Clamp() {}\n",
		"py/pyproject.toml":          "[project]\nname = \"app\"\n",
		"py/src/app/__init__.py":     "",
		"py/src/app/main.py":         "from app import models\n\ndef run():\n    pass\n",
		"py/src/app/models.py":       "class User:\n    pass\n",
		"cpp/include/kit/kit.h":      "#pragma once\nint kit_init(void);\n",
		"cpp/src/kit.cpp":            "#include <kit/kit.h>\nint kit_init(void) { return 0; }\n",
		"cpp/src/main.cpp":           "#include <kit/kit.h>\nint main() { return kit_init(); }\n",
		"svc/store/store.go":         "package store\n\nfunc Open() {}\n",
		"svc/store/store_helpers.go": "package store\n\nfunc helper() {}\n",
	}
	for name, src := range files {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGo(t, root, name, src)
	}
	opts := lang.ScanOptions{Root: root, Languages: []string{"auto"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	return st, root, opts
}

// assertStoredKeysMatch is the differential check: every indexed file's
// stored key, and the file stored for every key, agree with a fresh
// computation over the whole index, which is what the hook used to do.
func assertStoredKeysMatch(t *testing.T, st *Store, root, when string) {
	t.Helper()
	if !st.keysComplete() {
		t.Fatalf("%s: stored keys not marked complete", when)
	}
	want := resolveImportPaths(st, root, lang.NewRegistryForRoot(root))
	stored, err := st.storedKeyMap()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, want.ofFile) {
		t.Errorf("%s: stored keys\n got %v\nwant %v", when, stored, want.ofFile)
	}
	lookup := storedKeys{st}
	for key, file := range want.toFile {
		if got, ok := lookup.fileFor(key); !ok || got != file {
			t.Errorf("%s: fileFor(%q) = %q, %v; want %q", when, key, got, ok, file)
		}
	}
	for file, key := range want.ofFile {
		if got, ok := lookup.keyOf(file); !ok || got != key {
			t.Errorf("%s: keyOf(%q) = %q, %v; want %q", when, file, got, ok, key)
		}
	}
}

// TestStoredKeysTrackEveryChange: after a full scan and after each kind of
// commit the hook sees (an edit, a new file, a deletion, a rename, a change
// to a file keys derive from), the stored keys equal a full recomputation,
// and only the go.mod change recomputes them all.
func TestStoredKeysTrackEveryChange(t *testing.T) {
	st, root, opts := keyRepo(t)
	assertStoredKeysMatch(t, st, root, "full scan")

	step := func(name string, paths []string, wantRecompute bool) {
		t.Helper()
		before := keyRecomputes.Load()
		if err := IncrementalScan(st, opts, paths, 0); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := keyRecomputes.Load() - before; (got > 0) != wantRecompute {
			t.Errorf("%s: %d full key recomputations, want recompute=%v", name, got, wantRecompute)
		}
		assertStoredKeysMatch(t, st, root, name)
		report, err := VerifyIndex(st, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.StaleKeys) > 0 {
			t.Errorf("%s: verify reports stale keys %v", name, report.StaleKeys)
		}
	}

	writeGo(t, root, "svc/api/api.go", "package api\n\nfunc Serve() {}\nfunc Stop() {}\n")
	step("edit", []string{"svc/api/api.go"}, false)

	writeGo(t, root, "py/src/app/views.py", "from app.models import User\n\ndef show():\n    pass\n")
	step("add", []string{"py/src/app/views.py"}, false)

	if err := os.Remove(filepath.Join(root, "svc/store/store_helpers.go")); err != nil {
		t.Fatal(err)
	}
	step("delete", []string{"svc/store/store_helpers.go"}, false)

	if err := os.Rename(filepath.Join(root, "cpp/src/kit.cpp"), filepath.Join(root, "cpp/src/kit_impl.cpp")); err != nil {
		t.Fatal(err)
	}
	step("rename", []string{"cpp/src/kit.cpp", "cpp/src/kit_impl.cpp"}, false)

	// A module rename changes the key of every file in it, including those
	// the commit did not touch.
	writeGo(t, root, "lib/go.mod", "module example.com/library\n\ngo 1.23\n")
	step("go.mod", []string{"lib/go.mod"}, true)
	if key, _ := (storedKeys{st}).keyOf("lib/util/util.go"); key != "example.com/library/util" {
		t.Errorf("after the module rename lib/util/util.go is keyed %q", key)
	}
}

// TestHookRecomputesKeysAnIndexNeverStored: an index from a version without
// file_keys has no complete set, so the first hook run computes and stores
// them all, and the next looks them up.
func TestHookRecomputesKeysAnIndexNeverStored(t *testing.T) {
	st, root, opts := keyRepo(t)
	if _, err := st.db.Exec("DELETE FROM file_keys"); err != nil {
		t.Fatal(err)
	}
	if err := st.metaSet(metaKeysComplete, ""); err != nil {
		t.Fatal(err)
	}
	writeGo(t, root, "lib/util/util.go", "package util\n\nfunc Clamp() {}\nfunc Min() {}\n")
	before := keyRecomputes.Load()
	if err := IncrementalScan(st, opts, []string{"lib/util/util.go"}, 0); err != nil {
		t.Fatal(err)
	}
	if keyRecomputes.Load()-before != 1 {
		t.Error("an index without stored keys did not recompute them")
	}
	assertStoredKeysMatch(t, st, root, "first hook run")

	before = keyRecomputes.Load()
	if err := IncrementalScan(st, opts, []string{"lib/util/util.go"}, 0); err != nil {
		t.Fatal(err)
	}
	if keyRecomputes.Load() != before {
		t.Error("the second hook run recomputed keys it had stored")
	}
}

// TestVerifyReportsStaleKeys: a stored key the hook got wrong is drift.
func TestVerifyReportsStaleKeys(t *testing.T) {
	st, _, opts := keyRepo(t)
	if _, err := st.db.Exec("UPDATE file_keys SET import_key = 'wrong' WHERE file_path = 'lib/util/util.go'"); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyIndex(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.StaleKeys, []string{"lib/util/util.go"}) || !report.Drift() {
		t.Errorf("stale keys = %v, drift = %v", report.StaleKeys, report.Drift())
	}
}

// TestRebuildIndexesMatchSchema: the indexes a rebuild drops and recreates
// are exactly the ones the schema declares on the tables it replaces, and
// they exist after a rebuild. One missing from the list would be left
// maintained row by row; one missing from the schema would be created by a
// rebuild and absent from a fresh database.
func TestRebuildIndexesMatchSchema(t *testing.T) {
	st, _, _ := keyRepo(t)
	var want []string
	for _, def := range rebuildIndexes {
		name, _, _ := strings.Cut(def, " ")
		want = append(want, name)
	}
	sort.Strings(want)

	fresh, _ := tempStore(t)
	defer fresh.Close()
	for label, s := range map[string]*Store{"fresh database": fresh, "after a rebuild": st} {
		rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL AND tbl_name IN ('symbols', 'imports', 'facets', 'file_keys') ORDER BY name")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			got = append(got, n)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: indexes %v, rebuildIndexes %v", label, got, want)
		}
	}
}

// TestRebuildMatchesRowByRowWrites: the batched rebuild stores exactly what
// inserting one row at a time does, across a batch boundary.
func TestRebuildMatchesRowByRowWrites(t *testing.T) {
	var symbols []lang.Symbol
	var imports []lang.Import
	var facets []lang.Detected
	for i := 0; i < 2*rowsPerInsert+7; i++ {
		f := "src/f" + strings.Repeat("x", i%5) + ".go"
		symbols = append(symbols, lang.Symbol{Name: "S" + strings.Repeat("y", i%7), Kind: lang.KindFunc, FilePath: f, LineStart: i, LineEnd: i + 1, Signature: "(a int)", Language: "go"})
		imports = append(imports, lang.Import{FilePath: f, ImportedPath: "p/" + strings.Repeat("z", i%3)})
		facets = append(facets, lang.Detected{FilePath: f, Facet: "route", Framework: "chi", Name: "/" + strings.Repeat("r", i%4), Detail: "GET", Line: i})
	}
	batched, _ := tempStore(t)
	defer batched.Close()
	if err := batched.rebuild(rebuildRows{symbols: symbols, imports: imports, facets: facets,
		keys: func() map[string]string { return map[string]string{"src/f.go": "p"} }}); err != nil {
		t.Fatal(err)
	}
	single, _ := tempStore(t)
	defer single.Close()
	if err := single.InsertSymbols(symbols); err != nil {
		t.Fatal(err)
	}
	if err := single.InsertImports(imports); err != nil {
		t.Fatal(err)
	}
	if err := single.InsertFacets(facets); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT name, kind, file_path, line_start, line_end, signature, language FROM symbols ORDER BY id",
		"SELECT file_path, imported_path FROM imports ORDER BY id",
		"SELECT file_path, facet, framework, name, detail, line FROM facets ORDER BY id",
	} {
		if a, b := dumpRows(t, batched, q), dumpRows(t, single, q); !reflect.DeepEqual(a, b) {
			t.Errorf("%s differs:\n batched %d rows\n single %d rows", q, len(a), len(b))
		}
	}
}

func dumpRows(t *testing.T, s *Store, q string) []string {
	t.Helper()
	rows, err := s.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(vals...))
	}
	return out
}
