package hooks

import (
	"fmt"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

// maxRevertRows bounds the git calls one revert commit can cost the hook.
const maxRevertRows = 200

// recordReverts appends a "reverted" outcome for every recorded change that the
// commit at hash reverts.
//
// A commit message saying "This reverts commit X" is a claim made by whoever
// wrote the commit, which in a clone or a pull request is not necessarily
// anyone you trust, so it is only a pointer to where to look. A row is marked
// reverted only when the tree agrees: this commit changed the row's file, and
// the file now holds exactly what it held before the reverted commit. A forged
// message, a revert that hit conflicts, and a file reverted by hand all fail
// that test for the files they did not actually restore.
//
// The outcome is written with source "fallback" (githints inferred it, shown as
// auto-detected) and this commit as its commit hash, which puts it under this
// commit's Merkle anchor. Running the hook twice for one commit records nothing
// the second time. The message text itself is never stored: the note names the
// commit and nothing else.
//
// Not detected: reverts made with `git reset`, which leave no commit, and a
// change reverted by a commit whose message does not say so. A revert of a
// rename is detected: the old and new paths both count as touched. A revert that is
// itself reverted leaves the original outcome as "reverted".
func recordReverts(st *store.Store, root string, key []byte, hash string) error {
	msg, err := gitutil.CommitMessage(hash)
	if err != nil {
		return fmt.Errorf("read commit message: %w", err)
	}
	reverted := gitutil.RevertedCommits(msg)
	if len(reverted) == 0 {
		return nil
	}
	// Renames off: reverting a rename shows the old path by default, but the
	// row being undone was recorded under the new one.
	changed, err := gitutil.ChangedFilesNoRenames(hash)
	if err != nil {
		return fmt.Errorf("list changed files: %w", err)
	}

	touched := make(map[string]bool, len(changed))
	for _, f := range changed {
		touched[f] = true
	}
	note := fmt.Sprintf("Reverted by commit %s.", gitutil.ShortHash(hash))

	budget := maxRevertRows
	for _, ref := range reverted {
		full, ok := gitutil.ResolveCommit(ref)
		if !ok || full == hash {
			continue // names a commit this clone does not have, or itself
		}
		parent, hasParent := gitutil.ParentCommit(full)

		rows, err := st.ChangesForCommit(full)
		if err != nil {
			return fmt.Errorf("load rows for %s: %w", gitutil.ShortHash(full), err)
		}
		for _, row := range rows {
			if row.Kind != store.KindChange || !touched[row.FilePath] {
				continue
			}
			if budget--; budget < 0 {
				return fmt.Errorf("revert names more than %d recorded changes; the rest were not marked", maxRevertRows)
			}

			before := ""
			if hasParent {
				if before, err = gitutil.PathAt(parent, row.FilePath); err != nil {
					return err
				}
			}
			now, err := gitutil.PathAt(hash, row.FilePath)
			if err != nil {
				return err
			}
			if before != now {
				continue // not restored here: partial or conflicted revert, or a false claim
			}

			prior, err := st.OutcomesFor(row.ID)
			if err != nil {
				return err
			}
			if alreadyMarked(prior, note) {
				continue
			}
			if _, err := recorder.RecordOutcome(st, root, key, recorder.OutcomeInput{
				ChangeID:   row.ID,
				State:      store.OutcomeReverted,
				Note:       note,
				Source:     "fallback",
				CommitHash: hash,
			}); err != nil {
				return fmt.Errorf("record outcome for change %d: %w", row.ID, err)
			}
		}
	}
	return nil
}

func alreadyMarked(outcomes []store.Change, note string) bool {
	for _, o := range outcomes {
		if o.OutcomeState == store.OutcomeReverted && o.Summary == note {
			return true
		}
	}
	return false
}
