// Package hooks implements the git hook commands invoked by .git/hooks/post-commit
// and .git/hooks/pre-commit.
package hooks

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/graph"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/risk"
	"github.com/cjrdz/githints/internal/secrets"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// hookSummaryFor decides what text the post-commit hook should record when
// it has to write a fallback entry.
func hookSummaryFor(verifiable bool) string {
	if verifiable {
		return "Change detected by git hook (no AI summary recorded for this commit)."
	}
	return "Change detected by git hook; agent coverage could not be verified (binary, first commit, or missing diff stat)."
}

// HookRun is invoked by .git/hooks/post-commit. It records a low-detail
// fallback entry only for files the agent did not describe at all for this
// exact commit. Each file's claim-check-insert sequence runs inside a
// transaction so a concurrent record_change cannot slip between ClaimPending
// and the fallback decision.
func HookRun() error {
	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

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

	// One git process for the whole commit, not one per fallback file. A
	// failure costs only the stats: every fallback row is then unverifiable.
	stats, err := gitutil.CommitDiffStats(hash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: diff stats: %v\n", err)
	}

	// Resolved once for the commit rather than by each fallback row.
	branch, _ := gitutil.CurrentBranch() // detached HEAD or an old git: rows carry no branch

	rendered := false
	for _, f := range files {
		if f == "" || strings.HasPrefix(f, ".githints/") {
			continue // never track changes to githints' own output
		}
		// Paths now arrive exactly as git has them, so one with a control
		// character in it can: the recorder refuses those, and that must cost
		// that file, not the rest of the commit's records and its anchor.
		if err := recorder.ValidateFilePath(f); err != nil {
			fmt.Fprintf(os.Stderr, "githints: not recording %q: %v\n", f, err)
			continue
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

		if len(agentRows) > 0 {
			// The agent already described this file. Re-render the per-file
			// hint if we just claimed pending rows so the "uncommitted" label
			// is replaced with the real commit hash.
			if claimed > 0 {
				if err := hint.RenderFile(st, root, f, recorder.HistoryLimitPerFile); err != nil {
					return fmt.Errorf("render file hint for %s: %w", f, err)
				}
				rendered = true
			}
			continue
		}

		diffStat := stats[f]
		_, _, verifiable := gitutil.ParseDiffStat(diffStat)

		// Summaries come from the agent (record_change); the hook records
		// only that the file changed, and never sends a diff anywhere.
		err = recorder.Record(st, root, key, recorder.Input{
			FilePath:   f,
			Summary:    hookSummaryFor(verifiable),
			Source:     "fallback",
			DiffStat:   diffStat,
			CommitHash: hash,
			Branch:     branch,
		})
		if err != nil {
			return fmt.Errorf("record %s: %w", f, err)
		}
		rendered = true
	}

	// A receipt for exactly this commit's tree and parent is now used up.
	// Before the anchor below, which then covers it. Like revert detection, a
	// failure costs only this step.
	if err := burnReviews(st, hash); err != nil {
		fmt.Fprintf(os.Stderr, "githints: review receipts: %v\n", err)
	}

	// Detection is a convenience on top of the record, never a reason to lose
	// the rest of the hook's work: a failure here is reported and skipped.
	if err := recordReverts(st, root, key, hash); err != nil {
		fmt.Fprintf(os.Stderr, "githints: revert detection: %v\n", err)
	}

	if rendered {
		if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
			return fmt.Errorf("render changelog: %w", err)
		}
	}

	if cfg.Index.Enabled {
		idxDB, err := index.Open(lang.IndexDBPath(root))
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: could not open index db: %v\n", err)
		} else {
			defer idxDB.Close()
			if err := index.IncrementalScan(idxDB, lang.ScanOptions{
				Root:         root,
				Languages:    cfg.Index.Languages,
				MaxFileSize:  int64(cfg.Index.MaxFileSize),
				ParseTimeout: time.Duration(cfg.Index.ParseTimeoutMS) * time.Millisecond,
			}, files, cfg.Index.MaxBytes); err != nil {
				fmt.Fprintf(os.Stderr, "githints: incremental index scan: %v\n", err)
			} else if cfg.Index.GraphHTML {
				graph.RefreshPage(root, idxDB)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "githints: indexing disabled in config; skipping incremental index update\n")
	}

	commitRows, err := st.ChangesForCommit(hash)
	if err != nil {
		return fmt.Errorf("load commit rows: %w", err)
	}
	if len(commitRows) > 0 {
		all, err := st.AllChanges()
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: could not load log for Merkle anchor: %v\n", err)
			return nil
		}
		anchor, err := integrity.BuildAnchor(all, commitRows)
		if err != nil {
			// Non-fatal, like the note write below: the store-level chain is
			// still intact, we just cannot anchor it externally.
			fmt.Fprintf(os.Stderr, "githints: could not compute Merkle root: %v\n", err)
			return nil
		}
		if err := gitutil.AddNote("refs/notes/githints", anchor.String()); err != nil {
			// Non-fatal: the store-level integrity is still valid; the note
			// is an optional external anchor.
			fmt.Fprintf(os.Stderr, "githints: could not add Merkle note: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "githints: anchored Merkle root %s for commit %s\n", anchor.CommitRoot, gitutil.ShortHash(hash))
		}
	}

	return nil
}

