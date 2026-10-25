package risk

import (
	"fmt"
	"slices"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// ReviewInput is a reviewer's verdict on the staged change.
type ReviewInput struct {
	Verdict    string   // approved, escalated or inconclusive
	Findings   string   // what the review found; required unless approved
	Lenses     []string // which of Lenses the review covered
	AgentID    string
	RecordedAt int64 // leave zero; tests set it
}

// RecordReview signs a receipt for exactly what is staged now: the tree `git
// write-tree` gives for the index, on the current HEAD. githints computes the
// risk level itself; the reviewer supplies only the verdict, the findings and
// the lenses it covered, and the receipt records all of them as claimed.
//
// The receipt is unused until a commit of that exact tree on that base, which
// the post-commit hook stamps on it; any other commit, or a change to what is
// staged, leaves it behind. It renders no markdown: a receipt is about a
// change set, not a file.
func RecordReview(st *store.Store, key []byte, in Inputs, rv ReviewInput) (store.Change, Report, error) {
	if !store.ValidVerdict(rv.Verdict) {
		return store.Change{}, Report{}, fmt.Errorf("verdict must be approved, escalated or inconclusive, got %q", rv.Verdict)
	}
	if rv.Findings == "" && rv.Verdict != store.VerdictApproved {
		return store.Change{}, Report{}, fmt.Errorf("findings are required for a %s review: say what needs a person or what was not finished", rv.Verdict)
	}
	if len(rv.Findings) > recorder.MaxReasonLen {
		return store.Change{}, Report{}, fmt.Errorf("findings too long: %d bytes (max %d)", len(rv.Findings), recorder.MaxReasonLen)
	}
	if len(rv.AgentID) > recorder.MaxAgentIDLen {
		return store.Change{}, Report{}, fmt.Errorf("agent_id too long: %d bytes (max %d)", len(rv.AgentID), recorder.MaxAgentIDLen)
	}
	var lenses []string
	for _, l := range rv.Lenses {
		if !slices.Contains(Lenses, l) {
			return store.Change{}, Report{}, fmt.Errorf("unknown lens %q (lenses: risk, resilience, readability, reliability)", l)
		}
		if !slices.Contains(lenses, l) {
			lenses = append(lenses, l)
		}
	}

	// Same order as prepare: strip invisible characters first so they cannot
	// hide a credential from the scan that follows.
	rv.Findings = textsafe.StripControls(rv.Findings)
	rv.AgentID = textsafe.Remove(rv.AgentID)
	for _, text := range []string{rv.Findings, rv.AgentID} {
		if r := recorder.ScanSecrets(text); r.Matched {
			return store.Change{}, Report{}, fmt.Errorf("refusing to record review: findings/agent_id match a known secret pattern (%s)", r.Pattern)
		}
	}

	paths, err := Staged()
	if err != nil {
		return store.Change{}, Report{}, fmt.Errorf("list staged files: %w", err)
	}
	if len(paths) == 0 {
		return store.Change{}, Report{}, fmt.Errorf("nothing is staged: a review is of the staged change, so stage it first")
	}
	tree, err := gitutil.IndexTree()
	if err != nil {
		return store.Change{}, Report{}, fmt.Errorf("read the staged tree: %w", err)
	}
	base, err := gitutil.LastCommitHash()
	if err != nil {
		base = "" // no commit yet; the first commit has no parent either
	}
	rep, err := Assess(in, paths)
	if err != nil {
		return store.Change{}, Report{}, err
	}

	c := store.Change{
		Source:        "agent",
		Summary:       ReceiptSummary(rv.Verdict, rep.Level, lenses),
		Reason:        rv.Findings,
		AgentID:       rv.AgentID,
		RecordedAt:    rv.RecordedAt,
		Kind:          store.KindReview,
		CandidateTree: tree,
		BaseCommit:    base,
		Verdict:       rv.Verdict,
	}
	stamped := c.RecordedAt == 0
	if stamped {
		c.RecordedAt = time.Now().Unix()
	}
	if branch, err := gitutil.CurrentBranch(); err == nil {
		c.Branch = branch
	}
	// Before signing, so the flag is inside the HMAC payload.
	c.ClockTamperWarning = st.CheckClockTamper(c.RecordedAt)

	var sign func(store.Change) (string, error)
	if key != nil {
		sign = func(c store.Change) (string, error) { return integrity.ComputeHMAC(key, c) }
	}
	c, err = st.InsertSigned(c, sign, stamped)
	if err != nil {
		return store.Change{}, Report{}, fmt.Errorf("insert: %w", err)
	}
	return c, rep, nil
}
