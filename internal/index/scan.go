package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/safefs"
)

// FullScan walks the repository under opts.Root, parses every supported file,
// and writes the result to db. It is idempotent: it clears the existing index
// first, then re-parses every file.
//
// If force is false, the scan refuses to overwrite a larger existing index with
// a smaller one (partial-write guard) and refuses to write data that would exceed
// maxBytes. Use force to override either guard.
func FullScan(db *Store, opts lang.ScanOptions, force bool, maxBytes int) error {
	// Strict on purpose. The user ran `githints index`, so an unsupported
	// language is worth stopping for, and the error names the supported set.
	// IncrementalScan is deliberately lenient; see openScan.
	sess, err := openScan(opts, true)
	if err != nil {
		return err
	}
	defer sess.close()
	registry, extMap := sess.registry, sess.files

	meta := lang.IndexMeta{LanguageCounts: make(map[string]int)}
	// Files of a language the binary supports but index.languages leaves out.
	// Without naming them, a repository in a language nobody enabled reported
	// "indexed 0 files" and "unsupported files: N", and nothing said why.
	disabled := make(map[string]int)

	var candidates []candidate

	// Every ignored directory and file, decided up front by one git process
	// per pass (scanIgnores). One process per directory was two thirds of a
	// full scan (524 ms of 790 on this repository), and one batch per depth
	// level after that still four times what this costs.
	ignores := scanIgnores(opts.Root)
	ignoredDir := ignores.dirs

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
			if ignoredDir[rel] {
				return filepath.SkipDir
			}
			return nil
		}

		// The walk sees every manifest the dependency gates will ask about;
		// recording them here spares a listing of each directory later.
		if d.Type().IsRegular() && lang.IsManifest(rel) {
			sess.manifests.Seen(rel)
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
			if p := registry.ForPath(rel); p != nil {
				// The binary could have parsed it; config left it out.
				// Counted before the per-file ignore check, so an ignored
				// file inside a tracked directory counts too: the message
				// below says "about".
				disabled[p.Language()]++
			}
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
	sess.manifests.MarkWalked()

	// When the listing failed, one pair of git invocations for every
	// candidate, rather than one pair each.
	ignored := ignores.files
	if ignored == nil {
		rels := make([]string, 0, len(candidates))
		for _, c := range candidates {
			rels = append(rels, c.rel)
		}
		ignored = resolveIgnored(opts.Root, rels)
	}

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
	results := sess.parse(work)

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

	// Before the write guards: when a guard refuses an empty result, this is
	// the likeliest explanation for it.
	for _, name := range lang.SortedKeys(disabled) {
		fmt.Fprintf(os.Stderr,
			"githints: index: about %d %s file(s) not indexed: %q is not in index.languages (use \"auto\" for every supported language)\n",
			disabled[name], name, name)
	}

	if err := guardWrite(db, allSymbols, allImports, allFacets, force, maxBytes); err != nil {
		return err
	}

	// The notes render from the rows in memory while SQLite stores the same
	// rows: the two touch different files and neither waits on the other.
	// Only INDEX.md, which reads counts and hub rankings back from the
	// database, waits for both.
	w, err := safefs.NewWriter(githintsDir(opts.Root), safefs.StateDirPerm, 0o644)
	if err != nil {
		return fmt.Errorf("open .githints: %w", err)
	}
	defer w.Close()
	// The import keys are computed here too, and handed to the database
	// write once its rows are in: file_keys is what lets the hook look a key
	// up rather than recompute every file's.
	var keys importKeys
	var renderErr error
	keysReady := make(chan struct{})
	rendering := make(chan struct{})
	go func() {
		defer close(rendering)
		notes, files := memoryNotes(allSymbols, allImports, allFacets)
		keys = resolveImportPathsFor(files, opts.Root, registry)
		close(keysReady)
		renderErr = renderFileNotes(w, notes, files, opts.Root, keys)
	}()

	meta.LastIndexedAt = time.Now().Unix()
	dbErr := writeFullIndex(db, rebuildRows{
		symbols:   allSymbols,
		imports:   allImports,
		facets:    allFacets,
		meta:      meta,
		languages: lang.ParserNames(sess.parsers),
		keys: func() map[string]string {
			<-keysReady
			return keys.ofFile
		},
	})
	<-rendering
	// A failed database write leaves notes rendered from rows the database
	// did not keep. They are a derived cache, `index verify` reports the
	// drift, and the next scan rewrites both.
	if dbErr != nil {
		return dbErr
	}
	if renderErr != nil {
		return renderErr
	}
	if err := renderIndexRollup(w, db, opts.Root, keys); err != nil {
		return fmt.Errorf("render index rollup: %w", err)
	}
	return nil
}

