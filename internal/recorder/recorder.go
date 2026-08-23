// Package recorder is the single write path shared by the MCP tool
// (agent-driven) and the CLI "record" command (manual/scripted use).
// It inserts the change row and immediately re-renders the affected
// markdown so .githints/ is never stale.
package recorder

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/secrets"
	"github.com/cjrdz/githints/internal/store"
)

const HistoryLimitPerFile = 20
const ChangelogLimit = 100

// Caps on agent-supplied text and batch size. Nothing between an MCP argument
// and SQLite bounded these before: an oversized summary is stored, re-rendered
// into the per-file hint on every subsequent record for that file, echoed into
// CHANGES.md, and re-read whole by the markdown verifier — unbounded write
// amplification from one string. Enforced here, in the shared write path, so
// the same limit covers the MCP tools and the CLI.
const (
	MaxSummaryLen = 4000
	MaxReasonLen  = 4000
	MaxBatchSize  = 100
)

type Input struct {
	FilePath   string
	Summary    string
	Reason     string
	Source     string // "agent", "llm", or "fallback"
	DiffStat   string // optional, hook fills this in; empty for agent calls
	DiffHash   string // optional override; auto-computed from git diff if empty
	CommitHash string // leave empty for agent calls — record_change fires
	// before the commit exists. The row stays "pending" (shown as
	// "uncommitted") until the post-commit hook claims it via
	// store.ClaimPendingTx. Hook calls pass the real, already-known hash.
	Branch     string // optional override; auto-populated from CurrentBranch when empty
	AgentID    string
	RecordedAt int64
}

// validSources are the allowed provenance values. The integrity chain treats
// every source the same way: source is audit metadata, not a trust signal.
var validSources = map[string]bool{
	"agent":    true,
	"llm":      true,
	"fallback": true,
}

// SecretScanResult describes whether text matched a known secret pattern
// and, if so, which kind, so callers can surface a useful message instead of
// just "rejected".
type SecretScanResult struct {
	Matched bool
	Pattern string
}

// ScanSecrets checks text against the known secret patterns. The pattern
// field is the regex source of the first match, or "" when nothing matched.
// The pattern list lives in internal/secrets so this check and the diff
// scrubber in internal/llm cannot drift apart.
func ScanSecrets(text string) SecretScanResult {
	pattern, matched := secrets.Scan(text)
	return SecretScanResult{Matched: matched, Pattern: pattern}
}

// ValidateFilePath rejects paths that could escape the repo root via .. or
// absolute references. It is the critical defense against path traversal in
// the file parameter of record_change. filepath.IsLocal is available from
// Go 1.20 and exactly captures the "no .., not absolute, not empty" rule.
func ValidateFilePath(p string) error {
	if p == "" {
		return fmt.Errorf("file path is required")
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("file path must be repo-relative, not absolute: %s", p)
	}
	if !filepath.IsLocal(p) {
		return fmt.Errorf("file path must be a local, relative path without '..' segments: %s", p)
	}
	return nil
}

// Record writes one change row and regenerates that file's hint plus the
// root changelog. root is the repo root (where .githints/ lives). key is the
// integrity key used to HMAC-chain the row; if nil, the row is inserted
// without an HMAC (legacy mode, mainly for tests).
func Record(st *store.Store, root string, key []byte, in Input) error {
	_, err := record(st, root, key, in, true)
	return err
}

