// Package indexcmd implements the index CLI command and its subcommands:
// status, verify, languages, facets, and graph.
package indexcmd

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/graph"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Run re-parses the repository into a structural index stored in
// .githints/index.db, then writes .githints/index/<path>.md notes plus the
// .githints/INDEX.md rollup. It never touches the integrity-verified hint
// markdown or CHANGES.md.
func Run(args []string) error {
	if len(args) > 0 && args[0] == "status" {
		return Status(args[1:])
	}
	if len(args) > 0 && args[0] == "verify" {
		return Verify(args[1:])
	}
	if len(args) > 0 && args[0] == "languages" {
		return Languages(args[1:])
	}
	if len(args) > 0 && args[0] == "facets" {
		return Facets(args[1:])
	}
	if len(args) > 0 && args[0] == "graph" {
		return graph.Run(args[1:])
	}
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite the index even if a partial write is detected")
	obsidian := fs.Bool("obsidian", false, "render Obsidian wikilinks in index notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// flag.Parse stops at the first non-flag argument and leaves it in Args(),
	// so without this a mistyped subcommand ("index langauges") silently falls
	// through to a full rebuild — which clears the index and VACUUMs the file.
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unknown index subcommand: %s", rest[0])
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if !cfg.Index.Enabled {
		fmt.Println("indexing is disabled in .githints/config.json")
		return nil
	}

	dbPath := lang.IndexDBPath(root)
	db, err := index.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open index db: %w", err)
	}
	defer db.Close()

	if err := index.FullScan(db, lang.ScanOptions{
		Root:         root,
		Languages:    cfg.Index.Languages,
		MaxFileSize:  int64(cfg.Index.MaxFileSize),
		ParseTimeout: time.Duration(cfg.Index.ParseTimeoutMS) * time.Millisecond,
		Obsidian:     *obsidian || cfg.Index.ObsidianWikilinks,
	}, *force, cfg.Index.MaxBytes); err != nil {
		return fmt.Errorf("index: %w", err)
	}

	meta, err := db.Meta()
	if err != nil {
		return fmt.Errorf("index meta: %w", err)
	}
	fmt.Printf("indexed %d files, %d symbols\n", meta.FileCount, meta.SymbolCount)
	if cfg.Index.GraphHTML {
		graph.RefreshPage(root, db)
	}
	return nil
}

// Languages prints the languages this binary can index, with the file
// extensions each one claims.
//
// It deliberately needs no repository, no config, and no database: which
// languages exist is a property of the binary. That is the whole point — a
// user whose configured language was rejected has, without this, no way to ask
// what would have been accepted. When there *is* a repo to consult, each
// language is additionally marked with whether the current config enables it,
// which is the usual reason a file is not showing up in the index.
func Languages(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown index languages argument: %s", args[0])
	}

	// Best-effort: outside a repo, or with an unreadable config, the plain
	// list is still the correct answer to "what does this binary support".
	enabled := map[string]bool{}
	haveConfig := false
	root := ""
	if found, err := gitutil.RepoRoot(); err == nil {
		root = found
		if cfg, err := config.Load(root); err == nil {
			haveConfig = true
			for _, name := range cfg.Index.Languages {
				enabled[strings.ToLower(name)] = true
			}
		}
	}
	r := lang.NewRegistryForRoot(root)

	fmt.Println("languages this githints binary can index:")
	for _, name := range r.Languages() {
		p := r.ForLanguage(name)
		exts := append([]string(nil), p.Extensions()...)
		sort.Strings(exts)

		line := fmt.Sprintf("  %-12s %s", name, strings.Join(exts, " "))
		if r.Origin(name) == lang.OriginRepo {
			line += "  [from " + lang.UserSpecDir + "]"
		}
		if haveConfig {
			if enabled[name] {
				line += "  [enabled]"
			} else {
				line += "  [not enabled]"
			}
		}
		fmt.Println(line)
	}

	if haveConfig {
		// The set may have come from config.json, GITHINTS_INDEX_LANGUAGES, or
		// the built-in default, so name the levers rather than claiming a source.
		fmt.Println()
		fmt.Println(`change the enabled set with "index.languages" in .githints/config.json,`)
		fmt.Println(`or the GITHINTS_INDEX_LANGUAGES environment variable`)
	}
	return nil
}