// incrementalKeys returns the import keys the hook renders with.
//
// Normally the changed files' keys are stored and every other key is looked
// up in file_keys. Recomputing every indexed file's key on each commit cost
// time in proportion to the repository, not the commit (60 ms of a 250 ms
// hook on Qt's source). All keys are recomputed, and stored, when the stored
// set is incomplete (an index from before file_keys, or one a failed write
// left behind) or when the commit changed a file keys derive from, such as a
// go.mod or Cargo.toml.
func incrementalKeys(db *Store, root string, registry *lang.Registry, paths, withRows []string) keyLookup {
	recompute := !db.keysComplete()
	for _, p := range paths {
		if registry.AffectsImportKeys(p) {
			recompute = true
			break
		}
	}
	if recompute {
		keys := resolveImportPaths(db, root, registry)
		if err := db.replaceKeys(keys.ofFile); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index: store import keys: %v\n", err)
		}
		return keys
	}
	changed := make(map[string]string, len(withRows))
	for _, f := range withRows {
		if key, err := registry.ImportPath(root, f); err == nil {
			changed[f] = key
		}
	}
	if err := db.setKeys(changed); err != nil {
		// The stored set is now behind: mark it so the next run recomputes,
		// and render this one from a full computation.
		fmt.Fprintf(os.Stderr, "githints: index: store import keys: %v\n", err)
		if err := metaSetOn(db.db, metaKeysComplete, ""); err != nil {
			// The database refused two writes in a row; `githints index
			// verify` reports the stale keys and a full index repairs them.
			fmt.Fprintf(os.Stderr, "githints: index: mark import keys stale: %v\n", err)
		}
		return resolveImportPaths(db, root, registry)
	}
	return storedKeys{db}
}

// writeFullIndex replaces the index's rows and metadata with a full scan's,
// in one transaction, then returns freed pages to the filesystem.
func writeFullIndex(db *Store, rows rebuildRows) error {
	if err := db.rebuild(rows); err != nil {
		return err
	}
	if err := db.ReclaimSpace(); err != nil {
		// Vacuum is purely an optimization; log and continue.
		fmt.Fprintf(os.Stderr, "githints: index vacuum: %v\n", err)
	}
	return nil
}

// Row size estimates. These are approximations of what SQLite will store,
// deliberately on the generous side: a cap that is reached slightly early is a
// far better failure than one reached slightly late.
const (
	symbolOverhead = 64
	importOverhead = 32
	facetOverhead  = 48
)

// estimateRowBytes approximates what a batch of rows will cost on disk.
//
// Shared by both scan paths so they cannot drift into disagreeing about what
// the cap means.
func estimateRowBytes(symbols []lang.Symbol, imports []lang.Import, facets []lang.Detected) int64 {
	var n int64
	for _, sym := range symbols {
		n += int64(len(sym.Name) + len(string(sym.Kind)) + len(sym.FilePath) +
			len(sym.Signature) + len(sym.Language) + symbolOverhead)
	}
	for _, imp := range imports {
		n += int64(len(imp.FilePath) + len(imp.ImportedPath) + importOverhead)
	}
	for _, f := range facets {
		n += int64(len(f.FilePath) + len(f.Facet) + len(f.Framework) +
			len(f.Name) + len(f.Detail) + facetOverhead)
	}
	return n
}

// currentIndexBytes is the index's size on disk, or 0 when it cannot be
// determined. Treating an unknown size as zero lets a scan proceed rather than
// refusing on a stat failure.
func currentIndexBytes(db *Store) int64 {
	if size := db.Size(); size > 0 {
		return size
	}
	return 0
}

// guardWrite enforces the safety checks before a full rebuild mutates the
// index: that the scan is not a partial write (fewer rows than the existing
// index), and that the result would not exceed the configured size cap.
func guardWrite(db *Store, symbols []lang.Symbol, imports []lang.Import, facets []lang.Detected, force bool, maxBytes int) error {
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

	if maxBytes > 0 && !force {
		estimated := currentIndexBytes(db) + estimateRowBytes(symbols, imports, facets)
		if estimated > int64(maxBytes) {
			return fmt.Errorf("index would exceed max_bytes (%d): estimated %d bytes; use --force to overwrite", maxBytes, estimated)
		}
	}

	return nil
}

