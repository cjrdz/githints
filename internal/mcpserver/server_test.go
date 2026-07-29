package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
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

func setupIndex(t *testing.T) (string, *index.Store) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "store"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/cjrdz/githints\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	db, err := index.Open(filepath.Join(dir, ".githints", "index.db"))
	if err != nil {
		t.Fatalf("open index db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.InsertSymbols([]lang.Symbol{
		{Name: "User", Kind: lang.KindType, FilePath: "internal/store/user.go", LineStart: 1, LineEnd: 3},
		{Name: "FindUser", Kind: lang.KindFunc, FilePath: "internal/store/user.go", LineStart: 5, LineEnd: 10},
		{Name: "Search", Kind: lang.KindFunc, FilePath: "internal/api/api.go", LineStart: 1, LineEnd: 4},
	}); err != nil {
		t.Fatalf("InsertSymbols: %v", err)
	}
	if err := db.InsertImports([]lang.Import{
		{FilePath: "internal/api/api.go", ImportedPath: "github.com/cjrdz/githints/internal/store"},
	}); err != nil {
		t.Fatalf("InsertImports: %v", err)
	}
	if err := db.SetMeta(lang.IndexMeta{LastIndexedAt: 1234567890, LanguageCounts: map[string]int{"go": 2}}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	return dir, db
}

func TestHandleListSymbols(t *testing.T) {
	dir, db := setupIndex(t)
	_ = dir
	fn := handleListSymbols(db)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"file": "internal/store/user.go"}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content[0].(mcp.TextContent).Text)
	}
	text := resp.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "User") || !strings.Contains(text, "FindUser") {
		t.Errorf("response missing symbols:\n%s", text)
	}
	if !strings.Contains(text, "last indexed:") {
		t.Errorf("response missing last_indexed_at:\n%s", text)
	}
}

func TestHandleListSymbolsNilDB(t *testing.T) {
	fn := handleListSymbols(nil)
	resp, err := fn(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response for nil db")
	}
}

func TestHandleFindSymbol(t *testing.T) {
	dir, db := setupIndex(t)
	fn := handleFindSymbol(db)
	_ = dir

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "User"}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content[0].(mcp.TextContent).Text)
	}
	text := resp.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "internal/store/user.go:1") {
		t.Errorf("response missing match:\n%s", text)
	}
	if !strings.Contains(text, "last indexed:") {
		t.Errorf("response missing last_indexed_at:\n%s", text)
	}
}

func TestHandleFindSymbolPrefix(t *testing.T) {
	_, db := setupIndex(t)
	fn := handleFindSymbol(db)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "Find"}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content[0].(mcp.TextContent).Text)
	}
	text := resp.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "FindUser") {
		t.Errorf("response missing prefix match:\n%s", text)
	}
}

func TestHandleGetDependents(t *testing.T) {
	dir, db := setupIndex(t)
	fn := handleGetDependents(dir, db)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"file": "internal/store/user.go"}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content[0].(mcp.TextContent).Text)
	}
	text := resp.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "internal/api/api.go") {
		t.Errorf("response missing dependent:\n%s", text)
	}
	if !strings.Contains(text, "github.com/cjrdz/githints/internal/store") {
		t.Errorf("response missing resolved import path:\n%s", text)
	}
	if !strings.Contains(text, "last indexed:") {
		t.Errorf("response missing last_indexed_at:\n%s", text)
	}
}

func TestHandleGetIndexSummary(t *testing.T) {
	_, db := setupIndex(t)
	fn := handleGetIndexSummary(db)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": 10.0}
	resp, err := fn(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content[0].(mcp.TextContent).Text)
	}
	text := resp.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"files indexed:", "symbols:", "imports recorded:", "last indexed:", "go:"} {
		if !strings.Contains(text, want) {
			t.Errorf("response missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "internal/store") {
		t.Errorf("hub ranking missing internal/store:\n%s", text)
	}
}