// Facets lists detected framework constructs: routes, models, components and
// the rest, normalized across frameworks.
func Facets(args []string) error {
	fs := flag.NewFlagSet("index facets", flag.ExitOnError)
	facet := fs.String("facet", "", "role to list: route, model, component, migration, job, test")
	framework := fs.String("framework", "", "restrict to one framework, e.g. django")
	file := fs.String("file", "", "restrict to one repo-relative file path")
	limit := fs.Int("limit", 50, "maximum facets to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unknown argument: %s", rest[0])
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if !cfg.Index.Enabled {
		fmt.Println("indexing is disabled in .githints/config.json")
		return nil
	}

	db, err := index.Open(lang.IndexDBPath(root))
	if err != nil {
		return fmt.Errorf("open index db: %w", err)
	}
	defer db.Close()

	found, err := db.Facets(index.FacetFilter{
		Facet:     *facet,
		Framework: *framework,
		File:      *file,
		Limit:     clampCLILimit(*limit),
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		total, err := db.FacetCount()
		if err != nil {
			return err
		}
		if total == 0 {
			fmt.Println("no framework constructs detected in this repo")
		} else {
			fmt.Println("no facets match those filters")
		}
		return nil
	}
	for _, f := range found {
		line := fmt.Sprintf("%-10s %-12s %s:%d  %s", f.Facet, f.Framework, textsafe.OneLine(f.FilePath), f.Line, textsafe.OneLine(f.Name))
		if f.Detail != "" {
			line += "  -> " + textsafe.OneLine(f.Detail)
		}
		fmt.Println(line)
	}
	return nil
}

// clampCLILimit keeps a CLI limit in the same range the MCP tools enforce, so
// the two surfaces cannot disagree about what is allowed.
func clampCLILimit(n int) int {
	if n < 1 {
		return 1
	}
	if n > 500 {
		return 500
	}
	return n
}

// Status prints a quick dashboard of the structural index.
func Status(args []string) error {
	if len(args) > 0 && args[0] != "status" {
		return fmt.Errorf("unknown index subcommand: %s", args[0])
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if !cfg.Index.Enabled {
		fmt.Println("indexing is disabled in .githints/config.json")
		return nil
	}

	dbPath := lang.IndexDBPath(root)
	db, err := index.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open index db: %w", err)
	}
	defer db.Close()

	meta, err := db.Meta()
	if err != nil {
		return fmt.Errorf("index meta: %w", err)
	}

	files, err := db.FileCount()
	if err != nil {
		return err
	}
	symbols, err := db.SymbolCount()
	if err != nil {
		return err
	}
	imports, err := db.ImportCount()
	if err != nil {
		return err
	}

	fmt.Printf("index db: %s\n", dbPath)
	if dbSize := db.Size(); dbSize >= 0 {
		fmt.Printf("index db size: %d bytes\n", dbSize)
	}
	fmt.Printf("files indexed: %d\n", files)
	fmt.Printf("symbols: %d\n", symbols)
	fmt.Printf("imports: %d\n", imports)
	if meta.LastIndexedAt > 0 {
		fmt.Printf("last indexed: %s\n", time.Unix(meta.LastIndexedAt, 0).Format(time.RFC3339))
	} else {
		fmt.Println("last indexed: never")
	}
	if len(meta.LanguageCounts) > 0 {
		fmt.Println("language breakdown:")
		for _, lang := range core.SortedStringKeys(meta.LanguageCounts) {
			fmt.Printf("  - %s: %d\n", lang, meta.LanguageCounts[lang])
		}
	}
	if facets, err := db.FacetBreakdown(); err == nil && len(facets) > 0 {
		fmt.Println("framework facets:")
		for _, f := range facets {
			fmt.Printf("  - %s (%s): %d\n", f.Facet, f.Framework, f.Count)
		}
	}
	if meta.SkippedCount > 0 {
		fmt.Printf("skipped files: %d\n", meta.SkippedCount)
	}
	if meta.UnsupportedCount > 0 {
		fmt.Printf("unsupported files: %d\n", meta.UnsupportedCount)
	}
	return nil
}

// Verify reports drift between the index, the rendered notes, and the tracked
// source files: stale notes, ghost rows, and covered-language files with no
// index rows (with reasons). It exits non-zero on real drift (stale notes or
// ghosts); uncovered files are informational.
func Verify(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown index subcommand: %s", args[0])
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	if !cfg.Index.Enabled {
		fmt.Println("indexing is disabled in .githints/config.json")
		return nil
	}

	db, err := index.Open(lang.IndexDBPath(root))
	if err != nil {
		return fmt.Errorf("open index db: %w", err)
	}
	defer db.Close()

	report, err := index.VerifyIndex(db, lang.ScanOptions{
		Root:         root,
		Languages:    cfg.Index.Languages,
		MaxFileSize:  int64(cfg.Index.MaxFileSize),
		ParseTimeout: time.Duration(cfg.Index.ParseTimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return err
	}

	fmt.Printf("notes on disk: %d\n", report.Notes)
	if len(report.StaleNotes) > 0 {
		fmt.Printf("stale notes (source not indexed): %d\n", len(report.StaleNotes))
		for _, s := range report.StaleNotes {
			fmt.Printf("  - %s\n", s)
		}
	}
	if len(report.GhostFiles) > 0 {
		fmt.Printf("ghost rows (file deleted from disk): %d\n", len(report.GhostFiles))
		for _, g := range report.GhostFiles {
			fmt.Printf("  - %s\n", g)
		}
	}
	if len(report.Uncovered) > 0 {
		fmt.Printf("tracked files not indexed: %d\n", len(report.Uncovered))
		for _, u := range report.Uncovered {
			fmt.Printf("  - %s (%s)\n", textsafe.OneLine(u.Path), u.Reason)
		}
	}
	if !report.Drift() && len(report.Uncovered) == 0 {
		fmt.Println("index is in sync with the repository")
	} else if !report.Drift() {
		fmt.Println("no drift (run githints index to refresh; uncovered files above are skipped by design)")
	}

	if report.Drift() {
		return fmt.Errorf("index drift detected; run githints index to repair")
	}
	return nil
}
