package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/index/lang"
)

// UncoveredFile is a source file with no rows in the index, along with the
// reason it is absent.
type UncoveredFile struct {
	Path   string
	Reason string
}

// IndexReport describes drift between the index, the rendered notes, and the
// files actually in the repository.
type IndexReport struct {
	// Notes is the number of note files under the index notes directory.
	Notes int
	// StaleNotes are note files whose source has no rows in the index
	// (deleted, renamed, or dropped from the configured languages).
	StaleNotes []string
	// GhostFiles have rows in the index but no longer exist on disk.
	GhostFiles []string
	// Uncovered are source files on disk (in an enabled language) with no
	// index rows, each with a reason: "gitignored", "too large", or
	// "no symbols or imports".
	Uncovered []UncoveredFile
}

// Drift reports whether the index disagrees with the repository: stale notes
// or ghost rows. Uncovered files are informational and do not count as drift.
func (r *IndexReport) Drift() bool {
	return len(r.StaleNotes) > 0 || len(r.GhostFiles) > 0
}

// VerifyIndex compares the index database, the rendered notes, and the
// git-tracked source files, and reports any disagreement. It never mutates
// anything; run a full scan to repair drift.
func VerifyIndex(db *Store, opts lang.ScanOptions) (*IndexReport, error) {
	registry := lang.NewRegistry()
	parsers, err := registry.ResolveLanguages(opts.Languages)
	if err != nil {
		return nil, err
	}
	extMap := lang.ExtensionMap(lang.ParserSet(parsers))

	indexed := make(map[string]struct{})
	files, err := db.AllIndexedFiles()
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}
	for _, f := range files {
		indexed[f] = struct{}{}
	}

	report := &IndexReport{}

	// Notes on disk whose source is not indexed.
	notesRoot := lang.IndexNotesPath(opts.Root)
	if err := filepath.WalkDir(notesRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		report.Notes++
		rel, err := filepath.Rel(notesRoot, path)
		if err != nil {
			return nil
		}
		src := filepath.ToSlash(strings.TrimSuffix(rel, ".md"))
		if _, ok := indexed[src]; !ok {
			report.StaleNotes = append(report.StaleNotes, src)
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("walk notes: %w", err)
	}

	// Indexed files that no longer exist on disk.
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(opts.Root, f)); os.IsNotExist(err) {
			report.GhostFiles = append(report.GhostFiles, f)
		}
	}

	// Source files on disk in enabled languages with no index rows. The walk
	// mirrors FullScan's (same skip rules), so the report explains every
	// supported file the index does not cover.
	if err := filepath.WalkDir(opts.Root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(opts.Root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".githints" || strings.HasPrefix(rel, ".githints/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Same pruning as FullScan: git internals and ignored directories
			// (node_modules, dist) are never descended into.
			if rel == ".git" {
				return filepath.SkipDir
			}
			if shouldIgnoreFile(opts.Root, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || shouldSkipFile(path, info) {
			return nil
		}
		if lang.SelectParser(extMap, rel) == nil {
			return nil
		}
		if _, ok := indexed[rel]; ok {
			return nil
		}
		reason := "no symbols or imports"
		switch {
		case shouldIgnoreFile(opts.Root, rel):
			reason = "gitignored"
		case info.Size() > opts.MaxFileSize:
			reason = "too large"
		}
		report.Uncovered = append(report.Uncovered, UncoveredFile{Path: rel, Reason: reason})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk source files: %w", err)
	}
	return report, nil
}
