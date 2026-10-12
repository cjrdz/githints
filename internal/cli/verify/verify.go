// Package verify implements the verify CLI command.
package verify

import (
	"fmt"
	"os"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/recorder"
)

// Run checks the integrity of the change log: HMAC chain, monotonic
// recorded_at timestamps, and that the rendered markdown files match the
// database. It prints the Merkle root of the current log.
func Run() error {
	root, st, err := core.OpenInitialized()
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
	if hint := core.KeyChangedHint(rows, chainErrs); hint != "" {
		fmt.Println(hint)
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

	// The HMAC chain alone cannot catch a same-user attacker: the salt is
	// readable by that user, so the whole table can be re-signed. The anchors
	// in refs/notes/githints are the external check, and nothing read them
	// back until now.
	notes, err := gitutil.ReadNotes("refs/notes/githints")
	if err != nil {
		return fmt.Errorf("read anchor notes: %w", err)
	}
	anchors := integrity.VerifyAnchors(rows, notes)
	switch {
	case anchors.Notes == 0:
		fmt.Println("merkle anchors: none found (nothing committed yet, or refs/notes/githints was not fetched)")
	case len(anchors.Problems) == 0:
		fmt.Printf("merkle anchors: OK (%d note(s) checked)\n", anchors.Checked)
	default:
		fmt.Printf("merkle anchors: %d problem(s)\n", len(anchors.Problems))
		for _, p := range anchors.Problems {
			fmt.Printf("  - %s\n", p)
		}
	}
	if n := len(anchors.Unanchored); n > 0 {
		fmt.Printf("unanchored commits: %d (rows exist but no note; the note write failed or was removed)\n", n)
	}
	if anchors.Unanchorable > 0 {
		fmt.Printf("rows after the newest anchor: %d (anchored at the next commit)\n", anchors.Unanchorable)
	}

	if len(chainErrs) > 0 || len(diverged) > 0 || clockWarnings > 0 || len(anchors.Problems) > 0 {
		return core.ErrVerifyFailed
	}

	if err := st.MetaSet("last_verify_at", fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not persist last_verify_at: %v\n", err)
	}
	return nil
}
