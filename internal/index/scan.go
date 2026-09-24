package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
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
	registry := lang.NewRegistryForRoot(opts.Root)
	// Strict on purpose. The user ran `githints index`, so an unsupported
	// language is worth stopping for, and the error names the supported set.
	// IncrementalScan is deliberately lenient; see the comment there.
	parsers, err := registry.ResolveLanguages(opts.Languages)
	if err != nil {
		return err
	}
	set := lang.ParserSet(parsers)
	extMap := lang.ExtensionMap(set)

	// Let each enabled parser install whatever per-scan state it needs, such
	// as the TypeScript path aliases. Adding a language with its own project
	// configuration no longer means editing this function.
	defer lang.BeginScans(parsers, opts.Root)()

	meta := lang.IndexMeta{LanguageCounts: make(map[string]int)}

	detectors, detectorErrs := lang.EmbeddedDetectors()
	for _, err := range detectorErrs {
		fmt.Fprintf(os.Stderr, "githints: framework detector ignored: %v\n", err)
	}

	var candidates []candidate

	var allSymbols []lang.Symbol
	var allImports []lang.Import
	var allFacets []lang.Detected

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
			// Never descend into git internals, and prune ignored directories
			// (node_modules, dist, vendor): one check per directory replaces
			// one per file inside it, and the results are identical because
			// per-file ignore rules still apply to everything below.
			if rel == ".git" {
				return filepath.SkipDir
			}
			if shouldIgnoreFile(opts.Root, rel) {
				return filepath.SkipDir
			}
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

		// Parser selection first: it is a map lookup, and it discards most of
		// a typical repository. The ignore check that used to run here cost
		// 1.3ms per path in process startup, so asking it about assets nobody
		// can parse was most of a full scan.
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

		candidates = append(candidates, candidate{rel: rel, abs: path, parser: parser})
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// One pair of git invocations for every candidate, rather than one pair
	// each.
	rels := make([]string, 0, len(candidates))
	for _, c := range candidates {
		rels = append(rels, c.rel)
	}
	ignored := resolveIgnored(opts.Root, rels)

	work := candidates[:0:0]
	for _, c := range candidates {
		if !ignored[c.rel] {
			work = append(work, c)
		}
	}

	// Reading, parsing and framework detection are per-file and independent,
	// and together they are most of what a scan spends its time on. They run
	// across a worker pool; everything that follows is merged back in walk
	// order.
	results := parseCandidates(work, opts, detectors)

	for i, c := range work {
		r := results[i]
		if r.warn != "" {
			meta.SkippedCount++
			// Warnings are emitted here rather than from the worker so the
			// order of stderr matches the order of the files, whatever order
			// the pool happened to finish in.
			fmt.Fprintln(os.Stderr, r.warn)
			continue
		}
		// Record language only for files that actually produced symbols.
		if len(r.symbols) > 0 {
			meta.LanguageCounts[c.parser.Language()]++
			meta.FileCount++
			meta.SymbolCount += len(r.symbols)
			allSymbols = append(allSymbols, r.symbols...)
		}
		allImports = append(allImports, r.imports...)
		allFacets = append(allFacets, r.facets...)
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
	if err := db.InsertFacets(allFacets); err != nil {
		return err
	}
	meta.LastIndexedAt = time.Now().Unix()
	if err := db.SetMeta(meta); err != nil {
		return err
	}
	if err := db.ReclaimSpace(); err != nil {
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
		// Stamp the language here rather than in each parser: a parser that
		// forgot would produce symbols that no language filter could find,
		// and nothing would fail loudly enough to notice.
		language := p.Language()
		for i := range res.symbols {
			res.symbols[i].Language = language
		}
		return res.symbols, res.imports, res.err
	}
}

