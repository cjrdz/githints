package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/cjrdz/githints/internal/store"
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

func TestHandleGetDiffRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	fn := handleGetDiff(nil)

	cases := []struct {
		name    string
		file    string
		hash    string
		wantErr string
	}{
		{"path traversal", "../escape.go", "", "file path must be a local"},
		{"flag-like hash", "main.go", "--output=/tmp/pwn", "invalid commit hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{"file": tc.file, "hash": tc.hash}
			resp, err := fn(context.Background(), req)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !resp.IsError {
				t.Fatal("expected error response")
			}
			text := resp.Content[0].(mcp.TextContent)
			if !strings.Contains(text.Text, tc.wantErr) {
				t.Fatalf("expected %q in error, got %q", tc.wantErr, text.Text)
			}
		})
	}
}

func TestHandleFileHistoryRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	fn := handleFileHistory(st)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"file": "../escape.go"}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response")
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct {
		n, def, max, want int
	}{
		{0, 10, 500, 10},
		{-5, 10, 500, 10},
		{5, 10, 500, 5},
		{100, 10, 500, 100},
		{501, 10, 500, 500},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d-%d-%d", tt.n, tt.def, tt.max), func(t *testing.T) {
			if got := clampLimit(tt.n, tt.def, tt.max); got != tt.want {
				t.Errorf("clampLimit(%d,%d,%d) = %d, want %d", tt.n, tt.def, tt.max, got, tt.want)
			}
		})
	}
}
