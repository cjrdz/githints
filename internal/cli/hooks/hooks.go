// Package hooks implements the git hook commands invoked by .git/hooks/post-commit
// and .git/hooks/pre-commit.
package hooks

import (
	"context"
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
	"github.com/cjrdz/githints/internal/llm"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

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

// hookFallbackSummary returns the generic fallback text and provenance for a
// hook row. It is used when Ollama is disabled, the circuit breaker is open,
// or any LLM call fails.
func hookFallbackSummary(verifiable bool) (string, string) {
	return hookSummaryFor(verifiable), "fallback"
}

// summarizeViaOllama asks the local Ollama model to caption a diff. On any
// error it returns the generic fallback and logs a one-line warning. The model
// output is treated as untrusted text: if the shared recorder rejects it (e.g.
// it somehow contains a secret pattern), this function falls back as well.
func summarizeViaOllama(ctx context.Context, client *llm.Client, hash, file string, verifiable bool) (summary, source string) {
	diff, err := gitutil.FileDiff(hash, file)
	if err != nil || strings.TrimSpace(diff) == "" {
		return hookFallbackSummary(verifiable)
	}

	llmSummary, err := client.SummarizeDiff(ctx, file, diff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: ollama: %v\n", err)
		return hookFallbackSummary(verifiable)
	}

	return llmSummary, "llm"
}

// HookRun is invoked by .git/hooks/post-commit. It records a low-detail
// fallback entry only for files the agent didn't already record via
// record_change for this exact commit. Each file's claim-check-insert
// sequence runs inside a transaction so a concurrent record_change cannot
// slip between ClaimPending and the fallback decision.
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

	var client *llm.Client
	if cfg.Ollama.Enabled {
		client, err = llm.NewClient(cfg)
		if err != nil {
			return fmt.Errorf("ollama client: %w", err)
		}
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

	ctx := context.Background()
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
		summary, source := hookFallbackSummary(verifiable)
		if client != nil {
			summary, source = summarizeViaOllama(ctx, client, hash, f, verifiable)
		}

		err = recorder.Record(st, root, key, recorder.Input{
			FilePath:   f,
			Summary:    summary,
			Source:     source,
			DiffStat:   diffStat,
			CommitHash: hash,
		})
		if err != nil {
			// If the LLM produced a summary that the shared recorder rejected
			// (e.g. it matched a secret pattern), fall back to the generic text.
			if source == "llm" {
				fmt.Fprintf(os.Stderr, "githints: ollama summary rejected: %v; using fallback\n", err)
				fallbackSummary, _ := hookFallbackSummary(verifiable)
				err = recorder.Record(st, root, key, recorder.Input{
					FilePath:   f,
					Summary:    fallbackSummary,
					Source:     "fallback",
					DiffStat:   diffStat,
					CommitHash: hash,
				})
			}
			if err != nil {
				return fmt.Errorf("record %s: %w", f, err)
			}
		}
		rendered = true
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
				Obsidian:     cfg.Index.ObsidianWikilinks,
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
			fmt.Fprintf(os.Stderr, "githints: anchored Merkle root %s for commit %s\n", anchor.CommitRoot, core.ShortCommitHash(hash))
		}
	}

	return nil
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
		return fmt.Errorf("%w: %d unrecorded file(s); set GITHINTS_PRECOMMIT_BLOCK=0 to warn only", errPrecommitBlocked, len(missing))
	}
	_ = root // root reserved for future pre-commit rendering hooks
	return nil
}
