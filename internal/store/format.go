package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/textsafe"
)

// FormatChanges renders rows one per line, with each row's latest outcome
// under it. The CLI and the MCP server both print through it, so a terminal
// and an agent see the same listing.
//
// Recorded text is written by other agents and people, so every field is
// flattened to one line: a summary containing a newline could otherwise forge
// another entry, and rows written before record-time sanitizing can still hold
// terminal escapes.
func FormatChanges(changes []Change, withReason bool) string {
	var b strings.Builder
	for _, c := range changes {
		when := "unknown"
		if c.RecordedAt != 0 {
			when = time.Unix(c.RecordedAt, 0).Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "[%s] ", when)
		if c.ID > 0 {
			fmt.Fprintf(&b, "#%d ", c.ID) // what record_outcome and `githints outcome -id` take
		}
		fmt.Fprintf(&b, "%s (%s", textsafe.OneLine(c.FilePath), textsafe.OneLine(c.Source))
		if c.Branch != "" {
			fmt.Fprintf(&b, ", %s", textsafe.OneLine(c.Branch))
		}
		if c.AgentID != "" {
			fmt.Fprintf(&b, ", %s", textsafe.OneLine(c.AgentID))
		}
		if c.ClockTamperWarning {
			b.WriteString(" [CLOCK TAMPER WARNING]")
		}
		hash := "uncommitted"
		if c.CommitHash != "" {
			hash = gitutil.ShortHash(c.CommitHash)
		}
		fmt.Fprintf(&b, ", %s): %s\n", hash, textsafe.OneLine(c.Summary))
		if withReason && c.Reason != "" {
			fmt.Fprintf(&b, "    why: %s\n", textsafe.OneLine(c.Reason))
		}
		if o := c.LatestOutcome; o != nil {
			fmt.Fprintf(&b, "    outcome: %s", textsafe.OneLine(o.OutcomeState))
			if o.Source != "agent" {
				b.WriteString(" (auto-detected)")
			}
			if o.Summary != "" {
				fmt.Fprintf(&b, " — %s", textsafe.OneLine(o.Summary))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ParseTimestamp accepts Unix seconds, RFC3339, or a bare date, the forms
// the range queries take.
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
