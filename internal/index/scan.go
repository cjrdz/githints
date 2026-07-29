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
//
// If force is false, the scan refuses to overwrite a larger existing index with
// a smaller one (partial-write guard) and refuses to write data that would exceed
// maxBytes. Use force to override either guard.
func FullScan(db *Store, opts lang.ScanOptions, force bool, maxBytes int) error {
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

	if err := guardWrite(db, allSymbols, allImports, force, maxBytes); err != nil {
		return err
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

	return RenderNotes(db, opts.Root, opts.Obsidian)
}

// guardWrite enforces the Phase 5 safety checks before mutating the index.
// It returns an error if the new scan would be a partial write (fewer rows than
// the existing index) or would exceed the configured size cap, unless force is true.
func guardWrite(db *Store, symbols []lang.Symbol, imports []lang.Import, force bool, maxBytes int) error {
	existingSymbols, err := db.SymbolCount()
	if err != nil {
		return fmt.Errorf("check existing symbol count: %w", err)
	}
	existingImports, err := db.ImportCount()
	if err != nil {
		return fmt.Errorf("check existing import count: %w", err)
	}
	existingRows := existingSymbols + existingImports
	newRows := len(symbols) + len(imports)

	if !force && newRows < existingRows {
		return fmt.Errorf("partial write detected: %d new rows vs %d existing; use --force to overwrite", newRows, existingRows)
	}

	if maxBytes > 0 {
		var estimated int64
		if size := db.Size(); size > 0 {
			estimated = size
		}
		const symbolOverhead = 64
		for _, sym := range symbols {
			estimated += int64(len(sym.Name) + len(string(sym.Kind)) + len(sym.FilePath) + len(sym.Signature) + symbolOverhead)
		}
		const importOverhead = 32
		for _, imp := range imports {
			estimated += int64(len(imp.FilePath) + len(imp.ImportedPath) + importOverhead)
		}
		if !force && estimated > int64(maxBytes) {
			return fmt.Errorf("index would exceed max_bytes (%d): estimated %d bytes; use --force to overwrite", maxBytes, estimated)
		}
	}

	return nil
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

	for _, path := range paths {
		if err := recorder.ValidateFilePath(path); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index skipped (invalid path): %s: %v\n", path, err)
			continue
		}
		if path == ".githints" || strings.HasPrefix(path, ".githints/") {
			continue
		}
		abs := filepath.Join(opts.Root, path)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				// Deleted file: remove its rows and its note.
				if err := db.DeleteFile(path); err != nil {
					fmt.Fprintf(os.Stderr, "githints: index delete failed: %s: %v\n", path, err)
				}
				if note, _, noteErr := lang.IndexNotePath(opts.Root, path); noteErr == nil {
					if err := os.Remove(note); err != nil && !os.IsNotExist(err) {
						fmt.Fprintf(os.Stderr, "githints: index note delete failed: %s: %v\n", path, err)
					}
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "githints: index stat failed: %s: %v\n", path, err)
			continue
		}

		if shouldSkipFile(abs, info) {
			continue
		}
		if shouldIgnoreFile(opts.Root, path) {
			continue
		}
		if info.Size() > opts.MaxFileSize {
			fmt.Fprintf(os.Stderr, "githints: index skipped (too large): %s (%d bytes)\n", path, info.Size())
			continue
		}

		parser := lang.SelectParser(extMap, path)
		if parser == nil {
			// Unsupported extension: delete any previously indexed rows.
			if err := db.DeleteFile(path); err != nil {
				fmt.Fprintf(os.Stderr, "githints: index delete failed: %s: %v\n", path, err)
			}
			continue
		}

		src, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: index skipped (read error): %s: %v\n", path, err)
			continue
		}

		symbols, imports, err := parseWithTimeout(parser, path, src, opts.ParseTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: index skipped (parse error): %s: %v\n", path, err)
			continue
		}

		if err := db.DeleteFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index delete failed: %s: %v\n", path, err)
			continue
		}
		if err := db.InsertSymbols(symbols); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert symbols failed: %s: %v\n", path, err)
			continue
		}
		if err := db.InsertImports(imports); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert imports failed: %s: %v\n", path, err)
			continue
		}
		if err := renderFileNote(db, opts.Root, path, opts.Obsidian); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index note render failed: %s: %v\n", path, err)
		}
	}

	// Refresh meta timestamp and the rollup.
	meta, err := db.Meta()
	if err != nil {
		return fmt.Errorf("read index meta: %w", err)
	}
	meta.LastIndexedAt = time.Now().Unix()
	if err := db.SetMeta(meta); err != nil {
		return fmt.Errorf("set index meta: %w", err)
	}
	if err := renderIndexRollup(db, opts.Root, opts.Obsidian); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}

	return nil
}
