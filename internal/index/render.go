package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/index/lang"
)

// RenderNotes writes the per-file index notes and the root rollup from the
// current index state. It never writes to per-file hint markdown (.githints/<path>.md)
// or CHANGES.md, which are integrity-verified by the hint package.
func RenderNotes(db *Store, root string, obsidian bool) error {
	files, err := db.AllIndexedFiles()
	if err != nil {
		return fmt.Errorf("list indexed files: %w", err)
	}

	// Build the import-path → file map once; renderFileNote uses it to link
	// outbound imports to the notes of the files providing them.
	importToFile := resolveImportPaths(db, root)

	rendered := make(map[string]struct{}, len(files))
	for _, src := range files {
		if err := renderFileNote(db, root, src, obsidian, importToFile); err != nil {
			return fmt.Errorf("render note for %s: %w", src, err)
		}
		if notePath, _, err := lang.IndexNotePath(root, src); err == nil {
			rendered[filepath.Clean(notePath)] = struct{}{}
		}
	}

	pruneStaleNotes(root, rendered)
	writeObsidianGraphPreset(root)

	if err := renderIndexRollup(db, root, obsidian); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}

	return nil
}

// writeObsidianGraphPreset seeds a default Graph view filter when the vault
// has none, so the first graph opened shows the structural index instead of
// the change-journal notes. It never overwrites an existing configuration:
// once the user touches Graph view settings, their choices win.
func writeObsidianGraphPreset(root string) {
	path := filepath.Join(root, ".githints", ".obsidian", "graph.json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Missing keys fall back to Obsidian defaults. hideUnresolved drops
	// dangling links to notes that don't exist; showOrphans keeps
	// legitimately isolated files (entrypoints) visible.
	preset := `{"search":"path:index","hideUnresolved":true,"showOrphans":true}` + "\n"
	if err := os.WriteFile(path, []byte(preset), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not write Obsidian graph preset: %v\n", err)
	}
}

// pruneStaleNotes deletes note files under the index notes directory that
// were not produced by the current render — leftovers from files that were
// deleted, renamed, or dropped from the configured languages. Empty
// directories are removed afterwards, children before parents.
func pruneStaleNotes(root string, keep map[string]struct{}) {
	notesRoot := lang.IndexNotesPath(root)
	var dirs []string
	err := filepath.WalkDir(notesRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != notesRoot {
				dirs = append(dirs, path)
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		if _, ok := keep[filepath.Clean(path)]; !ok {
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, "githints: could not remove stale index note %s: %v\n", path, err)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: stale note prune failed: %v\n", err)
		return
	}
	// Parents are always shorter than their children, so length-descending
	// order guarantees children are processed first.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
}

func renderFileNote(db *Store, root, src string, obsidian bool, importToFile map[string]string) error {
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

	symbols, err := db.SymbolsForFile(src)
	if err != nil {
		return fmt.Errorf("load symbols: %w", err)
	}

	// Dependents are stored by import path (Go module path, or the
	// normalized TS-family file key), not by file path, so resolve before
	// querying — the same lookup the get_dependents MCP tool does. When the
	// path cannot be resolved (e.g. a Go file outside a module) no importer
	// could reference it either, so the section is simply omitted.
	var imports []lang.Import
	if importPath, err := lang.LocalImportPath(root, src); err == nil {
		imports, err = db.FilesImporting(importPath)
		if err != nil {
			return fmt.Errorf("load dependents: %w", err)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", src)

	if len(symbols) > 0 {
		b.WriteString("## Symbols\n\n")
		for _, sym := range symbols {
			fmt.Fprintf(&b, "- `%s` (%s) lines %d-%d", sym.Name, sym.Kind, sym.LineStart, sym.LineEnd)
			if sym.Signature != "" {
				fmt.Fprintf(&b, " — `%s`", sym.Signature)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	outbound, err := db.ImportsForFile(src)
	if err != nil {
		return fmt.Errorf("load imports: %w", err)
	}
	if len(outbound) > 0 {
		b.WriteString("## Imports\n\n")
		for _, imp := range outbound {
			if file, ok := importToFile[imp.ImportedPath]; ok && file != src {
				// The import resolves to an indexed file: link to its note.
				fmt.Fprintf(&b, "- %s\n", lang.NoteLink(indexDirOf(src), imp.ImportedPath, file, obsidian))
			} else {
				// Stdlib, external package, or unresolvable alias.
				fmt.Fprintf(&b, "- `%s`\n", imp.ImportedPath)
			}
		}
		b.WriteString("\n")
	}

	if len(imports) > 0 {
		b.WriteString("## Imported by\n\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "- %s\n", lang.NoteLink(indexDirOf(src), imp.FilePath, imp.FilePath, obsidian))
		}
		b.WriteString("\n")
	}

	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		return fmt.Errorf("mkdir note dir: %w", err)
	}
	return os.WriteFile(notePath, []byte(b.String()), 0o644)
}

func renderIndexRollup(db *Store, root string, obsidian bool) error {
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
		importToFile := resolveImportPaths(db, root)
		for _, h := range hubs {
			if file, ok := importToFile[h.File]; ok {
				// Hub entries are import paths; link them to the file's note.
				fmt.Fprintf(&b, "- %d import(s): %s\n", h.Dependents, lang.NoteLink(".", h.File, file, obsidian))
			} else {
				// Stdlib packages, external modules, and unresolvable path
				// aliases have no note to link to.
				fmt.Fprintf(&b, "- %d import(s): `%s`\n", h.Dependents, h.File)
			}
		}
	}

	roll := lang.IndexRollupPath(root)
	if err := os.MkdirAll(filepath.Dir(roll), 0o755); err != nil {
		return fmt.Errorf("mkdir rollup dir: %w", err)
	}
	return os.WriteFile(roll, []byte(b.String()), 0o644)
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
func resolveImportPaths(db *Store, root string) map[string]string {
	files, err := db.AllIndexedFiles()
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(files))
	for _, f := range files {
		importPath, err := lang.LocalImportPath(root, f)
		if err != nil {
			continue
		}
		if _, taken := m[importPath]; !taken {
			m[importPath] = f
		}
	}
	return m
}
