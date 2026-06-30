// Package recorder is the single write path shared by the MCP tool
// (agent-driven) and the CLI "record" command (manual/scripted use).
// It inserts the change row and immediately re-renders the affected
// markdown so .githints/ is never stale.
package recorder

import (
	"fmt"

	"githints/internal/gitutil"
	"githints/internal/hint"
	"githints/internal/store"
)

const HistoryLimitPerFile = 20
const ChangelogLimit = 100

type Input struct {
	FilePath   string
	Summary    string
	Reason     string
	Source     string // "agent" or "hook"
	DiffStat   string // optional, hook fills this in; empty for agent calls
	CommitHash string // leave empty for agent calls — record_change fires
	// before the commit exists. The row stays "pending" (shown as
	// "uncommitted") until the post-commit hook claims it via
	// store.ClaimPending. Hook calls pass the real, already-known hash.
}

// Record writes one change row and regenerates that file's hint plus the
// root changelog. root is the repo root (where .githints/ lives).
func Record(st *store.Store, root string, in Input) error {
	if in.FilePath == "" || in.Summary == "" {
		return fmt.Errorf("file and summary are required")
	}
	if in.Source != "agent" && in.Source != "hook" {
		return fmt.Errorf("source must be \"agent\" or \"hook\", got %q", in.Source)
	}

	// Agent calls happen before the commit exists, so capture the working-tree
	// diff stat now so the hook can later compare it against the real commit.
	if in.Source == "agent" && in.DiffStat == "" && in.CommitHash == "" {
		in.DiffStat = gitutil.WorktreeDiffStat(in.FilePath)
	}

	_, err := st.Insert(store.Change{
		FilePath:   in.FilePath,
		CommitHash: in.CommitHash,
		Source:     in.Source,
		Summary:    in.Summary,
		Reason:     in.Reason,
		DiffStat:   in.DiffStat,
	})
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	if err := hint.RenderFile(st, root, in.FilePath, HistoryLimitPerFile); err != nil {
		return fmt.Errorf("render file hint: %w", err)
	}
	if err := hint.RenderChangelog(st, root, ChangelogLimit); err != nil {
		return fmt.Errorf("render changelog: %w", err)
	}
	return nil
}
