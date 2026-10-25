// Package read implements the CLI read commands: log, diff and brief, the
// shell twins of the MCP read tools.
package read

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/cjrdz/githints/internal/brief"
	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/secrets"
	"github.com/cjrdz/githints/internal/store"
)

// Log reads the change log, newest first: the twin of the query_changes
// tool. Its flags combine; with none it lists the latest changes. history,
// recent, search and changes are aliases of it, and their flags are its own.
func Log(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	file := fs.String("file", "", "one repo-relative file's history")
	query := fs.String("query", "", "FTS5 MATCH expression over summaries and reasons")
	sinceStr := fs.String("since", "", "start, inclusive (RFC3339, a date, or Unix seconds)")
	untilStr := fs.String("until", "", "end, inclusive")
	limit := fs.Int("limit", 20, "max entries (1-500)")
	line := fs.Int("line", 0, "with -file: why this line is there")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *line != 0 {
		if *file == "" || *query != "" || *sinceStr != "" || *untilStr != "" {
			return errors.New("-line needs -file, and does not combine with -query, -since or -until")
		}
		if err := recorder.ValidateFilePath(*file); err != nil {
			return err
		}
		_, st, err := core.OpenInitialized()
		if err != nil {
			return err
		}
		defer st.Close()
		text, err := st.ExplainLine(*file, *line)
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	}
	q := store.Query{File: *file, Text: strings.TrimSpace(*query), Limit: *limit}
	if q.File != "" {
		if err := recorder.ValidateFilePath(q.File); err != nil {
			return err
		}
	}
	if err := core.CheckLimit(q.Limit); err != nil {
		return err
	}
	var err error
	if *sinceStr != "" {
		if q.Since, err = store.ParseTimestamp(*sinceStr); err != nil {
			return fmt.Errorf("since: %w", err)
		}
	}
	if *untilStr != "" {
		if q.Until, err = store.ParseTimestamp(*untilStr); err != nil {
			return fmt.Errorf("until: %w", err)
		}
	}

	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.QueryChanges(q)
	if err != nil {
		if q.Text != "" {
			return fmt.Errorf("search (FTS5 syntax): %w", err)
		}
		return err
	}
	core.PrintChanges(changes, "no matching changes recorded", true)
	return nil
}

// Diff prints the redacted diff of one file. The output is what an agent
// reads, so it gets exactly what get_diff gives: scrubbed unconditionally,
// and truncated with an explicit marker.
func Diff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	file := fs.String("file", "", "repo-relative path (required)")
	hash := fs.String("hash", "", "hex commit-ish to diff within one commit (default: working tree vs HEAD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("-file is required")
	}
	if err := recorder.ValidateFilePath(*file); err != nil {
		return err
	}
	if _, err := gitutil.RepoRoot(); err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}
	// FileDiff validates hash with IsValidCommitish before it reaches argv.
	diff, err := gitutil.FileDiff(*hash, *file)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Println("no changes")
		return nil
	}
	fmt.Println(secrets.DisplayDiff(diff))
	return nil
}

// Brief prints the session-start digest: failed attempts, uncommitted work,
// recent commits, blast radius and co-changing files, within a token budget.
// It is the shell twin of the digest get_session_context returns.
func Brief(args []string) error {
	fs := flag.NewFlagSet("brief", flag.ExitOnError)
	budget := fs.Int("budget", brief.DefaultBudgetTokens,
		fmt.Sprintf("approximate token budget (%d-%d)", brief.MinBudgetTokens, brief.MaxBudgetTokens))
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	// The index is optional.
	var idx, notesDB *index.Store
	if db := core.OpenExistingIndex(root); db != nil {
		defer db.Close()
		notesDB = db // the notes' tables are here whether or not indexing is on
		if cfg, err := config.Load(root); err == nil && cfg.Index.Enabled {
			idx = db
		}
	}

	out, err := brief.Build(st, idx, brief.Options{BudgetTokens: *budget, Root: root, Notes: notesDB})
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}
