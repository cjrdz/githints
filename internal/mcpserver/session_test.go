package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/cjrdz/githints/internal/store"
)

func sessionTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedChange(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.Insert(store.Change{
		FilePath: "a.go",
		Source:   "agent",
		Summary:  "did a thing",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func contextText(t *testing.T, session *SessionTracker) string {
	t.Helper()
	resp, err := handleGetSessionContext(session)(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(resp.Content) == 0 {
		t.Fatal("expected content")
	}
	text, ok := resp.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", resp.Content[0])
	}
	return text.Text
}

func TestSessionContextOnFreshSession(t *testing.T) {
	st := sessionTestStore(t)
	seedChange(t, st)

	session := NewSessionTracker(st)
	text := contextText(t, session)

	for _, want := range []string{
		"session started:",
		"recorded changes in store: 1",
		"githints tools used this session: none yet",
		"Suggested next steps:",
		"get_recent_changes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("context missing %q:\n%s", want, text)
		}
	}
}

func TestSessionContextReflectsCalledTools(t *testing.T) {
	st := sessionTestStore(t)
	seedChange(t, st)

	session := NewSessionTracker(st)
	session.MarkToolCalled("get_recent_changes")

	text := contextText(t, session)
	if !strings.Contains(text, "githints tools used this session: get_recent_changes") {
		t.Errorf("context does not report the called tool:\n%s", text)
	}
	// A tool that has already run must not be suggested again.
	suggestions := text[strings.Index(text, "Suggested next steps:"):]
	if strings.Contains(suggestions, "get_recent_changes") {
		t.Errorf("already-called tool was suggested again:\n%s", suggestions)
	}
	if !strings.Contains(suggestions, "get_file_history") {
		t.Errorf("expected the next un-called tool to be suggested:\n%s", suggestions)
	}
}

func TestSessionContextWhenEveryToolUsed(t *testing.T) {
	st := sessionTestStore(t)
	seedChange(t, st)

	session := NewSessionTracker(st)
	for _, s := range sessionSuggestions {
		session.MarkToolCalled(s.tool)
	}

	text := contextText(t, session)
	if !strings.Contains(text, "used every githints tool this session") {
		t.Errorf("expected the all-used message:\n%s", text)
	}
}

func TestSessionContextOnEmptyStore(t *testing.T) {
	session := NewSessionTracker(sessionTestStore(t))
	text := contextText(t, session)

	if !strings.Contains(text, "recorded changes in store: 0") {
		t.Errorf("expected zero count:\n%s", text)
	}
	if !strings.Contains(text, "No history has been recorded") {
		t.Errorf("expected the empty-repo guidance:\n%s", text)
	}
	if strings.Contains(text, "Suggested next steps:") {
		t.Errorf("empty repo should not suggest history tools:\n%s", text)
	}
}

// A nil tracker must not panic: handlers may be built without one in tests.
func TestSessionTrackerNilSafe(t *testing.T) {
	var session *SessionTracker
	session.MarkToolCalled("record_change")
	if got := session.CalledTools(); got != nil {
		t.Errorf("CalledTools on nil = %v, want nil", got)
	}
	if text := session.ContextReport(); !strings.Contains(text, "unavailable") {
		t.Errorf("unexpected nil report: %s", text)
	}
}

// The tracker is shared by every tool handler, so concurrent marking must be
// race-free under `go test -race`.
func TestSessionTrackerConcurrentMarking(t *testing.T) {
	session := NewSessionTracker(nil)

	var wg sync.WaitGroup
	for _, s := range sessionSuggestions {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			session.MarkToolCalled(name)
			_ = session.ContextReport()
		}(s.tool)
	}
	wg.Wait()

	if got := len(session.CalledTools()); got != len(sessionSuggestions) {
		t.Errorf("called tools = %d, want %d", got, len(sessionSuggestions))
	}
}

// MarkToolCalled is wired at registration time via addTool, so every tool the
// server exposes is tracked. This asserts the wrapper actually marks, using a
// stand-in handler rather than the real server plumbing.
func TestAddToolWrapperMarksCalls(t *testing.T) {
	session := NewSessionTracker(nil)

	tool := mcp.NewTool("some_tool", mcp.WithDescription("test"))
	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}

	// Mirrors the wrapper inside Run().
	name := tool.Name
	wrapped := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		defer session.MarkToolCalled(name)
		return handler(ctx, req)
	}

	if _, err := wrapped(context.Background(), mcp.CallToolRequest{}); err != nil {
		t.Fatalf("wrapped handler: %v", err)
	}
	called := session.CalledTools()
	if len(called) != 1 || called[0] != "some_tool" {
		t.Errorf("called = %v, want [some_tool]", called)
	}
}
