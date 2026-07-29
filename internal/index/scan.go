package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/recorder"
)

// FullScan walks the repository under opts.Root, parses every supported file,
// and writes the result to db. It is idempotent: it clears the existing index
// first, then re-parses every file.
func FullScan(db *Store, opts lang.ScanOptions) error {
	registry := lang.NewRegistry()
	parsers, err := registry.ResolveLanguages(opts.Languages)
	if err != nil {
		return err
	}
	set := lang.ParserSet(parsers)
	extMap := lang.ExtensionMap(set)

	meta := lang.IndexMeta{LanguageCounts: make(map[string]int)}

	var allSymbols []lang.Symbol
	var allImports []lang.Import

	// Walk in deterministic order so repeated scans are byte-for-byte identical.
	err = filepath.WalkDir(opts.Root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(opts.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		// Skip the .githints directory itself — we do not want to index our own
		// cache, notes, or state files.
		if rel == ".githints" || strings.HasPrefix(rel, ".githints/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			meta.SkippedCount++
			return nil
		}

		if shouldSkipFile(path, info) {
			meta.SkippedCount++
			return nil
		}

		if shouldIgnoreFile(opts.Root, rel) {
			return nil
		}

		parser := lang.SelectParser(extMap, rel)
		if parser == nil {
			meta.UnsupportedCount++
			return nil
		}

		if info.Size() > opts.MaxFileSize {
			meta.SkippedCount++
			// Warn to stderr; do not abort the scan.
			fmt.Fprintf(os.Stderr, "githints: index skipped (too large): %s (%d bytes)\n", rel, info.Size())
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			meta.SkippedCount++
			fmt.Fprintf(os.Stderr, "githints: index skipped (read error): %s: %v\n", rel, err)
			return nil
		}

		symbols, imports, err := parseWithTimeout(parser, rel, src, opts.ParseTimeout)
		if err != nil {
			meta.SkippedCount++
			fmt.Fprintf(os.Stderr, "githints: index skipped (parse error): %s: %v\n", rel, err)
			return nil
		}

		// Record language only for files that actually produced symbols.
		if len(symbols) > 0 {
			meta.LanguageCounts[parser.Language()]++
			meta.FileCount++
			meta.SymbolCount += len(symbols)
			for i := range symbols {
				allSymbols = append(allSymbols, symbols[i])
			}
		}
		for i := range imports {
			allImports = append(allImports, imports[i])
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	if err := db.Clear(); err != nil {
		return err
	}
	if err := db.InsertSymbols(allSymbols); err != nil {
		return err
	}
	if err := db.InsertImports(allImports); err != nil {
		return err
	}
	meta.LastIndexedAt = time.Now().Unix()
	if err := db.SetMeta(meta); err != nil {
		return err
	}
	if err := db.Vacuum(); err != nil {
		// Vacuum is purely an optimization; log and continue.
		fmt.Fprintf(os.Stderr, "githints: index vacuum: %v\n", err)
	}

	return RenderNotes(db, opts.Root, false)
}

func shouldSkipFile(path string, info os.FileInfo) bool {
	mode := info.Mode()

	// Skip symlinks, devices, FIFOs, and sockets. Never follow a symlink: a
	// malicious symlink could escape the repo or point to a system device.
	if mode&os.ModeSymlink != 0 ||
		mode&os.ModeDevice != 0 ||
		mode&os.ModeNamedPipe != 0 ||
		mode&os.ModeSocket != 0 ||
		mode&os.ModeCharDevice != 0 {
		return true
	}

	// Skip irregular files (unknown mode) for the same reason.
	if mode&os.ModeIrregular != 0 {
		return true
	}

	return false
}

// shouldIgnoreFile asks git whether a repo-relative path is ignored. It is
// two layered passes:
//
//  1. `git check-ignore --no-index -`. This checks only .gitignore and the
//     standard exclude mechanisms (e.g. .git/info/exclude). If git ignores the
//     file, it is ignored.
//  2. `git -c core.excludesFile=<.githintsignore> check-ignore --no-index -`,
//     run only for paths that passed the first pass. This adds the
//     .githintsignore patterns on top of the normal mechanisms.
//
// The .githintsignore file cannot re-include a file git already excluded
// because the second pass only runs if the first pass said "not ignored".
func shouldIgnoreFile(root, rel string) bool {
	// First pass: .gitignore and friends.
	if gitCheckIgnore(root, rel, "") {
		return true
	}
	// Second pass: .githintsignore subtracts additional files.
	hintsIgnore := filepath.Join(root, ".githintsignore")
	if fileExists(hintsIgnore) {
		if gitCheckIgnore(root, rel, hintsIgnore) {
			return true
		}
	}
	return false
}

// gitCheckIgnore runs `git check-ignore --no-index --stdin` for one path. If
// excludesFile is non-empty, it is passed via -c core.excludesFile so the file
// is treated as an additional global exclude file.
func gitCheckIgnore(root, rel, excludesFile string) bool {
	var cmd *exec.Cmd
	if excludesFile != "" {
		cmd = exec.Command("git", "-c", "core.excludesFile="+excludesFile, "check-ignore", "--no-index", "--stdin")
	} else {
		cmd = exec.Command("git", "check-ignore", "--no-index", "--stdin")
	}
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(rel + "\n")
	out, err := cmd.CombinedOutput()
	// git check-ignore exits 0 when the path is ignored and outputs the path,
	// exits 1 when the path is not ignored, and may exit non-zero on errors.
	// We treat any output as "ignored"; missing output with exit 0 is unusual
	// but conservatively treated as not ignored.
	return err == nil && len(out) > 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func parseWithTimeout(p lang.LanguageParser, rel string, src []byte, timeout time.Duration) ([]lang.Symbol, []lang.Import, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type result struct {
		symbols []lang.Symbol
		imports []lang.Import
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("parser panic: %v", r)}
			}
		}()
		symbols, imports, err := p.Parse(rel, src)
		ch <- result{symbols: symbols, imports: imports, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("parse timed out after %s", timeout)
	case res := <-ch:
		return res.symbols, res.imports, res.err
	}
}

// ValidateFilePath reuses the recorder's path validation so index commands
// and MCP tools reject paths that escape the repo root.
func ValidateFilePath(p string) error {
	return recorder.ValidateFilePath(p)
}

// IncrementalScan re-indexes the files in paths. Deleted files are detected
// when their path no longer exists on disk; existing files are parsed and
// their rows replaced. This is the hook path used in Phase 2.
func IncrementalScan(db *Store, opts lang.ScanOptions, paths []string) error {
	registry := lang.NewRegistry()
	parsers, err := registry.ResolveLanguages(opts.Languages)
	if err != nil {
		return err
	}
	set := lang.ParserSet(parsers)
	extMap := lang.ExtensionMap(set)

	for _, rel := range paths {
		if rel == "" || strings.HasPrefix(rel, ".githints/") {
			continue
		}
		if err := indexOneFile(db, opts, registry, extMap, rel); err != nil {
			fmt.Fprintf(os.Stderr, "githints: incremental index error for %s: %v\n", rel, err)
			// Continue with the remaining files; one bad file should not stop
			// the rest of the commit from being recorded.
		}
	}

	if err := RenderRollup(db, opts.Root, false); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}

	if err := updateMetaLastIndexed(db, registry); err != nil {
		return fmt.Errorf("update index meta: %w", err)
	}

	return nil
}

func indexOneFile(db *Store, opts lang.ScanOptions, registry *lang.Registry, extMap map[string]lang.LanguageParser, rel string) error {
	fullPath := filepath.Join(opts.Root, rel)

	// Deleted: remove the rows and note. We still consider .githintsignore
	// for a deleted file in case it was previously indexed and is now excluded.
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return deleteFileIndex(db, opts.Root, rel)
		}
		return fmt.Errorf("stat %s: %w", rel, err)
	}

	if shouldSkipFile(fullPath, info) {
		// If a previously indexed file turned into a symlink/device, drop it.
		return deleteFileIndex(db, opts.Root, rel)
	}

	if shouldIgnoreFile(opts.Root, rel) {
		return deleteFileIndex(db, opts.Root, rel)
	}

	parser := lang.SelectParser(extMap, rel)
	if parser == nil {
		// Unsupported file: drop any previous index rows so the index does not
		// keep stale symbols for a file that no longer has a parser.
		return deleteFileIndex(db, opts.Root, rel)
	}

	if info.Size() > opts.MaxFileSize {
		fmt.Fprintf(os.Stderr, "githints: index skipped (too large): %s (%d bytes)\n", rel, info.Size())
		return deleteFileIndex(db, opts.Root, rel)
	}

	src, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}

	symbols, imports, err := parseWithTimeout(parser, rel, src, opts.ParseTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: index skipped (parse error): %s: %v\n", rel, err)
		return deleteFileIndex(db, opts.Root, rel)
	}

	if err := db.DeleteFile(rel); err != nil {
		return fmt.Errorf("delete old rows for %s: %w", rel, err)
	}
	if err := db.InsertSymbols(symbols); err != nil {
		return fmt.Errorf("insert symbols for %s: %w", rel, err)
	}
	if err := db.InsertImports(imports); err != nil {
		return fmt.Errorf("insert imports for %s: %w", rel, err)
	}

	if err := renderFileNote(db, opts.Root, rel, false); err != nil {
		return fmt.Errorf("render note for %s: %w", rel, err)
	}
	return nil
}

