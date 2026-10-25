package core

import (
	"fmt"
	"sort"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

// MaxLimit is the largest -limit any CLI command accepts, the same ceiling
// the MCP tools apply.
const MaxLimit = 500

// CheckLimit rejects a -limit outside 1..MaxLimit. The CLI refuses rather
// than clamps: a person who typed -limit=1000 should hear that it is not
// what they get. (A negative LIMIT would mean "unbounded" to SQLite.)
func CheckLimit(n int) error {
	if n < 1 || n > MaxLimit {
		return fmt.Errorf("-limit must be between 1 and %d, got %d", MaxLimit, n)
	}
	return nil
}

// PrintChanges prints changes, or empty when there are none.
func PrintChanges(changes []store.Change, empty string, withReason bool) {
	if len(changes) == 0 {
		fmt.Println(empty)
		return
	}
	fmt.Print(store.FormatChanges(changes, withReason))
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
  - neither recoverable? githints salt rotate -force (discards tamper evidence)`, email)
}
