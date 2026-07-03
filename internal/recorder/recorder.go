// Package recorder is the single write path shared by the MCP tool
// (agent-driven) and the CLI "record" command (manual/scripted use).
// It inserts the change row and immediately re-renders the affected
// markdown so .githints/ is never stale.
package recorder

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"githints/internal/gitutil"
	"githints/internal/hint"
	"githints/internal/integrity"
	"githints/internal/store"
)

const HistoryLimitPerFile = 20
const ChangelogLimit = 100

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
	// store.ClaimPending. Hook calls pass the real, already-known hash.
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

// secretPatterns are the high-signal credential shapes githints refuses to
// record verbatim. They intentionally err on the side of few, well-known
// shapes: false positives would block legitimate summaries, while the goal
// is just to stop the obvious leak vectors (AWS keys, GitHub PATs, private
// keys, JWTs). The check is a defense-in-depth backstop — it is not a
// substitute for proper secret management.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),          // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`), // GitHub PAT / fine-grained / app / refresh
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.`), // JWT header.payload.
}

// SecretScanResult describes whether text matched a known secret pattern
// and, if so, which kind. Returned by HasSecrets so callers can surface a
// useful message instead of just "rejected".
type SecretScanResult struct {
	Matched bool
	Pattern string
}

// ScanSecrets checks text against the known secret patterns. The pattern
// field is the regex source of the first match, or "" when nothing matched.
func ScanSecrets(text string) SecretScanResult {
	for _, p := range secretPatterns {
		if p.MatchString(text) {
			return SecretScanResult{Matched: true, Pattern: p.String()}
		}
	}
	return SecretScanResult{}
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
func BatchRecord(st *store.Store, root string, key []byte, inputs []Input) error {
	files := make(map[string]struct{})
	for _, in := range inputs {
		c, err := record(st, root, key, in, false)
		if err != nil {
			return err
		}
		files[c.FilePath] = struct{}{}
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

// record is the shared implementation. When render is false it skips the
// markdown rendering so callers can batch render themselves.
func record(st *store.Store, root string, key []byte, in Input, render bool) (store.Change, error) {
	if in.FilePath == "" || in.Summary == "" {
		return store.Change{}, fmt.Errorf("file and summary are required")
	}
	if err := ValidateFilePath(in.FilePath); err != nil {
		return store.Change{}, err
	}
	if !validSources[in.Source] {
		return store.Change{}, fmt.Errorf("source must be one of agent/llm/fallback, got %q", in.Source)
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

	c := store.Change{
		FilePath:   in.FilePath,
		CommitHash: in.CommitHash,
		Branch:     in.Branch,
		Source:     in.Source,
		Summary:    in.Summary,
		Reason:     in.Reason,
		DiffStat:   in.DiffStat,
		DiffHash:   in.DiffHash,
		AgentID:    in.AgentID,
		RecordedAt: in.RecordedAt,
	}

	if key != nil {
		prev, err := st.LastHMAC()
		if err != nil {
			return store.Change{}, fmt.Errorf("last hmac: %w", err)
		}
		c.PrevHMAC = prev
		c.HMAC = integrity.ComputeHMAC(key, c)
	}

	_, err := st.Insert(c)
	if err != nil {
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
