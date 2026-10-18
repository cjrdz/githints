package index

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/safefs"
	"github.com/cjrdz/githints/internal/textsafe"
)

// RenderNotes writes the per-file index notes and the root rollup from the
// current index state. It never writes to per-file hint markdown (.githints/<path>.md)
// or CHANGES.md, which are integrity-verified by the hint package.
func RenderNotes(db *Store, root string) error {
	// Built once here: a registry reload per file would re-read the repository's
	// own specs for every note. Repo-aware so a language the checkout supplies
	// takes part in the graph like any other.
	registry := lang.NewRegistryForRoot(root)

	// Let parsers install per-scan state for the render too. resolveImportPaths
	// asks every indexed file for its import path, and without this a
	// standalone `githints render` re-reads go.mod once per file. Inside a
	// scan the hook is already active and installing it again is harmless.
	defer lang.BeginScans(registry.AllParsers(), root)()
	files, err := db.AllIndexedFiles()
	if err != nil {
		return fmt.Errorf("list indexed files: %w", err)
	}

	// Import keys are resolved once for the whole pass, for the notes and
	// the rollup alike: resolving reads files for some languages (a C#
	// namespace, a Kotlin package).
	keys := resolveImportPathsFor(files, root, registry)

	// Four queries in all rather than four per file.
	src, err := loadBulkNotes(db)
	if err != nil {
		return err
	}
	w, err := safefs.NewWriter(githintsDir(root), safefs.StateDirPerm, 0o644)
	if err != nil {
		return fmt.Errorf("open .githints: %w", err)
	}
	defer w.Close()

	if err := renderFileNotes(w, src, files, root, keys); err != nil {
		return err
	}
	if err := renderIndexRollup(w, db, root, keys); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}
	return nil
}

// renderFileNotes writes the note of every file from src, removes the notes
// of files no longer indexed. It needs no database, so a full scan runs it while SQLite stores the same rows.
func renderFileNotes(w *safefs.Writer, src noteSource, files []string, root string, keys keyLookup) error {
	rendered := make(map[string]struct{}, len(files))
	for _, file := range files {
		if notePath, _, err := lang.IndexNotePath(root, file); err == nil {
			rendered[filepath.Clean(notePath)] = struct{}{}
		}
	}
	// Notes are independent: each reads the shared, read-only data and keys
	// and writes its own file, so they render across a pool. Most of the
	// work is I/O (the unchanged-note comparison, the write).
	if err := renderInParallel(files, func(file string) error {
		return renderFileNote(w, src, root, file, keys)
	}); err != nil {
		return err
	}
	pruneStaleNotes(root, rendered)
	return nil
}

// maxRenderWorkers caps the render pool. Rendering is I/O: on this
// repository 4 workers gave the same wall time as 12 (about 185 ms against
// 215 sequential) while using no more CPU than the sequential pass; 12 cost
// about 10% more CPU in scheduling for nothing.
const maxRenderWorkers = 4

// renderInParallel runs render for every file across a worker pool and
// returns the error of the first file, in file order, that failed: the same
// error a sequential pass would have stopped at, whichever finished first.
func renderInParallel(files []string, render func(string) error) error {
	errs := make([]error, len(files))
	inParallel(len(files), min(runtime.NumCPU(), maxRenderWorkers), func(i int) {
		errs[i] = render(files[i])
	})
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("render note for %s: %w", files[i], err)
		}
	}
	return nil
}

// githintsDir is the directory every index write is confined to.
func githintsDir(root string) string {
	return filepath.Join(root, ".githints")
}

// writeManagedWith writes an absolute path under .githints/ through a pass's
// shared safefs.Writer, so a link committed inside .githints/ cannot redirect
// the write.
func writeManagedWith(w *safefs.Writer, root, abs string, data []byte) error {
	rel, err := filepath.Rel(githintsDir(root), abs)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not under .githints", abs)
	}
	return w.WriteFile(rel, data)
}

// removeManaged deletes an absolute path under .githints/ through an os.Root.
// A missing file is not an error.
func removeManaged(root, abs string) error {
	rel, err := filepath.Rel(githintsDir(root), abs)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not under .githints", abs)
	}
	r, err := safefs.Open(githintsDir(root), safefs.StateDirPerm)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// pruneStaleNotes deletes note files under the index notes directory that
