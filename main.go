package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"githints/internal/gitutil"
	"githints/internal/hint"
	"githints/internal/integrity"
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
		mustRun(func() error { return cmdInit(os.Args[2:]) })
	case "serve":
		mustRun(cmdServe)
	case "hook-run":
		mustRun(cmdHookRun)
	case "hook-precommit":
		mustRun(cmdPreCommit)
	case "record":
		mustRun(func() error { return cmdRecord(os.Args[2:]) })
	case "verify":
		mustRun(cmdVerify)
	case "changes":
		mustRun(func() error { return cmdChanges(os.Args[2:]) })
	case "rotate-salt":
		mustRun(func() error { return cmdRotateSalt(os.Args[2:]) })
	case "status":
		mustRun(cmdStatus)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `githints — lightweight change tracking for AI coding agents

Usage:
  githints init [-force]         set up .githints/ + install the git hooks
  githints serve                 run the MCP stdio server
  githints hook-run              (internal) called by .git/hooks/post-commit
  githints hook-precommit        (internal) called by .git/hooks/pre-commit
  githints record -file=... -summary=... [-reason=...] [-agent-id=...]
                                 manually record a change (useful for testing)
  githints verify                check HMAC chain + markdown consistency
  githints changes -since=... -until=... [-file=...] [-limit=...]
                                 timeline forensics query
  githints rotate-salt [-force]  generate a new integrity salt and re-sign the chain
  githints status                show store health and pending records`)
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

// managedHookMarker is the sentinel comment githints writes into hooks it
// installs. It lets re-init update its own hooks while refusing to clobber
// user-managed hooks unless --force is passed.
const managedHookMarker = "installed by `githints init`"

