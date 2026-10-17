package core

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// PrintChanges writes one line per change, newest data as the store returned
// it. Rows written before record-time sanitizing can still hold terminal
// escapes, so every recorded field is flattened on output too.
func PrintChanges(changes []store.Change, empty string, withReason bool) {
	if len(changes) == 0 {
		fmt.Println(empty)
		return
	}
	for _, c := range changes {
		when := time.Unix(c.RecordedAt, 0).Format(time.RFC3339)
		fmt.Fprintf(os.Stdout, "[%s] ", when)
		if c.ID > 0 {
			fmt.Fprintf(os.Stdout, "#%d ", c.ID) // what `githints outcome -id` takes
		}
		fmt.Fprintf(os.Stdout, "%s (%s", textsafe.OneLine(c.FilePath), c.Source)
		if c.AgentID != "" {
			fmt.Fprintf(os.Stdout, ", %s", textsafe.OneLine(c.AgentID))
		}
		if c.ClockTamperWarning {
			fmt.Fprintf(os.Stdout, " [CLOCK TAMPER WARNING]")
		}
		fmt.Fprintf(os.Stdout, ", %s): %s\n", ShortCommitHash(c.CommitHash), textsafe.OneLine(c.Summary))
		if withReason && c.Reason != "" {
			fmt.Fprintf(os.Stdout, "    why: %s\n", textsafe.OneLine(c.Reason))
		}
		if o := c.LatestOutcome; o != nil {
			fmt.Fprintf(os.Stdout, "    outcome: %s", textsafe.OneLine(o.OutcomeState))
			if o.Source != "agent" {
				fmt.Fprint(os.Stdout, " (auto-detected)")
			}
			if o.Summary != "" {
				fmt.Fprintf(os.Stdout, " — %s", textsafe.OneLine(o.Summary))
			}
			fmt.Fprintln(os.Stdout)
		}
	}
}

// ShortCommitHash returns a short prefix of a commit hash, or a placeholder.
func ShortCommitHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	if h == "" {
		return "uncommitted"
	}
	return h
}

// SortedStringKeys returns the keys of m in sorted order.
func SortedStringKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseTimestamp accepts either a Unix-seconds integer or an RFC3339 string.
func ParseTimestamp(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("timestamp is required")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("expected Unix seconds or RFC3339 timestamp")
}

// KeyChangedHint explains the most common cause of a wholesale verify failure.
// When every signed row fails to recompute, the rows are almost certainly
// fine and the key is different: it is derived from the salt and from git's
// user.email, and either may have changed since the rows were signed.
func KeyChangedHint(rows []store.Change, errs []integrity.IntegrityError) string {
	signed, failed := 0, 0
	for _, r := range rows {
		if r.HMAC != "" {
			signed++
		}
	}
	for _, e := range errs {
		if e.Problem == integrity.ProblemHMACMismatch {
			failed++
		}
	}
	if signed < 2 || failed != signed {
		return ""
	}
	email, _ := gitutil.UserEmail()
	return fmt.Sprintf(`
Every signed row fails, which usually means the integrity key changed rather
than the rows. The key is derived from the salt and from git user.email
(currently %q):
  - changed user.email? set it back for this repo: git config user.email <old>
  - moved machines or lost the salt? githints salt import <file>
  - neither recoverable? githints rotate-salt -force (discards tamper evidence)`, email)
}