// were not produced by the current render — leftovers from files that were
// deleted, renamed, or dropped from the configured languages. Empty
// directories are removed afterwards, children before parents.
func pruneStaleNotes(root string, keep map[string]struct{}) {
	// Walked and deleted through an os.Root: a clone that ships
	// .githints -> .. (or .githints/index -> somewhere) must not turn the
	// prune into "delete every .md file over there".
	r, err := safefs.Open(githintsDir(root), safefs.StateDirPerm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: stale note prune skipped: %v\n", err)
		return
	}
	defer r.Close()
	if fi, err := r.Lstat("index"); err != nil || !fi.IsDir() {
		// Missing is the normal first-run case; anything other than a plain
		// directory (a link) is not ours to walk.
		return
	}
	// Directories a note was removed from: only those, and their parents,
	// can have become empty. Trying every directory was a failed remove per
	// directory on every scan, for nothing.
	emptied := map[string]bool{}
	err = fs.WalkDir(r.FS(), "index", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}
		abs := filepath.Join(githintsDir(root), filepath.FromSlash(rel))
		if _, ok := keep[filepath.Clean(abs)]; !ok {
			if err := r.Remove(filepath.FromSlash(rel)); err != nil {
				fmt.Fprintf(os.Stderr, "githints: could not remove stale index note %s: %v\n", abs, err)
			} else {
				for dir := path.Dir(rel); dir != "index" && dir != "."; dir = path.Dir(dir) {
					emptied[dir] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: stale note prune failed: %v\n", err)
		return
	}
	// Parents are always shorter than their children, so length-descending
	// order guarantees children are processed first. Remove fails on a
	// non-empty directory, which is exactly the check wanted here.
	dirs := make([]string, 0, len(emptied))
	for dir := range emptied {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = r.Remove(filepath.FromSlash(dir)) // non-empty directories are meant to stay
	}
}

func renderFileNote(w *safefs.Writer, notes noteSource, root, src string, keys keyLookup) error {
	notePath, collision, err := lang.IndexNotePath(root, src)
	if err != nil {
		if collision {
			// Log and skip; this is a serious structural conflict we cannot
			// resolve without clobbering an integrity-verified file.
			fmt.Fprintf(os.Stderr, "githints: index note collision for %s: %v\n", src, err)
			return nil
		}
		return err
	}

	symbols, err := notes.symbols(src)
	if err != nil {
		return fmt.Errorf("load symbols: %w", err)
	}

	// Dependents are stored by import path (Go module path, or the
	// normalized TS-family file key), not by file path, so resolve before
	// querying — the same lookup the get_dependents MCP tool does. When the
	// path cannot be resolved (e.g. a Go file outside a module) no importer
	// could reference it either, so the section is simply omitted.
	var imports []lang.Import
	if importPath, ok := keys.keyOf(src); ok {
		imports, err = notes.importers(importPath)
		if err != nil {
			return fmt.Errorf("load dependents: %w", err)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", textsafe.Markdown(src))

	if len(symbols) > 0 {
		b.WriteString("## Symbols\n\n")
		for _, sym := range symbols {
			fmt.Fprintf(&b, "- %s (%s) lines %d-%d", textsafe.CodeSpan(sym.Name), textsafe.Markdown(string(sym.Kind)), sym.LineStart, sym.LineEnd)
			if sym.Signature != "" {
				fmt.Fprintf(&b, " — %s", textsafe.CodeSpan(sym.Signature))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Framework constructs are not symbols -- a route is a call, not a
	// declaration -- so they get their own section rather than being folded in
	// above.
	facets, err := notes.facets(src)
	if err != nil {
		return fmt.Errorf("load facets: %w", err)
	}
	if len(facets) > 0 {
		b.WriteString("## Framework\n\n")
		for _, f := range facets {
			fmt.Fprintf(&b, "- %s %s (%s) line %d", textsafe.Markdown(f.Facet), textsafe.CodeSpan(f.Name), textsafe.Markdown(f.Framework), f.Line)
			if f.Detail != "" {
				fmt.Fprintf(&b, " — %s", textsafe.CodeSpan(f.Detail))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	outbound, err := notes.imports(src)
	if err != nil {
		return fmt.Errorf("load imports: %w", err)
	}
	if len(outbound) > 0 {
		b.WriteString("## Imports\n\n")
		for _, imp := range outbound {
			if file, ok := keys.fileFor(imp.ImportedPath); ok && file != src {
				// The import resolves to an indexed file: link to its note.
				fmt.Fprintf(&b, "- %s\n", lang.NoteLink(indexDirOf(src), imp.ImportedPath, file))
			} else {
				// Stdlib, external package, or unresolvable alias.
				fmt.Fprintf(&b, "- %s\n", textsafe.CodeSpan(imp.ImportedPath))
			}
		}
		b.WriteString("\n")
	}

	if len(imports) > 0 {
		b.WriteString("## Imported by\n\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "- %s\n", lang.NoteLink(indexDirOf(src), imp.FilePath, imp.FilePath))
		}
		b.WriteString("\n")
	}

	return writeManagedWith(w, root, notePath, []byte(b.String()))
}

func renderIndexRollup(w *safefs.Writer, db *Store, root string, keys keyLookup) error {
	meta, err := db.Meta()
	if err != nil {
		return fmt.Errorf("load meta: %w", err)
	}
	files, err := db.FileCount()
	if err != nil {
		return fmt.Errorf("file count: %w", err)
	}
	symbols, err := db.SymbolCount()
	if err != nil {
		return fmt.Errorf("symbol count: %w", err)
	}
	imports, err := db.ImportCount()
	if err != nil {
		return fmt.Errorf("import count: %w", err)
	}
	hubs, err := db.TopFilesByInDegree(10)
	if err != nil {
		return fmt.Errorf("hub ranking: %w", err)
	}

	var b strings.Builder
	b.WriteString("# githints structural index\n\n")
	fmt.Fprintf(&b, "- files indexed: %d\n", files)
	fmt.Fprintf(&b, "- symbols: %d\n", symbols)
	fmt.Fprintf(&b, "- imports recorded: %d\n", imports)
	fmt.Fprintf(&b, "- last indexed: %s\n", lang.FormatIndexedAt(meta.LastIndexedAt))
	if len(meta.LanguageCounts) > 0 {
		b.WriteString("- language breakdown:\n")
		for _, lang := range lang.SortedKeys(meta.LanguageCounts) {
			fmt.Fprintf(&b, "  - %s: %d\n", lang, meta.LanguageCounts[lang])
		}
	}
	if meta.SkippedCount > 0 {
		fmt.Fprintf(&b, "- skipped files: %d\n", meta.SkippedCount)
	}
	if meta.UnsupportedCount > 0 {
		fmt.Fprintf(&b, "- unsupported files: %d\n", meta.UnsupportedCount)
	}

	if len(hubs) > 0 {
		b.WriteString("\n## Most imported files (hubs)\n\n")
		for _, h := range hubs {
			if file, ok := keys.fileFor(h.File); ok {
				// Hub entries are import paths; link them to the file's note.
				fmt.Fprintf(&b, "- %d import(s): %s\n", h.Dependents, lang.NoteLink(".", h.File, file))
			} else {
				// Stdlib packages, external modules, and unresolvable path
				// aliases have no note to link to.
				fmt.Fprintf(&b, "- %d import(s): %s\n", h.Dependents, textsafe.CodeSpan(h.File))
			}
		}
	}

	return writeManagedWith(w, root, lang.IndexRollupPath(root), []byte(b.String()))
}

// indexDirOf returns the directory, relative to .githints/, containing the
// note for a repo-relative source file: "index" for root-level files,
// "index/pkg/lib" for pkg/lib/lib.go. It is the fromDir for links rendered
// in that note.
func indexDirOf(src string) string {
	return filepath.Join("index", filepath.Dir(src))
}

// resolveImportPaths maps the import path importers use (Go module path,
// normalized TS file key) back to the indexed file providing it, so hub
// entries — which are import paths, not files — can be linked to the file's
// note. Files whose import path cannot be resolved are skipped; on
// collisions the first file wins.
func resolveImportPaths(db *Store, root string, registry *lang.Registry) importKeys {
	keyRecomputes.Add(1)
	files, err := db.AllIndexedFiles()
	if err != nil {
		return importKeys{toFile: map[string]string{}, ofFile: map[string]string{}}
	}
	return resolveImportPathsFor(files, root, registry)
}

// keyRecomputes counts resolveImportPaths calls, so TestScanCostModel can
// check that the hook looks keys up instead of recomputing them all.
var keyRecomputes atomic.Int64

// resolveImportPathsFor is resolveImportPaths for a list of files already in
// hand, in AllIndexedFiles order.
func resolveImportPathsFor(files []string, root string, registry *lang.Registry) importKeys {
	keys := importKeys{toFile: map[string]string{}, ofFile: map[string]string{}}
	for _, f := range files {
		importPath, err := registry.ImportPath(root, f)
		if err != nil {
			continue
		}
		keys.ofFile[f] = importPath
		if _, taken := keys.toFile[importPath]; !taken {
			keys.toFile[importPath] = f
		}
	}
	return keys
}

// importKeys is resolveImportPaths' answer both ways: the file providing an
// import path, and the import path of each file.
type importKeys struct {
	toFile map[string]string
	ofFile map[string]string
}

// noteSource is what a note is rendered from. A full render loads the whole
// index in four queries (bulkNotes); the hook renders a handful of notes and
// queries per file (dbNotes), since loading every row for twenty notes would
// cost more than it saves.
type noteSource interface {
	symbols(file string) ([]lang.Symbol, error)
	facets(file string) ([]lang.Detected, error)
	imports(file string) ([]lang.Import, error)
	importers(importPath string) ([]lang.Import, error)
}

type dbNotes struct{ db *Store }

func (n dbNotes) symbols(f string) ([]lang.Symbol, error)   { return n.db.SymbolsForFile(f) }
func (n dbNotes) facets(f string) ([]lang.Detected, error)  { return n.db.FacetsForFile(f) }
func (n dbNotes) imports(f string) ([]lang.Import, error)   { return n.db.ImportsForFile(f) }
func (n dbNotes) importers(p string) ([]lang.Import, error) { return n.db.FilesImporting(p) }

// bulkNotes is the whole index grouped by file, in the order the per-file
// queries return rows, so both sources render byte-identical notes.
type bulkNotes struct {
	bySymbolFile map[string][]lang.Symbol
	byFacetFile  map[string][]lang.Detected
	byImportFile map[string][]lang.Import
	byImported   map[string][]lang.Import
}

func loadBulkNotes(db *Store) (bulkNotes, error) {
	n := bulkNotes{
		bySymbolFile: map[string][]lang.Symbol{},
		byFacetFile:  map[string][]lang.Detected{},
		byImportFile: map[string][]lang.Import{},
		byImported:   map[string][]lang.Import{},
	}
	symbols, err := db.AllSymbols()
	if err != nil {
		return n, fmt.Errorf("load symbols: %w", err)
	}
	for _, s := range symbols {
		n.bySymbolFile[s.FilePath] = append(n.bySymbolFile[s.FilePath], s)
	}
	facets, err := db.Facets(FacetFilter{})
	if err != nil {
		return n, fmt.Errorf("load facets: %w", err)
	}
	for _, f := range facets {
		n.byFacetFile[f.FilePath] = append(n.byFacetFile[f.FilePath], f)
	}
	// Ordered by file then import path: grouped by file this is
	// ImportsForFile's order, and grouped by import path FilesImporting's.
	imports, err := db.AllImports()
	if err != nil {
		return n, fmt.Errorf("load imports: %w", err)
	}
	for _, imp := range imports {
		n.byImportFile[imp.FilePath] = append(n.byImportFile[imp.FilePath], imp)
		n.byImported[imp.ImportedPath] = append(n.byImported[imp.ImportedPath], imp)
	}
	return n, nil
}

// memoryNotes groups a scan's rows the way loadBulkNotes' four queries return
// them -- symbols by line then name, facets by facet then line, a file's
// imports by import path, an import path's importers by file -- and lists the
// files AllIndexedFiles would. It reads the slices without changing them, so
// they can be inserted at the same time. TestBulkAndPerFileNotesAreIdentical
// pins the notes it renders against the database's.
func memoryNotes(symbols []lang.Symbol, imports []lang.Import, facets []lang.Detected) (bulkNotes, []string) {
	n := bulkNotes{
		bySymbolFile: map[string][]lang.Symbol{},
		byFacetFile:  map[string][]lang.Detected{},
		byImportFile: map[string][]lang.Import{},
		byImported:   map[string][]lang.Import{},
	}
	files := map[string]bool{}
	for _, s := range symbols {
		n.bySymbolFile[s.FilePath] = append(n.bySymbolFile[s.FilePath], s)
		files[s.FilePath] = true
	}
	for _, f := range facets {
		n.byFacetFile[f.FilePath] = append(n.byFacetFile[f.FilePath], f)
		files[f.FilePath] = true
	}
	for _, imp := range imports {
		n.byImportFile[imp.FilePath] = append(n.byImportFile[imp.FilePath], imp)
		n.byImported[imp.ImportedPath] = append(n.byImported[imp.ImportedPath], imp)
		files[imp.FilePath] = true
	}
	for _, list := range n.bySymbolFile {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].LineStart != list[j].LineStart {
				return list[i].LineStart < list[j].LineStart
			}
			return list[i].Name < list[j].Name
		})
	}
	for _, list := range n.byFacetFile {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Facet != list[j].Facet {
				return list[i].Facet < list[j].Facet
			}
			return list[i].Line < list[j].Line
		})
	}
	for _, list := range n.byImportFile {
		sort.SliceStable(list, func(i, j int) bool { return list[i].ImportedPath < list[j].ImportedPath })
	}
	for _, list := range n.byImported {
		sort.SliceStable(list, func(i, j int) bool { return list[i].FilePath < list[j].FilePath })
	}
	out := make([]string, 0, len(files))
	for f := range files {
		out = append(out, f)
	}
	sort.Strings(out)
	return n, out
}

func (n bulkNotes) symbols(f string) ([]lang.Symbol, error)   { return n.bySymbolFile[f], nil }
func (n bulkNotes) facets(f string) ([]lang.Detected, error)  { return n.byFacetFile[f], nil }
func (n bulkNotes) imports(f string) ([]lang.Import, error)   { return n.byImportFile[f], nil }
func (n bulkNotes) importers(p string) ([]lang.Import, error) { return n.byImported[p], nil }
