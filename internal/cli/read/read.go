// Package read implements the CLI read commands: history, recent, search,
// diff, and changes. They are the shell twins of the MCP read tools.
package read

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"os"

	"github.com/cjrdz/githints/internal/brief"
	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/llm"
	"github.com/cjrdz/githints/internal/mcpserver"
	"github.com/cjrdz/githints/internal/recorder"
)

// maxCLILimit matches the MCP tools' limit ceiling.
const maxCLILimit = 500

func checkLimit(n int) error {
	if n < 1 || n > maxCLILimit {
		return fmt.Errorf("-limit must be between 1 and %d, got %d", maxCLILimit, n)
	}
	return nil
}

// History shows the recorded history for one file.
func History(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	file := fs.String("file", "", "repo-relative path (required)")
	limit := fs.Int("limit", 20, "max entries (1-500)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("-file is required")
	}
	if err := recorder.ValidateFilePath(*file); err != nil {
		return err
	}
	if err := checkLimit(*limit); err != nil {
		return err
	}
	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.FileHistory(*file, *limit)
	if err != nil {
		return err
	}
	core.PrintChanges(changes, "no recorded history for "+*file, true)
	return nil
}

// Recent shows the latest changes across the repo.
func Recent(args []string) error {
	fs := flag.NewFlagSet("recent", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max entries (1-500)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkLimit(*limit); err != nil {
		return err
	}
	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.RecentChanges(*limit)
	if err != nil {
		return err
	}
	core.PrintChanges(changes, "no changes recorded yet", false)
	return nil
}

// Search runs a full-text search over summaries and reasons.
func Search(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	query := fs.String("query", "", "FTS5 MATCH expression (required)")
	limit := fs.Int("limit", 20, "max entries (1-500)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*query) == "" {
		return errors.New("-query is required")
	}
	if len(*query) > mcpserver.MaxSearchQueryLen {
		return fmt.Errorf("query too long: %d bytes (max %d)", len(*query), mcpserver.MaxSearchQueryLen)
	}
	if err := checkLimit(*limit); err != nil {
		return err
	}
	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.Search(*query, *limit)
	if err != nil {
		return fmt.Errorf("search (FTS5 syntax): %w", err)
	}
	core.PrintChanges(changes, "no matches", true)
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
	fmt.Println(mcpserver.TruncateDiff(llm.ScrubDiff(diff)))
	return nil
}

// Changes queries the change log over a recorded_at time range. It accepts
// RFC3339 timestamps or Unix seconds for -since and -until.
func Changes(args []string) error {
	fs := flag.NewFlagSet("changes", flag.ExitOnError)
	sinceStr := fs.String("since", "", "start timestamp (RFC3339 or Unix seconds, required)")
	untilStr := fs.String("until", "", "end timestamp (RFC3339 or Unix seconds, required)")
	file := fs.String("file", "", "restrict to one repo-relative file (optional)")
	limit := fs.Int("limit", 50, "max entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sinceStr == "" || *untilStr == "" {
		return fmt.Errorf("-since and -until are required")
	}
	if *file != "" {
		if err := recorder.ValidateFilePath(*file); err != nil {
			return err
		}
	}

	_, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	since, err := core.ParseTimestamp(*sinceStr)
	if err != nil {
		return fmt.Errorf("since: %w", err)
	}
	until, err := core.ParseTimestamp(*untilStr)
	if err != nil {
		return fmt.Errorf("until: %w", err)
	}

	// A negative LIMIT means "unbounded" in SQLite, which is not what
	// -limit=-1 looks like it should do.
	if *limit <= 0 {
		return fmt.Errorf("-limit must be positive, got %d", *limit)
	}

	changes, err := st.ChangesInRange(since, until, *file, *limit)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	core.PrintChanges(changes, "no changes in range", false)
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

	// The index is optional, and opening one that does not exist would create
	// it, so only an existing file is opened.
	var idx, notesDB *index.Store
	if fi, err := os.Lstat(lang.IndexDBPath(root)); err == nil && fi.Mode().IsRegular() {
		if db, err := index.Open(lang.IndexDBPath(root)); err == nil {
			defer db.Close()
			notesDB = db // the notes' tables are here whether or not indexing is on
			if cfg, err := config.Load(root); err == nil && cfg.Index.Enabled {
				idx = db
			}
		}
	}

	out, err := brief.Build(st, idx, brief.Options{BudgetTokens: *budget, Root: root, Notes: notesDB})
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}
