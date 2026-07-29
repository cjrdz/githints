// Session tracking for the MCP server.
//
// Scope and lifetime, deliberately: session state lives in memory for the
// lifetime of one MCP server process and is never persisted. If the server
// restarts the state resets, which is correct — a new process is a new
// session, and a stale "you already called this" claim would be worse than
// no claim at all.
//
// The MCP stdio transport is single-session by construction: one process
// serves one client over one pair of pipes. If two agents were somehow wired
// to the same stdio they would share this state. That is an inherent property
// of the transport, not something the tracker can fix, so it is documented
// rather than worked around.
package mcpserver

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cjrdz/githints/internal/store"
)

// sessionSuggestions lists the tools worth calling at session start, in the
// order an agent should consider them. Only tools that have not been called
// yet are suggested, so the advice shrinks as the session progresses instead
// of repeating itself.
var sessionSuggestions = []struct {
	tool string
	why  string
}{
	{"get_recent_changes", "catch up on what changed since you were last here, including work from other agents or plain git commits"},
	{"get_file_history", "before editing an unfamiliar file, find out why it is shaped the way it is"},
	{"search_changes", "full-text search over past summaries and reasons when hunting for a specific decision"},
	{"get_diff", "check the real unified diff when a recorded summary looks vague or wrong"},
	{"get_changes_in_range", "timeline forensics for one window, e.g. what happened to auth.go last week"},
	{"record_change", "record what you changed and why, right after each file edit"},
	{"record_batch", "record several files at once when they changed in the same conceptual step"},
}

// maxSessionSuggestions caps how many suggestions are returned, so the
// response stays a nudge rather than a wall of text the agent skims past.
const maxSessionSuggestions = 3

// SessionTracker records which githints tools have been used during this
// server process, so get_session_context can tell an agent what it has and
// has not done yet. It is safe for concurrent use.
type SessionTracker struct {
	startedAt time.Time

	mu     sync.Mutex
	called map[string]bool

	// changeCount is how many changes the store held when the session began.
	// It answers "is there any history worth consulting?" without re-querying
	// on every get_session_context call.
	changeCount int
}

// NewSessionTracker starts a session and samples the store so the first
// context report can say whether any history exists. A nil store is allowed
// (the count is simply reported as zero), which keeps the tracker usable in
// tests that do not need a store.
func NewSessionTracker(st *store.Store) *SessionTracker {
	t := &SessionTracker{
		startedAt: time.Now(),
		called:    make(map[string]bool),
	}
	if st != nil {
		count, err := st.Count()
		if err != nil {
			// Non-fatal: the session still works, it just cannot report how
			// much history exists.
			fmt.Fprintf(os.Stderr, "githints: session context: count changes: %v\n", err)
		} else {
			t.changeCount = count
		}
	}
	return t
}

// MarkToolCalled records that a tool ran during this session. A nil receiver
// is a no-op so handlers can be constructed without a tracker.
func (t *SessionTracker) MarkToolCalled(name string) {
	if t == nil || name == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.called[name] = true
}

// CalledTools returns the tools used this session, sorted for stable output.
func (t *SessionTracker) CalledTools() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	names := make([]string, 0, len(t.called))
	for name := range t.called {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ContextReport renders the session state as human-readable text: when the
// session started, what has been called, how much history exists, and which
// tools to consider next. The response is text rather than JSON to match the
// other tools, which all return prose an agent reads directly.
func (t *SessionTracker) ContextReport() string {
	if t == nil {
		return "session tracking is unavailable in this server process"
	}

	t.mu.Lock()
	started := t.startedAt
	changeCount := t.changeCount
	called := make(map[string]bool, len(t.called))
	for name := range t.called {
		called[name] = true
	}
	t.mu.Unlock()

	var b strings.Builder
	b.WriteString("githints session context\n\n")
	fmt.Fprintf(&b, "- session started: %s (%s ago)\n",
		started.Format(time.RFC3339), time.Since(started).Round(time.Second))
	fmt.Fprintf(&b, "- recorded changes in store: %d\n", changeCount)

	usedNames := make([]string, 0, len(called))
	for name := range called {
		usedNames = append(usedNames, name)
	}
	sort.Strings(usedNames)
	if len(usedNames) == 0 {
		b.WriteString("- githints tools used this session: none yet\n")
	} else {
		fmt.Fprintf(&b, "- githints tools used this session: %s\n", strings.Join(usedNames, ", "))
	}

	if changeCount == 0 {
		b.WriteString("\nNo history has been recorded in this repo yet. Start recording as you " +
			"work: call record_change after each file you edit so the next session has context.\n")
		return b.String()
	}

	b.WriteString("\nSuggested next steps:\n")
	shown := 0
	for _, s := range sessionSuggestions {
		if shown >= maxSessionSuggestions {
			break
		}
		if called[s.tool] {
			continue
		}
		fmt.Fprintf(&b, "- %s — %s\n", s.tool, s.why)
		shown++
	}
	if shown == 0 {
		b.WriteString("- you have used every githints tool this session; " +
			"keep calling record_change after each edit so the history stays complete\n")
	}

	return b.String()
}