// incrementalBudget is the number of bytes an incremental scan may still add
// before reaching the cap, or -1 when no cap is configured.
//
// FullScan has always enforced max_bytes; IncrementalScan took no cap at all,
// so the hook path could grow the index past the configured limit
// indefinitely, one commit at a time -- the one way an index actually gets
// large in practice.
func incrementalBudget(db *Store, maxBytes int) (int64, error) {
	if maxBytes <= 0 {
		return -1, nil
	}
	remaining := int64(maxBytes) - currentIndexBytes(db)
	if remaining <= 0 {
		return 0, fmt.Errorf("index is at or over max_bytes (%d); not adding to it. "+
			"Raise index.max_bytes, or rebuild with `githints index --force`", maxBytes)
	}
	return remaining, nil
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
	gitIgnoreCalls.Add(1)
	var args []string
	if excludesFile != "" {
		args = append(args, "-c", "core.excludesFile="+excludesFile)
	}
	args = append(args, "check-ignore", "--no-index", "--stdin")
	out, err := gitutil.RunIn(context.Background(), root, strings.NewReader(rel+"\n"), gitutil.MaxOutputBytes, args...)
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
func IncrementalScan(db *Store, opts lang.ScanOptions, paths []string, maxBytes int) error {
	if v, err := db.IndexResolverVersion(); err == nil && v < ResolverVersion {
		if n, err := db.FileCount(); err == nil && n > 0 {
			fmt.Fprintln(os.Stderr, "githints: the index was built by an older githints whose import resolution differs; run `githints index` once to rebuild it")
		}
	}
	// Lenient on purpose: see openScan.
	sess, err := openScan(opts, false)
	if err != nil {
		return err
	}
	defer sess.close()
	registry, extMap := sess.registry, sess.files
	// A language enabled since the last full scan reaches the index only one
	// committed file at a time. Say so, as for the resolver version above.
	if why, err := db.LanguagesStaleness(lang.ParserNames(sess.parsers)); err == nil && why != "" {
		fmt.Fprintf(os.Stderr,
			"githints: index: the index was %s; run `githints index` once so existing files of every enabled language are indexed\n", why)
	}

	// Files whose rows were written and which therefore need a note, and
	// those of them that hold any rows, which are the files with a stored key.
	var rendered, withRows []string

	budget, err := incrementalBudget(db, maxBytes)
	if err != nil {
		return err
	}

	repoRoot := sess.repo

	// A manifest names the frameworks every file below it is detected with,
	// and this scan re-detects only the files the commit touched.
	for _, path := range paths {
		if lang.IsManifest(path) {
			fmt.Fprintf(os.Stderr,
				"githints: index: %s changed; run `githints index` to re-detect frameworks in files this commit did not touch\n", path)
			break
		}
	}

	// One batched ignore check for the commit's paths instead of up to two
	// git processes per path; only valid paths are asked about.
	var askIgnore []string
	for _, path := range paths {
		if recorder.ValidateFilePath(path) == nil && path != ".githints" && !strings.HasPrefix(path, ".githints/") {
			askIgnore = append(askIgnore, path)
		}
	}
	ignoredPath := resolveIgnored(opts.Root, askIgnore)

	// The files to parse, in commit order. Deleted and unsupported files are
	// handled on the way.
	var work []candidate
	for _, path := range paths {
		if err := recorder.ValidateFilePath(path); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index skipped (invalid path): %s: %v\n", path, err)
			continue
		}
		if path == ".githints" || strings.HasPrefix(path, ".githints/") {
			continue
		}
		abs := filepath.Join(opts.Root, path)
		// Lstat through an os.Root, never os.Stat: Stat follows the link, so
		// shouldSkipFile never saw a symlink here and a committed
		// leak.py -> ~/.aws/credentials was read into the index. The root
		// also refuses a path whose parent directory is a link out of the repo.
		info, err := repoRoot.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Deleted file: remove its rows and its note.
				if err := db.DeleteFile(path); err != nil {
					fmt.Fprintf(os.Stderr, "githints: index delete failed: %s: %v\n", path, err)
				}
				if note, _, noteErr := lang.IndexNotePath(opts.Root, path); noteErr == nil {
					if err := removeManaged(opts.Root, note); err != nil {
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
		if ignoredPath[path] {
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
		work = append(work, candidate{rel: path, abs: abs, parser: parser})
	}

	// Parse and detect the commit's files across the worker pool, as the
	// full scan does; the results come back in commit order and are written
	// one file at a time, so the budget stops at the same file as before.
	results := sess.parse(work)
	for i, c := range work {
		path, r := c.rel, results[i]
		if r.warn != "" {
			// A file that cannot be read or parsed keeps its existing rows.
			fmt.Fprintln(os.Stderr, r.warn)
			continue
		}
		if err := db.DeleteFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index delete failed: %s: %v\n", path, err)
			continue
		}

		// Stop before crossing the cap rather than after. The remaining files
		// keep their existing rows, so the index stays consistent -- just no
		// longer complete, which the error says plainly.
		if budget >= 0 {
			budget -= estimateRowBytes(r.symbols, r.imports, r.facets)
			if budget < 0 {
				return fmt.Errorf("index reached max_bytes (%d) while indexing %s; "+
					"remaining files were not indexed. Raise index.max_bytes, "+
					"or rebuild with `githints index --force`", maxBytes, path)
			}
		}

		if err := db.InsertSymbols(r.symbols); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert symbols failed: %s: %v\n", path, err)
			continue
		}
		if err := db.InsertImports(r.imports); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert imports failed: %s: %v\n", path, err)
			continue
		}
		if err := db.InsertFacets(r.facets); err != nil {
			fmt.Fprintf(os.Stderr, "githints: index insert facets failed: %s: %v\n", path, err)
			continue
		}
		rendered = append(rendered, path)
		if len(r.symbols)+len(r.imports)+len(r.facets) > 0 {
			withRows = append(withRows, path)
		}
	}

	// After the write loop, so the keys include the files this scan just
	// wrote, or a new file would render without its own links.
	keys := incrementalKeys(db, opts.Root, registry, paths, withRows)
	w, err := safefs.NewWriter(githintsDir(opts.Root), safefs.StateDirPerm, 0o644)
	if err != nil {
		return fmt.Errorf("open .githints: %w", err)
	}
	defer w.Close()
	for _, path := range rendered {
		if err := renderFileNote(w, dbNotes{db}, opts.Root, path, keys); err != nil {
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
	if err := renderIndexRollup(w, db, opts.Root, keys); err != nil {
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
func detectFacets(set *lang.DetectorSet, parser lang.LanguageParser, rel string, src []byte, imports []lang.Import, manifests *lang.Manifests) []lang.Detected {
	if set.Empty() {
		return nil
	}
	bl, ok := parser.(lineBlanker)
	if !ok {
		return nil
	}
	return set.Detect(lang.DetectInput{
		FilePath:         rel,
		Language:         parser.Language(),
		Imports:          imports,
		LoadDependencies: func() []string { return manifests.DependenciesFor(rel) },
		LoadViews: func() ([]string, []string) {
			return bl.BlankLines(src), bl.BlankLinesKeepingStrings(src)
		},
	})
}

// scanSession is what every scan sets up the same way, whichever files it
// covers: the registry, the enabled parsers and the file map choosing among
// them, the framework detectors, each parser's per-scan state, the manifest
// cache, and one os.Root on the repository that every read goes through.
// FullScan and IncrementalScan differ in what they scan and how they write
// it; the per-file step, parse, is the same code for both.
type scanSession struct {
	opts      lang.ScanOptions
	registry  *lang.Registry
	parsers   []lang.LanguageParser
	files     *lang.FileMap
	detectors *lang.DetectorSet
	manifests *lang.Manifests
	repo      *os.Root
	endScans  func()
}

// scanGCPercent paces the garbage collector during a scan. A scan allocates
// heavily for a short time, and at the default of 100 the collector's mark
// work was about 12% of its CPU. Measured on this repository: 200 cut wall
// time 13% and CPU 17% for about 10 MB more peak memory; 400 bought almost
// nothing more for twice that memory. A GOGC the user set is left alone.
const scanGCPercent = 200

// scanMemoryLimit is a soft memory limit while a scan runs. With the
// collector paced at 200 a large scan kept three times its live heap: a full
// index of Qt's source peaked at 480 MB for a live heap of about 130 MB. Under
// this limit it peaked at 270 MB in the same wall time, for about 5% more CPU;
// a smaller repository never reaches it. The limit is soft (the runtime caps
// the collector's CPU rather than stalling), and a GOMEMLIMIT the user set is
// left alone, as is a lower limit already in force.
const scanMemoryLimit = 256 << 20

// scanGC sets scanGCPercent and scanMemoryLimit while any scan runs and
// restores the previous settings when the last one ends. They are
// process-wide, and the MCP server can run a scan while serving, so the
// scans are counted rather than toggled.
var scanGC struct {
	mu        sync.Mutex
	count     int
	prev      int
	setGC     bool
	prevLimit int64
	setLimit  bool
}

func beginScanGC() {
	scanGC.mu.Lock()
	defer scanGC.mu.Unlock()
	if scanGC.count == 0 {
		scanGC.setGC = os.Getenv("GOGC") == ""
		if scanGC.setGC {
			scanGC.prev = debug.SetGCPercent(scanGCPercent)
		}
		// SetMemoryLimit with a negative value only reads the limit.
		current := debug.SetMemoryLimit(-1)
		scanGC.setLimit = os.Getenv("GOMEMLIMIT") == "" && current > scanMemoryLimit
		if scanGC.setLimit {
			scanGC.prevLimit = debug.SetMemoryLimit(scanMemoryLimit)
		}
	}
	scanGC.count++
}

func endScanGC() {
	scanGC.mu.Lock()
	defer scanGC.mu.Unlock()
	if scanGC.count--; scanGC.count == 0 {
		if scanGC.setGC {
			debug.SetGCPercent(scanGC.prev)
		}
		if scanGC.setLimit {
			debug.SetMemoryLimit(scanGC.prevLimit)
		}
	}
}

// openScan prepares a scan. strict decides what an unsupported language in
// opts.Languages means: an error for work the user invoked directly (a full
// scan), or a warning for the post-commit hook, which only warns on a scan
// error -- a hard failure there would let the commit succeed while the index
// silently stopped updating for good.
func openScan(opts lang.ScanOptions, strict bool) (*scanSession, error) {
	registry := lang.NewRegistryForRoot(opts.Root)
	var parsers []lang.LanguageParser
	if strict {
		var err error
		if parsers, err = registry.ResolveLanguages(opts.Languages); err != nil {
			return nil, err
		}
	} else {
		var unknown []string
		var err error
		if parsers, unknown, err = registry.ResolveKnownLanguages(opts.Languages); err != nil {
			return nil, err
		}
		if len(unknown) > 0 {
			fmt.Fprintf(os.Stderr,
				"githints: index: ignoring unsupported language(s) %s; indexing the rest (supported: %s)\n",
				strings.Join(unknown, ", "), strings.Join(registry.Languages(), ", "))
		}
	}
	detectors, detectorErrs := lang.EmbeddedDetectors()
	for _, err := range detectorErrs {
		fmt.Fprintf(os.Stderr, "githints: framework detector ignored: %v\n", err)
	}
	repo, err := os.OpenRoot(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("open repo root: %w", err)
	}
	beginScanGC()
	return &scanSession{
		opts:      opts,
		registry:  registry,
		parsers:   parsers,
		files:     lang.ExtensionMap(lang.ParserSet(parsers)),
		detectors: detectors,
		manifests: lang.NewManifests(opts.Root),
		repo:      repo,
		// Each enabled parser installs whatever per-scan state it needs,
		// such as the TypeScript path aliases.
		endScans: lang.BeginScans(parsers, opts.Root),
	}, nil
}

func (sess *scanSession) close() {
	endScanGC()
	sess.endScans()
	_ = sess.manifests.Close() // a listing root; nothing to recover on error
	_ = sess.repo.Close()      // read-only; nothing to recover on error
}

// gitIgnoreCalls counts the git processes ignore checks start. A scan's cost
// is dominated by process starts, so TestScanCostModel pins how this grows:
// with the depth of the tree, never with the number of directories or files.
var gitIgnoreCalls atomic.Int64

// maxIgnoreLevels bounds the breadth-first directory listing. A tree deeper
// than this is listed no further for pruning; the per-file check still
// applies to everything found below.
const maxIgnoreLevels = 64

// ignoredDirs lists the repository breadth first and asks git about each
// depth level's directories in one batch (resolveIgnored: the same two
// passes, .gitignore then .githintsignore, and the same per-path fallback).
// An ignored directory is not listed further, so node_modules is never
// read. Links are not followed, as WalkDir does not follow them.
//
// The result is the set of ignored directory paths, which the walk prunes
// by lookup, keeping its order.
func ignoredDirs(root string) map[string]bool {
	ignored := map[string]bool{}
	frontier := []string{"."}
	for level := 0; level < maxIgnoreLevels && len(frontier) > 0; level++ {
		var children []string
		for _, dir := range frontier {
			entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				rel := e.Name()
				if dir != "." {
					rel = dir + "/" + rel
				}
				if rel == ".git" || rel == ".githints" {
					continue
				}
				children = append(children, rel)
			}
		}
		if len(children) == 0 {
			break
		}
		verdict := resolveIgnored(root, children)
		frontier = frontier[:0]
		for _, c := range children {
			if verdict[c] {
				ignored[c] = true
			} else {
				frontier = append(frontier, c)
			}
		}
	}
	return ignored
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

	// The two passes run as two concurrent git processes, not one after the
	// other: each is a blocked wait on a child process, which leaves its
	// processor to the other goroutine, so the pair costs about one. The
	// answer is the same as running them in order. .githintsignore only ever
	// adds ignores (its pass applies the repository's rules plus its own), so
	// a path the first pass ignores stays ignored, and for any other path the
	// second pass is asked exactly what it was asked before.
	hintsIgnore := filepath.Join(root, ".githintsignore")
	var extra ignoreSet
	var extraErr error
	var wg sync.WaitGroup
	if fileExists(hintsIgnore) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			extra, extraErr = batchCheckIgnore(root, rels, hintsIgnore)
		}()
	}
	ignored, err := batchCheckIgnore(root, rels, "")
	wg.Wait()
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: batched ignore check failed (%v); falling back to per-path\n", err)
		return perPathIgnored(root, rels)
	}
	if extraErr != nil {
		fmt.Fprintf(os.Stderr, "githints: batched .githintsignore check failed (%v); falling back to per-path\n", extraErr)
		return perPathIgnored(root, rels)
	}
	for rel := range extra {
		ignored[rel] = true
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
	gitIgnoreCalls.Add(1)

	args := []string{}
	if excludesFile != "" {
		args = append(args, "-c", "core.excludesFile="+excludesFile)
	}
	// -z on both sides: a path may legitimately contain a newline, and a
	// mis-split line would silently mark the wrong file ignored.
	args = append(args, "check-ignore", "--no-index", "--stdin", "-z")

	var stdin bytes.Buffer
	for _, rel := range rels {
		stdin.WriteString(rel)
		stdin.WriteByte(0)
	}

	// Through gitutil so the call has a timeout and a bounded buffer: it
	// used to buffer all of stdout and check the size afterwards.
	stdout, err := gitutil.RunIn(context.Background(), root, &stdin, gitutil.MaxOutputBytes, args...)
	// Exit status 1 means "nothing matched", which is a normal answer rather
	// than a failure.
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, err
		}
	}

	for _, p := range strings.Split(string(stdout), "\x00") {
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
func (sess *scanSession) parse(work []candidate) []fileResult {
	results := make([]fileResult, len(work))
	inParallel(len(work), min(runtime.NumCPU(), maxParseWorkers), func(i int) {
		results[i] = sess.parseOne(work[i])
	})
	return results
}

// inParallel runs fn(0) to fn(n-1) on at most workers goroutines. Each worker
// claims the next index with one atomic add: feeding them through an
// unbuffered channel cost a goroutine handoff per item, and measured on this
// repository's 398 files the claim-by-counter pool parsed 12% faster on 12%
// less CPU.
func inParallel(n, workers int, fn func(i int)) {
	if workers > n {
		workers = n
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// parseOne is the per-file work a pool worker does. It touches nothing shared
// but the session, whose parsers and detectors are immutable once built and
// whose per-scan state (parser hooks, manifests) is read under a lock.
func (sess *scanSession) parseOne(c candidate) fileResult {
	// Read through the session's os.Root with the size cap re-applied: the
	// walk or Lstat saw this path as a regular file, but it can be swapped
	// for a link before the read.
	src, err := safefs.ReadFileIn(sess.repo, c.rel, sess.opts.MaxFileSize)
	if err != nil {
		return fileResult{warn: fmt.Sprintf("githints: index skipped (read error): %s: %v", c.rel, err)}
	}

	symbols, imports, err := parseWithTimeout(c.parser, c.rel, src, sess.opts.ParseTimeout)
	if err != nil {
		return fileResult{warn: fmt.Sprintf("githints: index skipped (parse error): %s: %v", c.rel, err)}
	}

	return fileResult{
		symbols: symbols,
		imports: imports,
		facets:  detectFacets(sess.detectors, c.parser, c.rel, src, imports, sess.manifests),
	}
}