// IncrementalScan re-indexes the files in paths. Deleted files are detected
// when their path no longer exists on disk; existing files are parsed and
// their rows replaced. This is the hook path used in Phase 2.
func IncrementalScan(db *Store, opts lang.ScanOptions, paths []string) error {
	registry := lang.NewRegistryForRoot(opts.Root)
	// Lenient on purpose: this runs from the post-commit hook, which only
	// warns on a scan error, so a hard failure here means the commit succeeds
	// while the index silently stops updating forever. FullScan and
	// VerifyIndex stay strict because the user invoked those directly and an
	// error is the answer they asked for.
	parsers, unknown, err := registry.ResolveKnownLanguages(opts.Languages)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr,
			"githints: index: ignoring unsupported language(s) %s; indexing the rest (supported: %s)\n",
			strings.Join(unknown, ", "), strings.Join(registry.Languages(), ", "))
	}
	detectors, detectorErrs := lang.EmbeddedDetectors()
	for _, err := range detectorErrs {
		fmt.Fprintf(os.Stderr, "githints: framework detector ignored: %v\n", err)
	}
	set := lang.ParserSet(parsers)
	extMap := lang.ExtensionMap(set)

	// Let each enabled parser install whatever per-scan state it needs, such
	// as the TypeScript path aliases. Adding a language with its own project
	// configuration no longer means editing this function.
	defer lang.BeginScans(parsers, opts.Root)()

	// Files whose rows were written and which therefore need a note.
	var rendered []string

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
		if err := db.InsertFacets(detectFacets(detectors, parser, path, src, imports)); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert facets failed: %s: %v\n", path, err)
			continue
		}
		rendered = append(rendered, path)
	}

	// resolveImportPaths reads every indexed file and resolves each one's
	// import path, so calling it per changed file made an incremental scan
	// cost O(changed x indexed): the same twenty files took 51ms, 146ms and
	// 1.08s as the index grew. It is computed once here instead.
	//
	// It has to come after the write loop, not before: the map must include
	// the rows this scan just inserted, or a new file would render without
	// its own links.
	importToFile := resolveImportPaths(db, opts.Root, registry)
	for _, path := range rendered {
		if err := renderFileNote(db, opts.Root, path, opts.Obsidian, importToFile, registry); err != nil {
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
	if err := renderIndexRollup(db, opts.Root, opts.Obsidian, registry); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}

	return nil
}

// lineBlanker is implemented by parsers that can expose the blanked views of a
// file. Framework detection matches lines, and matching raw source would let a
// declaration inside a comment or an example inside a docstring be reported as
// real code.
type lineBlanker interface {
	BlankLines(src []byte) []string
	BlankLinesKeepingStrings(src []byte) []string
}

// detectFacets runs framework detection over one file.
//
// A parser that cannot produce blanked views simply contributes no facets:
// guessing from raw source would be worse than reporting nothing.
func detectFacets(set *lang.DetectorSet, parser lang.LanguageParser, rel string, src []byte, imports []lang.Import) []lang.Detected {
	if set.Empty() {
		return nil
	}
	bl, ok := parser.(lineBlanker)
	if !ok {
		return nil
	}
	return set.Detect(lang.DetectInput{
		FilePath: rel,
		Language: parser.Language(),
		Imports:  imports,
		Code:     bl.BlankLines(src),
		Strings:  bl.BlankLinesKeepingStrings(src),
	})
}

// ignoreSet is the resolved ignore status of a batch of paths.
type ignoreSet map[string]bool

// resolveIgnored asks git about every path in one pair of invocations instead
// of one pair per path.
//
// `git check-ignore` costs roughly 1.3ms per call, nearly all of it process
// startup, which made it three quarters of a full scan. Batching removes the
// per-path cost entirely; the semantics are unchanged because the two passes
// are kept separate, which is what keeps .githintsignore subtract-only: the
// second pass only ever sees paths the first said were not ignored.
//
// On failure it falls back to the per-path check rather than guessing, since
// guessing "not ignored" would index files the user excluded.
func resolveIgnored(root string, rels []string) ignoreSet {
	if len(rels) == 0 {
		return ignoreSet{}
	}

	ignored, err := batchCheckIgnore(root, rels, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: batched ignore check failed (%v); falling back to per-path\n", err)
		return perPathIgnored(root, rels)
	}

	hintsIgnore := filepath.Join(root, ".githintsignore")
	if fileExists(hintsIgnore) {
		var remaining []string
		for _, rel := range rels {
			if !ignored[rel] {
				remaining = append(remaining, rel)
			}
		}
		extra, err := batchCheckIgnore(root, remaining, hintsIgnore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: batched .githintsignore check failed (%v); falling back to per-path\n", err)
			return perPathIgnored(root, rels)
		}
		for rel := range extra {
			ignored[rel] = true
		}
	}
	return ignored
}