// burnReviews stamps hash on every unused review receipt for the tree and
// parent this commit has, so none of them is ever valid again.
func burnReviews(st *store.Store, hash string) error {
	pending, err := st.Reviews(true, 1)
	if err != nil || len(pending) == 0 {
		return err // the common case: no receipts, and no git call to make
	}
	shapes, err := gitutil.CommitShapes([]string{hash})
	if err != nil {
		return err
	}
	shape := shapes[hash]
	if shape.Missing {
		return fmt.Errorf("commit %s not found", gitutil.ShortHash(hash))
	}
	return st.WithTx(func(tx *sql.Tx) error {
		n, err := store.ClaimReviewsTx(tx, hash, shape.Tree, shape.Parent)
		if err == nil && n > 0 {
			fmt.Fprintf(os.Stderr, "githints: commit %s used %d review receipt(s)\n", gitutil.ShortHash(hash), n)
		}
		return err
	})
}

// PreCommit is invoked by .git/hooks/pre-commit. For each staged file
// (excluding githints' own output) it checks whether the agent left a
// pending record_change for it; if not, the file is about to land without
// a "why" attached, so the hook warns. Set GITHINTS_PRECOMMIT_BLOCK=1 to
// turn the warning into a hard block that aborts the commit.
//
// The check is best-effort: a missing pending record is not proof of a
// problem (an agent may legitimately batch records after staging), so the
// default mode is advisory. The block mode is opt-in for teams that want
// to enforce the discipline.
func PreCommit() error {
	err := preCommitCheck()
	if err == nil || errors.Is(err, errPrecommitBlocked) {
		return err
	}
	// Any other failure is githints' problem, not the commit's: a locked or
	// corrupt store, a bad config, a missing git. The gate is a warning by
	// design, so it must never be the reason someone cannot commit -- not
	// even with GITHINTS_PRECOMMIT_BLOCK=1, which is about unrecorded files.
	fmt.Fprintf(os.Stderr, "githints: pre-commit check skipped: %v\n", err)
	return nil
}

// errPrecommitBlocked is the one pre-commit outcome that may fail a commit.
var errPrecommitBlocked = errors.New("pre-commit gate blocked the commit")

func preCommitCheck() error {
	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	if err := secretGate(); err != nil {
		return err
	}
	if err := recordGate(st); err != nil {
		return err
	}
	return reviewGate(root, st)
}

// maxSecretLines bounds how many findings the hook lists.
const maxSecretLines = 20

// secretGate warns when a line the commit adds looks like a credential, and
// with GITHINTS_SECRET_BLOCK=1 stops the commit. It names the file and line,
// never the value. .githintsignore does not apply: a key is a key wherever it
// is. githints' own output is skipped; it refuses secrets when it is written.
func secretGate() error {
	diff, err := gitutil.StagedDiff()
	if err != nil {
		return fmt.Errorf("read staged diff: %w", err)
	}
	var found []secrets.Finding
	for _, f := range secrets.ScanDiff(diff) {
		if !strings.HasPrefix(f.File, ".githints/") {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		return nil
	}
	fmt.Fprintln(os.Stderr, "githints: staged lines that look like credentials (values not shown):")
	for i, f := range found {
		if i == maxSecretLines {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(found)-i)
			break
		}
		fmt.Fprintf(os.Stderr, "  - %s:%d\n", textsafe.OneLine(f.File), f.Line)
	}
	fmt.Fprintln(os.Stderr, "githints: move them to a secret store or an ignored file; GITHINTS_SECRET_BLOCK=1 makes this stop the commit.")
	if os.Getenv("GITHINTS_SECRET_BLOCK") == "1" {
		return fmt.Errorf("%w: %d possible credential(s) staged", errPrecommitBlocked, len(found))
	}
	return nil
}

// recordGate warns about staged files nobody described, and blocks the
// commit for them under GITHINTS_PRECOMMIT_BLOCK=1.
func recordGate(st *store.Store) error {
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
		// A path from the repository: printed raw, an escape in it could
		// rewrite the terminal.
		fmt.Fprintf(os.Stderr, "  - %s\n", textsafe.OneLine(f))
	}
	fmt.Fprintln(os.Stderr, "call record_change for each before committing, or set GITHINTS_PRECOMMIT_BLOCK=1 to enforce.")

	if os.Getenv("GITHINTS_PRECOMMIT_BLOCK") == "1" {
		return fmt.Errorf("%w: %d unrecorded file(s); set GITHINTS_PRECOMMIT_BLOCK=0 to warn only", errPrecommitBlocked, len(missing))
	}
	return nil
}

// reviewGate is review.gate in config.json: when the staged change is high
// risk and no approved receipt covers exactly what is staged, it warns, or
// with "block" stops the commit. GITHINTS_REVIEW=0 turns it off: whether to
// review is the user's choice, never a cloned repository's.
func reviewGate(root string, st *store.Store) error {
	if os.Getenv("GITHINTS_REVIEW") == "0" {
		return nil
	}
	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	gate := cfg.Review.Gate
	if gate != "warn" && gate != "block" {
		return nil // off, or a value doctor reports
	}
	paths, err := risk.Staged()
	if err != nil || len(paths) == 0 {
		return err
	}
	in := risk.Inputs{Store: st, Root: root, Config: cfg.Review}
	if cfg.Index.Enabled {
		if idx := core.OpenExistingIndex(root); idx != nil {
			defer idx.Close()
			in.Index = idx
		}
	}
	rep, err := risk.Assess(in, paths)
	if err != nil {
		return err
	}
	if rep.Level < risk.High {
		return nil
	}
	receipts, err := risk.CurrentReceipts(st, root, rep.Level)
	if err != nil {
		return err
	}
	if receipts.Valid {
		return nil
	}
	fmt.Fprintf(os.Stderr, "githints: this change is high risk and has no approved review: %s.\n", receipts.Why)
	fmt.Fprintln(os.Stderr, "githints: run `githints review` to see why, review it by all four lenses, then record it with record_review.")
	if gate == "block" {
		return fmt.Errorf("%w: high-risk change without an approved review (GITHINTS_REVIEW=0 turns review off)", errPrecommitBlocked)
	}
	return nil
}
