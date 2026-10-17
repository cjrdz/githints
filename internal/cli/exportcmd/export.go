// Package exportcmd implements `githints export`.
package exportcmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/export"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
)

// Run writes an export pack: the change log as JSON Lines and CSV, a full
// changelog, per-file hints, the notes, and a manifest with a hash of every
// file and the integrity status at the time.
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	out := fs.String("o", "", "output directory (required; must not exist or be empty)")
	duck := fs.Bool("duckdb", false, "also write githints.duckdb.sql, a script that loads the pack into DuckDB")
	noNotes := fs.Bool("no-notes", false, "leave the notes out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q", rest[0])
	}
	if *out == "" {
		return errors.New("-o DIR is required")
	}

	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	var db *index.Store
	if !*noNotes {
		if db, err = index.Open(lang.IndexDBPath(root)); err != nil {
			return fmt.Errorf("open notes index: %w", err)
		}
		defer db.Close()
	}

	res, err := export.Run(root, st, db, export.Options{Dir: *out, DuckDB: *duck, NoNotes: *noNotes, Version: version})
	if err != nil {
		return err
	}
	m := res.Manifest
	fmt.Printf("exported to %s\n", res.Dir)
	var keys []string
	for k := range m.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s: %d\n", k, m.Counts[k])
	}
	fmt.Printf("  files written: %d (manifest.json lists each with its sha256)\n", len(m.Files))
	fmt.Printf("verify at export time: chain %s, anchors %s\n", m.Verify.Chain, m.Verify.Anchors)
	for _, s := range m.Skipped {
		fmt.Fprintf(os.Stderr, "githints: skipped: %s\n", s)
	}
	if !m.Verify.OK() {
		fmt.Fprintln(os.Stderr, "githints: WARNING: the log did not fully verify; see verify in manifest.json and run `githints verify`")
	}
	return nil
}