// hookExistsAndManaged reports whether path is an existing file that was
// written by githints (and may safely be re-initialized).
func hookExistsAndManaged(path string) (exists bool, managed bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	if info.IsDir() {
		return true, false, fmt.Errorf("%s is a directory, not a hook", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true, false, err
	}
	return true, strings.Contains(string(data), managedHookMarker), nil
}

// cmdInit creates .githints/, the store, and installs the git hooks.
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing git hooks even if not managed by githints")
	if err := fs.Parse(args); err != nil {
		return err
	}

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

	if _, err := integrity.LoadOrCreateSalt(root); err != nil {
		return fmt.Errorf("integrity salt: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own binary path: %w", err)
	}

	// Install both hooks from one template so the post-commit reactive
	// recorder and the pre-commit proactive gate stay in sync.
	hookScript := func(cmd string) string {
		return fmt.Sprintf("#!/bin/sh\n# %s — do not edit by hand\nexec %q %s\n", managedHookMarker, exe, cmd)
	}

	postCommit := filepath.Join(root, ".git", "hooks", "post-commit")
	preCommit := filepath.Join(root, ".git", "hooks", "pre-commit")

	for _, path := range []string{postCommit, preCommit} {
		exists, managed, err := hookExistsAndManaged(path)
		if err != nil {
			return err
		}
		if exists && !managed && !*force {
			return fmt.Errorf("%s already exists and was not installed by githints; use -force to overwrite", path)
		}
	}

	if err := os.WriteFile(postCommit, []byte(hookScript("hook-run")), 0o755); err != nil {
		return fmt.Errorf("write post-commit hook: %w", err)
	}
	if err := os.WriteFile(preCommit, []byte(hookScript("hook-precommit")), 0o755); err != nil {
		return fmt.Errorf("write pre-commit hook: %w", err)
	}

	fmt.Printf("githints initialized at %s/.githints\nhooks installed at %s, %s\n", root, postCommit, preCommit)
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
// the entire diff in the commit. It returns two booleans:
//   - covered: true if the agent rows' diff stats sum to the commit's diff
//     stat, meaning no hook fallback is needed.
//   - verifiable: false when any agent row lacks a diff stat or the commit's
//     diff stat cannot be parsed. In that case we cannot prove coverage, so
//     the hook writes an "unverifiable" fallback rather than silently giving
//     the agent the benefit of the doubt.
func agentCoversCommit(agentRows []store.Change, commitStat string) (covered bool, verifiable bool) {
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

	commitAdd, commitDel, ok := gitutil.ParseDiffStat(commitStat)
	if !ok {
		// Binary, first commit, or otherwise unmeasurable: we can't compare.
		return false, false
	}
	if !haveAllStats {
		return false, false
	}
	return agentAdd == commitAdd && agentDel == commitDel, true
}

// hookSummaryFor decides what text the post-commit hook should record when
// it has to write a fallback entry.
func hookSummaryFor(verifiable bool) string {
	if verifiable {
		return "Change detected by git hook (no AI summary recorded for this commit)."
	}
	return "Change detected by git hook; agent coverage could not be verified (binary, first commit, or missing diff stat)."
}

// cmdHookRun is invoked by .git/hooks/post-commit. It records a low-detail
// fallback entry only for files the agent didn't already record via
// record_change for this exact commit. Each file's claim-check-insert
// sequence runs inside a transaction so a concurrent record_change cannot
// slip between ClaimPending and the fallback decision.
func cmdHookRun() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	hash, err := gitutil.LastCommitHash()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	if hash == "" {
		return fmt.Errorf("could not resolve HEAD (no commits yet?)")
	}

	files, err := gitutil.ChangedFiles(hash)
	if err != nil {
		return fmt.Errorf("list changed files: %w", err)
	}

	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		return fmt.Errorf("integrity key: %w", err)
	}

	rendered := false
	for _, f := range files {
		if f == "" || strings.HasPrefix(f, ".githints/") {
			continue // never track changes to githints' own output
		}

		// Run the DB operations for this file atomically so the view of
		// pending rows doesn't change mid-check.
		var claimed int64
		var agentRows []store.Change
		if err := st.WithTx(func(tx *sql.Tx) error {
			var err error
			claimed, err = store.ClaimPendingTx(tx, f, hash)
			if err != nil {
				return err
			}
			agentRows, err = store.AgentRecordsForCommitTx(tx, f, hash)
			return err
		}); err != nil {
			return fmt.Errorf("process %s: %w", f, err)
		}

		if claimed > 0 {
			// Re-render the per-file hint so the "uncommitted" label is
			// replaced with the real commit hash we just stamped.
			if err := hint.RenderFile(st, root, f, recorder.HistoryLimitPerFile); err != nil {
				return fmt.Errorf("render file hint for %s: %w", f, err)
			}
			rendered = true
		}

		needsFallback := true
		verifiable := false
		if len(agentRows) > 0 {
			var covered bool
			covered, verifiable = agentCoversCommit(agentRows, gitutil.DiffStat(hash, f))
			needsFallback = !covered
		}
		if !needsFallback {
			continue
		}

		diffStat := gitutil.DiffStat(hash, f)
		err = recorder.Record(st, root, key, recorder.Input{
			FilePath:   f,
			Summary:    hookSummaryFor(verifiable),
			Source:     "hook",
			DiffStat:   diffStat,
			CommitHash: hash,
		})
		if err != nil {
			return fmt.Errorf("record %s: %w", f, err)
		}
		rendered = true
	}

	if rendered {
		if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
			return fmt.Errorf("render changelog: %w", err)
		}
	}

	commitRows, err := st.ChangesForCommit(hash)
	if err != nil {
		return fmt.Errorf("load commit rows: %w", err)
	}
	if len(commitRows) > 0 {
		commitRoot := integrity.MerkleRoot(commitRows)
		note := fmt.Sprintf("githints-root: %s", commitRoot)
		if err := gitutil.AddNote("refs/notes/githints", note); err != nil {
			// Non-fatal: the store-level integrity is still valid; the note
			// is an optional external anchor.
			fmt.Fprintf(os.Stderr, "githints: could not add Merkle note: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "githints: anchored Merkle root %s for commit %s\n", commitRoot, shortCommitHash(hash))
		}
	}

	return nil
}

