package recorder

import (
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/store"
)

// Hints is advice on a change that was recorded anyway, never a reason to
// refuse it: refusing a weak summary only makes an agent retry with another
// weak one. prevSummary is the file's latest change before this one. The MCP
// tool and the CLI show the same hints.
func Hints(file, summary, reason, prevSummary string) []string {
	var hints []string
	if strings.TrimSpace(reason) == "" {
		hints = append(hints, "no reason: say why (the evidence, bug or constraint behind it); it is what the next reader needs most")
	}
	if isGenericSummary(file, summary) {
		hints = append(hints, `generic summary: say what specifically changed, e.g. "Replaced the linear scan in FindUser with a map lookup"`)
	}
	if prevSummary != "" && strings.EqualFold(strings.TrimSpace(prevSummary), strings.TrimSpace(summary)) {
		hints = append(hints, "same summary as the previous change to this file: say what is different this time")
	}
	return hints
}

// PreviousSummary is the summary of file's latest recorded change, or "".
// Call it before recording: afterwards the latest change is the new one.
func PreviousSummary(st *store.Store, file string) string {
	prev, err := st.FileHistory(file, 1)
	if err != nil || len(prev) == 0 {
		return "" // no comparison; the hint is optional
	}
	return prev[0].Summary
}

// genericVerbs open summaries that say nothing when little follows them.
var genericVerbs = map[string]bool{
	"update": true, "updated": true, "updates": true,
	"fix": true, "fixed": true, "fixes": true,
	"change": true, "changed": true, "changes": true,
	"modify": true, "modified": true, "edit": true, "edited": true,
	"refactor": true, "refactored": true, "tweak": true, "tweaked": true,
	"wip": true, "misc": true, "cleanup": true,
}

// isGenericSummary reports a summary that names no specific change: the file
// name alone, or a generic verb followed by at most two words ("Updated
// function", "fix bug"). "Fix the race in token refresh" is specific enough.
func isGenericSummary(file, summary string) bool {
	s := strings.TrimSpace(summary)
	if strings.EqualFold(s, file) || strings.EqualFold(s, filepath.Base(file)) {
		return true
	}
	words := strings.Fields(strings.ToLower(strings.TrimRight(s, ".!")))
	return len(words) > 0 && len(words) <= 3 && genericVerbs[words[0]]
}
