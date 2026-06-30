package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"githints/internal/gitutil"
	"githints/internal/hint"
	"githints/internal/mcpserver"
	"githints/internal/recorder"
	"githints/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		mustRun(cmdInit)
	case "serve":
		mustRun(cmdServe)
	case "hook-run":
		mustRun(cmdHookRun)
	case "record":
		mustRun(func() error { return cmdRecord(os.Args[2:]) })
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `githints — lightweight change tracking for AI coding agents

Usage:
  githints init                 set up .githints/ + install the git hook
  githints serve                run the MCP stdio server
  githints hook-run              (internal) called by .git/hooks/post-commit
  githints record -file=... -summary=... [-reason=...]
                                 manually record a change (useful for testing)`)
}

func mustRun(fn func() error) {
	if err := fn(); err != nil {
		fmt.Fprintln(os.Stderr, "githints: "+err.Error())
		os.Exit(1)
	}
}

func openRootAndStore() (root string, st *store.Store, err error) {
	root, err = gitutil.RepoRoot()
	if err != nil {
		return "", nil, fmt.Errorf("not inside a git repo: %w", err)
	}
	st, err = store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		return "", nil, err
	}
	return root, st, nil
}

// cmdInit creates .githints/, the store, and installs the post-commit hook.
func cmdInit() error {
	root, st, err := openRootAndStore()
	if err != nil {
		// repo root resolves fine even before .githints/ exists; the only
		// failure case here is "not a git repo", which openRootAndStore
		// already reports clearly.
		return err
	}
	defer st.Close()

	if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own binary path: %w", err)
	}
	hookPath := filepath.Join(root, ".git", "hooks", "post-commit")
	hookScript := fmt.Sprintf("#!/bin/sh\n# installed by `githints init` — do not edit by hand\nexec %q hook-run\n", exe)
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		return fmt.Errorf("write post-commit hook: %w", err)
	}

	fmt.Printf("githints initialized at %s/.githints\nhook installed at %s\n", root, hookPath)
	return nil
}

// cmdServe runs the MCP stdio server. Assumes `githints init` already ran.
func cmdServe() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return mcpserver.Run(root, st)
}

// agentCoversCommit reports whether the agent rows for a file account for
// the entire diff in the commit. If every agent row has a diff stat and
// their sum equals the commit's diff stat, the agent covered everything.
// If any agent row is missing a diff stat (first commit, untracked file,
// binary file), we give the agent the benefit of the doubt to avoid a
// duplicate fallback entry.
func agentCoversCommit(agentRows []store.Change, hash, file string) bool {
	var agentAdd, agentDel int
	haveAllStats := true
	for _, r := range agentRows {
		a, d, ok := gitutil.ParseDiffStat(r.DiffStat)
		if !ok {
			haveAllStats = false
			break
		}
		agentAdd += a
		agentDel += d
	}
	if !haveAllStats {
		return true
	}

	commitStat := gitutil.DiffStat(hash, file)
	commitAdd, commitDel, ok := gitutil.ParseDiffStat(commitStat)
	if !ok {
		return false // can't compare, let the hook record a fallback
	}
	return agentAdd == commitAdd && agentDel == commitDel
}

// cmdHookRun is invoked by .git/hooks/post-commit. It records a low-detail
// fallback entry only for files the agent didn't already record via
// record_change for this exact commit.
func cmdHookRun() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	hash := gitutil.LastCommitHash()
	if hash == "" {
		return fmt.Errorf("could not resolve HEAD")
	}

	files, err := gitutil.ChangedFiles(hash)
	if err != nil {
		return fmt.Errorf("list changed files: %w", err)
	}

	rendered := false
	for _, f := range files {
		if f == "" || strings.HasPrefix(f, ".githints/") {
			continue // never track changes to githints' own output
		}

		// If an agent called record_change for this file before the commit
		// existed, its row is "pending" (commit_hash ""). Claim it now so
		// the check below recognizes it and we don't write a duplicate.
		claimed, err := st.ClaimPending(f, hash)
		if err != nil {
			return fmt.Errorf("claim pending for %s: %w", f, err)
		}
		if claimed > 0 {
			// Re-render the per-file hint so the "uncommitted" label is
			// replaced with the real commit hash we just stamped.
			if err := hint.RenderFile(st, root, f, recorder.HistoryLimitPerFile); err != nil {
				return fmt.Errorf("render file hint for %s: %w", f, err)
			}
			rendered = true
		}

		agentRows, err := st.AgentRecordsForCommit(f, hash)
		if err != nil {
			return err
		}

		needsFallback := true
		if len(agentRows) > 0 {
			needsFallback = !agentCoversCommit(agentRows, hash, f)
		}
		if !needsFallback {
			continue // agent fully covered this file for this commit
		}

		diffStat := gitutil.DiffStat(hash, f)
		err = recorder.Record(st, root, recorder.Input{
			FilePath:   f,
			Summary:    "Change detected by git hook (no AI summary recorded for this commit).",
			Source:     "hook",
			DiffStat:   diffStat,
			CommitHash: hash,
		})
		if err != nil {
			return fmt.Errorf("record %s: %w", f, err)
		}
		rendered = true
	}

	// Make sure the root changelog reflects any claimed or newly recorded rows.
	if rendered {
		if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
			return fmt.Errorf("render changelog: %w", err)
		}
	}
	return nil
}

// cmdRecord lets you (or a script) write a change row from the shell,
// without going through the MCP tool. Mirrors record_change exactly.
func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	file := fs.String("file", "", "repo-relative path to the changed file (required)")
	summary := fs.String("summary", "", "one to two sentence summary (required)")
	reason := fs.String("reason", "", "why the change was made (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *summary == "" {
		return fmt.Errorf("-file and -summary are required")
	}

	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return recorder.Record(st, root, recorder.Input{
		FilePath: *file,
		Summary:  *summary,
		Reason:   *reason,
		Source:   "agent",
	})
}