// cmdPreCommit is invoked by .git/hooks/pre-commit. For each staged file
// (excluding githints' own output) it checks whether the agent left a
// pending record_change for it; if not, the file is about to land without
// a "why" attached, so the hook warns. Set GITHINTS_PRECOMMIT_BLOCK=1 to
// turn the warning into a hard block that aborts the commit.
//
// The check is best-effort: a missing pending record is not proof of a
// problem (an agent may legitimately batch records after staging), so the
// default mode is advisory. The block mode is opt-in for teams that want
// to enforce the discipline.
func cmdPreCommit() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	staged, err := gitutil.StagedFiles()
	if err != nil {
		return fmt.Errorf("list staged files: %w", err)
	}

	var missing []string
	for _, f := range staged {
		if f == "" || strings.HasPrefix(f, ".githints/") {
			continue
		}
		has, err := st.HasPendingAgentRecord(f)
		if err != nil {
			return fmt.Errorf("check pending for %s: %w", f, err)
		}
		if !has {
			missing = append(missing, f)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	fmt.Fprintln(os.Stderr, "githints: staged files with no pending record_change (no 'why' will be attached):")
	for _, f := range missing {
		fmt.Fprintf(os.Stderr, "  - %s\n", f)
	}
	fmt.Fprintln(os.Stderr, "call record_change for each before committing, or set GITHINTS_PRECOMMIT_BLOCK=1 to enforce.")

	if os.Getenv("GITHINTS_PRECOMMIT_BLOCK") == "1" {
		return fmt.Errorf("pre-commit gate blocked commit of %d unrecorded file(s); set GITHINTS_PRECOMMIT_BLOCK=0 to warn only", len(missing))
	}
	_ = root // root reserved for future pre-commit rendering hooks
	return nil
}

// cmdRecord lets you (or a script) write a change row from the shell,
// without going through the MCP tool. Mirrors record_change exactly.
func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	file := fs.String("file", "", "repo-relative path to the changed file (required)")
	summary := fs.String("summary", "", "one to two sentence summary (required)")
	reason := fs.String("reason", "", "why the change was made (optional)")
	agentID := fs.String("agent-id", "", "agent/session fingerprint (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *summary == "" {
		return fmt.Errorf("-file and -summary are required")
	}
	if err := recorder.ValidateFilePath(*file); err != nil {
		return err
	}

	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		return fmt.Errorf("integrity key: %w", err)
	}

	return recorder.Record(st, root, key, recorder.Input{
		FilePath: *file,
		Summary:  *summary,
		Reason:   *reason,
		AgentID:  *agentID,
		Source:   "agent",
	})
}

// cmdStatus prints a quick health/dashboard view of the githints store.
func cmdStatus() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("store: %s\n", filepath.Join(root, ".githints", "store.db"))

	total, err := st.Count()
	if err != nil {
		return err
	}
	fmt.Printf("total changes: %d\n", total)

	pending, err := st.UncommittedCount()
	if err != nil {
		return err
	}
	fmt.Printf("uncommitted: %d\n", pending)

	if lastRecorded, _ := st.MetaGet("last_recorded_at"); lastRecorded != "" {
		if ts, err := strconv.ParseInt(lastRecorded, 10, 64); err == nil {
			fmt.Printf("last recorded: %s\n", time.Unix(ts, 0).Format(time.RFC3339))
		}
	}

	if lastVerify, _ := st.MetaGet("last_verify_at"); lastVerify != "" {
		if ts, err := strconv.ParseInt(lastVerify, 10, 64); err == nil {
			fmt.Printf("last verified: %s\n", time.Unix(ts, 0).Format(time.RFC3339))
		}
	}

	recent, err := st.RecentChanges(1)
	if err != nil {
		return err
	}
	if len(recent) > 0 && recent[0].Branch != "" {
		fmt.Printf("latest branch: %s\n", recent[0].Branch)
	}

	return nil
}

