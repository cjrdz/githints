package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"githints/internal/store"
)

func TestHandleRecordBatch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	fn := handleRecordBatch(dir, st)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"changes": []any{
			map[string]any{"file": "a.go", "summary": "first"},
			map[string]any{"file": "b.go", "summary": "second", "reason": "why not"},
		},
		"agent_id": "test-agent",
	}

	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(resp.Content) == 0 {
		t.Fatalf("expected content")
	}
	text, ok := resp.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", resp.Content[0])
	}
	if text.Text != "recorded 2 changes" {
		t.Fatalf("unexpected response: %s", text.Text)
	}

	count, err := st.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestHandleRecordBatchRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	fn := handleRecordBatch(dir, st)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"changes": []any{
			map[string]any{"file": "../escape.go", "summary": "bad"},
		},
	}

	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.IsError {
		t.Fatalf("expected error response")
	}

	// Ensure unused imports don't break compilation (server is used by other tests).
	_ = server.NewMCPServer("x", "1")
}
