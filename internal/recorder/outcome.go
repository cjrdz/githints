package recorder

import (
	"fmt"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// MaxOutcomeNoteLen bounds an outcome's note. It shares the summary cap: the
// note is stored in the summary column and rendered into the same hints.
const MaxOutcomeNoteLen = MaxSummaryLen

// OutcomeInput describes what became of an earlier recorded change.
type OutcomeInput struct {
	ChangeID   int64  // id of the change this is about, as listed by history/recent
	State      string // worked, partial, failed, reverted or unknown
	Note       string // what happened; required unless the state is worked or unknown
	AgentID    string
	RecordedAt int64 // leave zero; tests set it

	// Source and CommitHash are for the post-commit hook, which records
	// "reverted" outcomes it detects itself. Source is "agent" (the default:
	// someone asserted it) or "fallback" (githints inferred it, shown as
	// auto-detected). CommitHash is the commit that caused the outcome, so the
	// per-commit Merkle root of that commit covers the row; an agent's outcome
	// leaves it empty.
	Source     string
	CommitHash string
}

// noteRequired lists the states whose whole value is the explanation: "failed"
// with no why is the repeated mistake outcomes exist to prevent.
var noteRequired = map[string]bool{
	store.OutcomePartial:  true,
	store.OutcomeFailed:   true,
	store.OutcomeReverted: true,
}

// RecordOutcome appends an outcome row for an earlier change. Outcomes are
// events, never edits: the change keeps its row and its signature, and a later
// outcome for the same change supersedes an earlier one when displayed.
//
// The row is signed into the same chain as every change, and carries the
// target's diff_hash as it is now so `githints verify` can tell if the thing
// it points at was replaced.
func RecordOutcome(st *store.Store, root string, key []byte, in OutcomeInput) (store.Change, error) {
	if in.ChangeID < 1 {
		return store.Change{}, fmt.Errorf("change id is required")
	}
	if !store.ValidOutcome(in.State) {
		return store.Change{}, fmt.Errorf("outcome must be one of worked/partial/failed/reverted/unknown, got %q", in.State)
	}
	if in.Note == "" && noteRequired[in.State] {
		return store.Change{}, fmt.Errorf("a note is required for outcome %q: say what happened", in.State)
	}
	if in.Source == "" {
		in.Source = "agent"
	}
	if in.Source != "agent" && in.Source != "fallback" {
		return store.Change{}, fmt.Errorf("outcome source must be agent or fallback, got %q", in.Source)
	}
	if in.CommitHash != "" && !gitutil.IsValidCommitish(in.CommitHash) {
		return store.Change{}, fmt.Errorf("invalid commit hash %q", in.CommitHash)
	}
	if len(in.Note) > MaxOutcomeNoteLen {
		return store.Change{}, fmt.Errorf("note too long: %d bytes (max %d)", len(in.Note), MaxOutcomeNoteLen)
	}
	if len(in.AgentID) > MaxAgentIDLen {
		return store.Change{}, fmt.Errorf("agent_id too long: %d bytes (max %d)", len(in.AgentID), MaxAgentIDLen)
	}

	// Same order as prepare: strip invisible characters first so they cannot
	// hide a credential from the scan that follows.
	in.Note = textsafe.StripControls(in.Note)
	in.AgentID = textsafe.Remove(in.AgentID)
	for _, text := range []string{in.Note, in.AgentID} {
		if r := ScanSecrets(text); r.Matched {
			return store.Change{}, fmt.Errorf("refusing to record outcome: note/agent_id matches a known secret pattern (%s)", r.Pattern)
		}
	}

	target, ok, err := st.GetChange(in.ChangeID)
	if err != nil {
		return store.Change{}, fmt.Errorf("look up change %d: %w", in.ChangeID, err)
	}
	if !ok {
		return store.Change{}, fmt.Errorf("no recorded change with id %d", in.ChangeID)
	}
	if target.Kind == store.KindOutcome {
		return store.Change{}, fmt.Errorf("id %d is an outcome, not a change; record the outcome against the change it describes (id %d)", target.ID, target.TargetID)
	}

	c := store.Change{
		FilePath:       target.FilePath,
		Source:         in.Source,
		CommitHash:     in.CommitHash,
		Summary:        in.Note,
		AgentID:        in.AgentID,
		RecordedAt:     in.RecordedAt,
		Kind:           store.KindOutcome,
		TargetID:       target.ID,
		TargetDiffHash: target.DiffHash,
		OutcomeState:   in.State,
	}
	stamped := c.RecordedAt == 0
	if stamped {
		c.RecordedAt = timeNow()
	}
	if branch, err := gitutil.CurrentBranch(); err == nil {
		c.Branch = branch
	}
	c.ClockTamperWarning = st.CheckClockTamper(c.RecordedAt)

	var sign func(store.Change) (string, error)
	if key != nil {
		sign = func(c store.Change) (string, error) { return integrity.ComputeHMAC(key, c) }
	}
	c, err = st.InsertSigned(c, sign, stamped)
	if err != nil {
		return store.Change{}, fmt.Errorf("insert: %w", err)
	}

	// The per-file hint shows each change's newest outcome, so it is stale now.
	if err := hint.RenderFile(st, root, c.FilePath, HistoryLimitPerFile); err != nil {
		return store.Change{}, fmt.Errorf("render file hint: %w", err)
	}
	return c, nil
}
