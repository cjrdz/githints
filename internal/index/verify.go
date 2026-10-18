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
	// StaleKeys are files whose stored import key differs from the one
	// computed now: a project file such as go.mod changed in a way the hook
	// did not see. The notes link through these keys.
	StaleKeys []string
}

// Drift reports whether the index disagrees with the repository: stale notes
// or ghost rows. Uncovered files are informational and do not count as drift.
func (r *IndexReport) Drift() bool {
	return len(r.StaleNotes) > 0 || len(r.GhostFiles) > 0 || len(r.StaleKeys) > 0
}

// VerifyIndex compares the index database, the rendered notes, and the
// git-tracked source files, and reports any disagreement. It never mutates
// anything; run a full scan to repair drift.
func VerifyIndex(db *Store, opts lang.ScanOptions) (*IndexReport, error) {
	registry := lang.NewRegistryForRoot(opts.Root)
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

	// Stored import keys that no longer match: the hook keeps them current
	// for the files a commit touches, so this is the check that it did.
	if db.keysComplete() {
		want := resolveImportPathsFor(files, opts.Root, registry)
		stored, err := db.storedKeyMap()
		if err != nil {
			return nil, fmt.Errorf("read import keys: %w", err)
		}
		for _, f := range files {
			w, wok := want.ofFile[f]
			s, sok := stored[f]
			if wok != sok || w != s {
				report.StaleKeys = append(report.StaleKeys, f)
			}
		}
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
	ignores := scanIgnores(opts.Root)
	ignoredDir := ignores.dirs
	type uncovered struct {
		rel  string
		size int64
	}
	var missing []uncovered
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
			if ignoredDir[rel] {
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
		missing = append(missing, uncovered{rel: rel, size: info.Size()})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk source files: %w", err)
	}
	// The reasons. The listing answers for every file; when it failed, one
	// batched check for the uncovered ones.
	ignored := ignores.files
	if ignored == nil {
		rels := make([]string, len(missing))
		for i, m := range missing {
			rels[i] = m.rel
		}
		ignored = resolveIgnored(opts.Root, rels)
	}
	for _, m := range missing {
		reason := "no symbols or imports"
		switch {
		case ignored[m.rel]:
			reason = "gitignored"
		case m.size > opts.MaxFileSize:
			reason = "too large"
		}
		report.Uncovered = append(report.Uncovered, UncoveredFile{Path: m.rel, Reason: reason})
	}
	return report, nil
}
