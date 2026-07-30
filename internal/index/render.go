package index

import (
	"fmt"
	"os"
	"path/filepath"
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

	for _, src := range files {
		if err := renderFileNote(db, root, src, obsidian); err != nil {
			return fmt.Errorf("render note for %s: %w", src, err)
		}
	}

	if err := renderIndexRollup(db, root, obsidian); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}

	return nil
}

func renderFileNote(db *Store, root, src string, obsidian bool) error {
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

	if len(imports) > 0 {
		b.WriteString("## Imported by\n\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "- %s\n", lang.FileLink(root, imp.FilePath, obsidian))
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
		for _, h := range hubs {
			fmt.Fprintf(&b, "- %d import(s): %s\n", h.Dependents, lang.FileLink(root, h.File, obsidian))
		}
	}

	roll := lang.IndexRollupPath(root)
	if err := os.MkdirAll(filepath.Dir(roll), 0o755); err != nil {
		return fmt.Errorf("mkdir rollup dir: %w", err)
	}
	return os.WriteFile(roll, []byte(b.String()), 0o644)
}