// cmdVerify checks the integrity of the change log: HMAC chain, monotonic
// recorded_at timestamps, and that the rendered markdown files match the
// database. It prints the Merkle root of the current log.
func cmdVerify() error {
	root, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		return fmt.Errorf("integrity key: %w", err)
	}

	rows, err := st.AllChanges()
	if err != nil {
		return fmt.Errorf("load changes: %w", err)
	}

	fmt.Printf("changes: %d\n", len(rows))

	chainErrs := integrity.VerifyChain(key, rows)
	if len(chainErrs) == 0 {
		fmt.Println("hmac chain: OK")
	} else {
		fmt.Printf("hmac chain: %d problem(s)\n", len(chainErrs))
		for _, e := range chainErrs {
			fmt.Printf("  row %d: %s\n", e.ID, e.Problem)
		}
	}

	clockWarnings := 0
	for _, c := range rows {
		if c.ClockTamperWarning {
			clockWarnings++
		}
	}
	if clockWarnings > 0 {
		fmt.Printf("clock tamper warnings: %d row(s) flagged at write time\n", clockWarnings)
	}

	diverged, err := hint.VerifyRendered(st, root, recorder.ChangelogLimit, recorder.HistoryLimitPerFile)
	if err != nil {
		return fmt.Errorf("markdown verify: %w", err)
	}
	if len(diverged) == 0 {
		fmt.Println("rendered markdown: OK")
	} else {
		fmt.Printf("rendered markdown: %d diverged file(s)\n", len(diverged))
		for _, p := range diverged {
			fmt.Printf("  - %s\n", p)
		}
	}

	rootHash := integrity.MerkleRoot(rows)
	if rootHash != "" {
		fmt.Printf("merkle root: %s\n", rootHash)
	}

	if len(chainErrs) > 0 || len(diverged) > 0 || clockWarnings > 0 {
		return fmt.Errorf("verification failed")
	}

	if err := st.MetaSet("last_verify_at", fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not persist last_verify_at: %v\n", err)
	}
	return nil
}

// cmdChanges queries the change log over a recorded_at time range. It
// accepts RFC3339 timestamps or Unix seconds for -since and -until.
func cmdChanges(args []string) error {
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

	_, st, err := openRootAndStore()
	if err != nil {
		return err
	}
	defer st.Close()

	since, err := parseTimestamp(*sinceStr)
	if err != nil {
		return fmt.Errorf("since: %w", err)
	}
	until, err := parseTimestamp(*untilStr)
	if err != nil {
		return fmt.Errorf("until: %w", err)
	}

	changes, err := st.ChangesInRange(since, until, *file, *limit)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if len(changes) == 0 {
		fmt.Println("no changes in range")
		return nil
	}
	for _, c := range changes {
		when := time.Unix(c.RecordedAt, 0).Format(time.RFC3339)
		fmt.Fprintf(os.Stdout, "[%s] %s (%s", when, c.FilePath, c.Source)
		if c.AgentID != "" {
			fmt.Fprintf(os.Stdout, ", %s", c.AgentID)
		}
		if c.ClockTamperWarning {
			fmt.Fprintf(os.Stdout, " [CLOCK TAMPER WARNING]")
		}
		fmt.Fprintf(os.Stdout, ", %s): %s\n", shortCommitHash(c.CommitHash), c.Summary)
	}
	return nil
}

func shortCommitHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	if h == "" {
		return "uncommitted"
	}
	return h
}

// parseTimestamp accepts either a Unix-seconds integer or an RFC3339 string.
func parseTimestamp(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("timestamp is required")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("expected Unix seconds or RFC3339 timestamp")
}

// cmdRotateSalt generates a new integrity salt and re-signs every existing
// change row so the HMAC chain remains valid under the new key. The existing
// chain is verified first unless -force is passed.
func cmdRotateSalt(args []string) error {
	fs := flag.NewFlagSet("rotate-salt", flag.ExitOnError)
	force := fs.Bool("force", false, "rotate even if the existing chain has integrity problems")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if err := integrity.RotateSalt(root, *force); err != nil {
		return err
	}
	fmt.Println("salt rotated; all rows re-signed with the new key")
	return nil
}