// BatchRecord inserts multiple changes and renders each affected file and
// the changelog exactly once. Use this from the record_batch MCP tool to
// avoid N redundant render passes.
//
// All rows land or none do. Previously a failure at item K left items 0..K-1
// committed and returned an error, so the caller had no way to know what had
// actually been recorded.
func BatchRecord(st *store.Store, root string, key []byte, inputs []Input) error {
	if len(inputs) == 0 {
		return fmt.Errorf("no changes to record")
	}
	if len(inputs) > MaxBatchSize {
		return fmt.Errorf("batch too large: %d changes (max %d)", len(inputs), MaxBatchSize)
	}

	// Validate and enrich every row before opening the transaction: prepare
	// shells out to git, which has no business running with a write lock held.
	rows := make([]store.Change, len(inputs))
	files := make(map[string]struct{}, len(inputs))
	for i, in := range inputs {
		c, err := prepare(st, in)
		if err != nil {
			return fmt.Errorf("changes[%d]: %w", i, err)
		}
		rows[i] = c
		files[c.FilePath] = struct{}{}
	}

	if err := st.WithTx(func(tx *sql.Tx) error {
		prev, err := store.LastHMACTx(tx)
		if err != nil {
			return fmt.Errorf("last hmac: %w", err)
		}
		for i := range rows {
			if key != nil {
				rows[i].PrevHMAC = prev
				h, err := integrity.ComputeHMAC(key, rows[i])
				if err != nil {
					return err
				}
				rows[i].HMAC = h
				prev = h
			}
			if _, err := store.InsertTx(tx, rows[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}

	for f := range files {
		if err := hint.RenderFile(st, root, f, HistoryLimitPerFile); err != nil {
			return fmt.Errorf("render file hint for %s: %w", f, err)
		}
	}
	if err := hint.RenderChangelog(st, root, ChangelogLimit); err != nil {
		return fmt.Errorf("render changelog: %w", err)
	}
	return nil
}

// prepare validates an Input and builds the row to insert, including the
// working-tree diff stat, diff hash, branch stamp, timestamp, and the
// clock-tamper flag. It does not sign or insert.
//
// The tamper flag is decided here, before signing, so it ends up inside the
// HMAC payload. Note that the flag's high-water mark is process-global and
// therefore not rolled back if the surrounding transaction aborts; the mark is
// monotonic, so the only effect is that later checks stay slightly more
// conservative.
func prepare(st *store.Store, in Input) (store.Change, error) {
	if in.FilePath == "" || in.Summary == "" {
		return store.Change{}, fmt.Errorf("file and summary are required")
	}
	if err := ValidateFilePath(in.FilePath); err != nil {
		return store.Change{}, err
	}
	if !validSources[in.Source] {
		return store.Change{}, fmt.Errorf("source must be one of agent/llm/fallback, got %q", in.Source)
	}
	if len(in.Summary) > MaxSummaryLen {
		return store.Change{}, fmt.Errorf("summary too long: %d bytes (max %d)", len(in.Summary), MaxSummaryLen)
	}
	if len(in.Reason) > MaxReasonLen {
		return store.Change{}, fmt.Errorf("reason too long: %d bytes (max %d)", len(in.Reason), MaxReasonLen)
	}

	// Defense-in-depth: never let an agent or hook persist a row whose
	// summary or reason contains an obvious secret. The hint markdown is
	// committed to git, so a leaked credential here is as bad as one in
	// source.
	for _, text := range []string{in.Summary, in.Reason} {
		if r := ScanSecrets(text); r.Matched {
			return store.Change{}, fmt.Errorf("refusing to record change: summary/reason matches a known secret pattern (%s)", r.Pattern)
		}
	}

	// Agent calls happen before the commit exists, so capture the working-tree
	// diff stat now so the hook can later compare it against the real commit.
	if in.Source == "agent" && in.DiffStat == "" && in.CommitHash == "" {
		in.DiffStat = gitutil.WorktreeDiffStat(in.FilePath)
	}

	// Capture a hash of the unified diff so the integrity chain binds the
	// actual code content, not just the metadata. Errors (e.g., first commit
	// with no HEAD yet) leave diff_hash empty rather than blocking the
	// record.
	if in.DiffHash == "" {
		if in.CommitHash == "" {
			in.DiffHash, _ = gitutil.WorktreeDiffHash(in.FilePath)
		} else {
			in.DiffHash, _ = gitutil.DiffHash(in.CommitHash, in.FilePath)
		}
	}

	// Stamp the current branch on every row. Failures here (detached HEAD,
	// old git) are non-fatal — an empty branch is still a valid value.
	if in.Branch == "" {
		if branch, err := gitutil.CurrentBranch(); err == nil {
			in.Branch = branch
		}
	}

	if in.RecordedAt == 0 {
		in.RecordedAt = time.Now().Unix()
	}

	return store.Change{
		FilePath:           in.FilePath,
		CommitHash:         in.CommitHash,
		Branch:             in.Branch,
		Source:             in.Source,
		Summary:            in.Summary,
		Reason:             in.Reason,
		DiffStat:           in.DiffStat,
		DiffHash:           in.DiffHash,
		AgentID:            in.AgentID,
		RecordedAt:         in.RecordedAt,
		ClockTamperWarning: st.CheckClockTamper(in.RecordedAt),
	}, nil
}

// record is the shared implementation. When render is false it skips the
// markdown rendering so callers can batch render themselves.
func record(st *store.Store, root string, key []byte, in Input, render bool) (store.Change, error) {
	c, err := prepare(st, in)
	if err != nil {
		return store.Change{}, err
	}

	if key != nil {
		prev, err := st.LastHMAC()
		if err != nil {
			return store.Change{}, fmt.Errorf("last hmac: %w", err)
		}
		c.PrevHMAC = prev
		c.HMAC, err = integrity.ComputeHMAC(key, c)
		if err != nil {
			return store.Change{}, err
		}
	}

	if _, err := st.Insert(c); err != nil {
		return store.Change{}, fmt.Errorf("insert: %w", err)
	}

	if render {
		if err := hint.RenderFile(st, root, in.FilePath, HistoryLimitPerFile); err != nil {
			return store.Change{}, fmt.Errorf("render file hint: %w", err)
		}
		if err := hint.RenderChangelog(st, root, ChangelogLimit); err != nil {
			return store.Change{}, fmt.Errorf("render changelog: %w", err)
		}
	}
	return c, nil
}
