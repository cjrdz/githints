package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/llm"
	"github.com/cjrdz/githints/internal/mcpserver"
	"github.com/cjrdz/githints/internal/recorder"
)

// The read commands below are the CLI twins of the MCP read tools, for repos
// whose agents run githints from the shell instead of over MCP. AGENTS.md
// documented them for a long time before any of them existed.

// maxCLILimit matches the MCP tools' limit ceiling.
const maxCLILimit = 500

func checkLimit(n int) error {
	if n < 1 || n > maxCLILimit {
		return fmt.Errorf("-limit must be between 1 and %d, got %d", maxCLILimit, n)
	}
	return nil
}

func cmdHistory(args []string) error {
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
	_, st, err := openInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.FileHistory(*file, *limit)
	if err != nil {
		return err
	}
	printChanges(changes, "no recorded history for "+*file, true)
	return nil
}

func cmdRecent(args []string) error {
	fs := flag.NewFlagSet("recent", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max entries (1-500)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkLimit(*limit); err != nil {
		return err
	}
	_, st, err := openInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.RecentChanges(*limit)
	if err != nil {
		return err
	}
	printChanges(changes, "no changes recorded yet", false)
	return nil
}

func cmdSearch(args []string) error {
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
	_, st, err := openInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	changes, err := st.Search(*query, *limit)
	if err != nil {
		return fmt.Errorf("search (FTS5 syntax): %w", err)
	}
	printChanges(changes, "no matches", true)
	return nil
}

// cmdDiff prints the redacted diff of one file. The output is what an agent
// reads, so it gets exactly what get_diff gives: scrubbed unconditionally,
// and truncated with an explicit marker.
func cmdDiff(args []string) error {
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