func deleteFileIndex(db *Store, root, rel string) error {
	if err := db.DeleteFile(rel); err != nil {
		return fmt.Errorf("delete rows for %s: %w", rel, err)
	}
	notePath, collision, err := lang.IndexNotePath(root, rel)
	if err != nil {
		if collision {
			// Nothing to delete; the collision prevented a note from being written.
			return nil
		}
		return err
	}
	if err := os.Remove(notePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove note for %s: %w", rel, err)
	}
	return nil
}

func updateMetaLastIndexed(db *Store, registry *lang.Registry) error {
	meta, err := db.Meta()
	if err != nil {
		return err
	}
	meta.LastIndexedAt = time.Now().Unix()

	files, err := db.FileCount()
	if err != nil {
		return err
	}
	meta.FileCount = files

	symbols, err := db.SymbolCount()
	if err != nil {
		return err
	}
	meta.SymbolCount = symbols

	// Recompute language counts from the current symbols table.
	meta.LanguageCounts, err = languageCountsFromSymbols(db, registry)
	if err != nil {
		return err
	}

	return db.SetMeta(meta)
}

func languageCountsFromSymbols(db *Store, registry *lang.Registry) (map[string]int, error) {
	files, err := db.AllIndexedFiles()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, f := range files {
		p := registry.ForPath(f)
		if p == nil {
			continue
		}
		counts[p.Language()]++
	}
	return counts, nil
}