func perPathIgnored(root string, rels []string) ignoreSet {
	out := make(ignoreSet, len(rels))
	for _, rel := range rels {
		if shouldIgnoreFile(root, rel) {
			out[rel] = true
		}
	}
	return out
}

// batchCheckIgnore runs one `git check-ignore` over every path and returns the
// ones git excludes.
func batchCheckIgnore(root string, rels []string, excludesFile string) (ignoreSet, error) {
	out := make(ignoreSet)
	if len(rels) == 0 {
		return out, nil
	}

	args := []string{}
	if excludesFile != "" {
		args = append(args, "-c", "core.excludesFile="+excludesFile)
	}
	// -z on both sides: a path may legitimately contain a newline, and a
	// mis-split line would silently mark the wrong file ignored.
	args = append(args, "check-ignore", "--no-index", "--stdin", "-z")

	cmd := exec.Command("git", args...)
	cmd.Dir = root

	var stdin bytes.Buffer
	for _, rel := range rels {
		stdin.WriteString(rel)
		stdin.WriteByte(0)
	}
	cmd.Stdin = &stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	// Exit status 1 means "nothing matched", which is a normal answer rather
	// than a failure. Anything else with output on stderr is a real problem.
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	if stdout.Len() > gitutil.MaxOutputBytes {
		return nil, fmt.Errorf("check-ignore produced %d bytes, over the cap of %d", stdout.Len(), gitutil.MaxOutputBytes)
	}

	for _, p := range strings.Split(stdout.String(), "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out, nil
}

// candidate is a file the walk accepted, held until the batched ignore check
// can rule on all of them at once.
type candidate struct {
	rel    string
	abs    string
	parser lang.LanguageParser
}

// maxParseWorkers caps the pool. Past a dozen, a scan is bounded by the
// filesystem and the store rather than by CPU, and more workers only add
// scheduling and memory pressure. tgrep caps its own walker at the same number
// for the same reason.
const maxParseWorkers = 12

// fileResult is one file's contribution, or the warning explaining why it has
// none.
type fileResult struct {
	symbols []lang.Symbol
	imports []lang.Import
	facets  []lang.Detected
	warn    string
}

// parseCandidates reads, parses and runs detection over every candidate,
// returning results in the same order it was given them.
//
// Order is not an implementation detail here: rendered notes are committed in
// shared mode, so a scan that emitted symbols in completion order would
// produce a different diff on every run. Each worker writes to its own slot
// and nothing is appended concurrently.
func parseCandidates(work []candidate, opts lang.ScanOptions, detectors *lang.DetectorSet) []fileResult {
	results := make([]fileResult, len(work))
	if len(work) == 0 {
		return results
	}

	workers := runtime.NumCPU()
	if workers > maxParseWorkers {
		workers = maxParseWorkers
	}
	if workers > len(work) {
		workers = len(work)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = parseOne(work[i], opts, detectors)
			}
		}()
	}
	for i := range work {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return results
}

// parseOne is the per-file work a pool worker does. It touches nothing shared:
// parsers and detectors are immutable once built, and the per-scan state they
// consult is installed before the pool starts and read under a lock.
func parseOne(c candidate, opts lang.ScanOptions, detectors *lang.DetectorSet) fileResult {
	src, err := os.ReadFile(c.abs)
	if err != nil {
		return fileResult{warn: fmt.Sprintf("githints: index skipped (read error): %s: %v", c.rel, err)}
	}

	symbols, imports, err := parseWithTimeout(c.parser, c.rel, src, opts.ParseTimeout)
	if err != nil {
		return fileResult{warn: fmt.Sprintf("githints: index skipped (parse error): %s: %v", c.rel, err)}
	}

	return fileResult{
		symbols: symbols,
		imports: imports,
		facets:  detectFacets(detectors, c.parser, c.rel, src, imports),
	}
}
