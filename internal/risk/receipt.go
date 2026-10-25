package risk

import (
	"fmt"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// ReceiptSummary is the signed summary line of a review receipt. Its form is
// fixed so the lenses a reviewer claimed can be read back from the row.
func ReceiptSummary(verdict string, level Level, lenses []string) string {
	l := "none"
	if len(lenses) > 0 {
		l = strings.Join(lenses, ",")
	}
	return fmt.Sprintf("%s; risk %s; lenses %s", verdict, level, l)
}

// receiptLenses reads the lenses back from a ReceiptSummary.
func receiptLenses(summary string) []string {
	i := strings.LastIndex(summary, "; lenses ")
	if i < 0 {
		return nil
	}
	l := summary[i+len("; lenses "):]
	if l == "none" {
		return nil
	}
	return strings.Split(l, ",")
}

// Receipts is the review state of the staged change: the tree it would commit,
// the newest unused receipt for exactly that tree and base, and whether that
// receipt lets a change at this level through.
type Receipts struct {
	Tree, Base string
	Current    *store.Change // nil: no receipt for this exact tree
	Valid      bool
	Why        string // why Current is not valid, when it is not
	Stale      int    // unused receipts for some other tree or base
	LastUsed   *store.Change
}

// maxReceiptScan bounds how many receipts are looked through: the current one
// is almost always the newest.
const maxReceiptScan = 50

// CurrentReceipts reads the receipt state of what is staged now, for a change
// assessed at level. The receipt's signature is checked against the repository
// key before it is believed: the gate must not let through an approval someone
// wrote into the database, which verify would only catch after the commit.
func CurrentReceipts(st *store.Store, root string, level Level) (Receipts, error) {
	var r Receipts
	var err error
	if r.Tree, err = gitutil.IndexTree(); err != nil {
		return r, fmt.Errorf("read the staged tree: %w", err)
	}
	if r.Base, err = gitutil.LastCommitHash(); err != nil {
		r.Base = "" // no commit yet: the receipt's base is empty too
	}
	pending, err := st.Reviews(true, maxReceiptScan)
	if err != nil {
		return r, err
	}
	for i := range pending {
		p := pending[i]
		if p.CandidateTree == r.Tree && p.BaseCommit == r.Base {
			if r.Current == nil {
				r.Current = &p
			}
		} else {
			r.Stale++
		}
	}
	used, err := st.Reviews(false, maxReceiptScan)
	if err != nil {
		return r, err
	}
	for i := range used {
		if used[i].CommitHash != "" {
			r.LastUsed = &used[i]
			break
		}
	}
	// ExistingKey never creates a salt; without one nothing can be checked.
	key, _, keyErr := integrity.ExistingKey(root)
	r.Valid, r.Why = valid(r.Current, level, key, keyErr)
	return r, nil
}

// valid reports whether a receipt lets a change at level through: it must
// approve, and claim at least as many lenses as the level asks for. Low risk
// needs no receipt.
func valid(rec *store.Change, level Level, key []byte, keyErr error) (bool, string) {
	if level == Low {
		return true, ""
	}
	if rec == nil {
		return false, "no review receipt for the staged change"
	}
	if keyErr != nil {
		return false, "the receipt cannot be checked: " + keyErr.Error()
	}
	if want, err := integrity.ComputeHMAC(key, *rec); err != nil || want != rec.HMAC {
		return false, fmt.Sprintf("receipt #%d does not match its signature (edited in the database?); run githints verify", rec.ID)
	}
	if rec.Verdict != store.VerdictApproved {
		return false, "the review " + rec.Verdict
	}
	if n := len(receiptLenses(rec.Summary)); n < level.LensCount() {
		return false, fmt.Sprintf("the review covered %d lens(es); %s risk asks for %d", n, level, level.LensCount())
	}
	return true, ""
}

// Format renders the receipt state under a risk report.
func (r Receipts) Format() string {
	var b strings.Builder
	b.WriteString("\nReceipt: ")
	if r.Current == nil {
		b.WriteString("none for the staged change.\n")
	} else {
		fmt.Fprintf(&b, "#%d %s by %s at %s, for exactly this staged tree.\n",
			r.Current.ID, textsafe.OneLine(r.Current.Summary), who(r.Current), when(r.Current))
	}
	if r.Valid {
		b.WriteString("It lets this change through; committing uses it up.\n")
	} else {
		fmt.Fprintf(&b, "Not approved yet: %s. Review by the lenses above, then record_review (githints review record).\n", r.Why)
	}
	if r.Stale > 0 {
		fmt.Fprintf(&b, "%d unused receipt(s) are for a different staged tree or base and no longer apply.\n", r.Stale)
	}
	if r.LastUsed != nil {
		fmt.Fprintf(&b, "Last used: #%d %s, by commit %s.\n",
			r.LastUsed.ID, textsafe.OneLine(r.LastUsed.Summary), gitutil.ShortHash(r.LastUsed.CommitHash))
	}
	b.WriteString("An approval never commits: committing stays with you.\n")
	return b.String()
}

func who(c *store.Change) string {
	if c.AgentID == "" {
		return "an unnamed reviewer"
	}
	return textsafe.OneLine(c.AgentID)
}

func when(c *store.Change) string {
	return time.Unix(c.RecordedAt, 0).Format(time.RFC3339)
}
