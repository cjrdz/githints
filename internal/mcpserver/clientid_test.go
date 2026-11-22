package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

func TestClientAgentID(t *testing.T) {
	long := strings.Repeat("é", recorder.MaxAgentIDLen)
	for _, tc := range []struct {
		name string
		info mcp.Implementation
		want string
	}{
		{"name and version", mcp.Implementation{Name: "claude-code", Version: "2.1.0"}, "client:claude-code/2.1.0"},
		{"name only", mcp.Implementation{Name: "opencode"}, "client:opencode"},
		{"no name", mcp.Implementation{Version: "1.0"}, ""},
		{"blank name", mcp.Implementation{Name: " \t"}, ""},
		{"control characters removed", mcp.Implementation{Name: "co\x1b[2Jdex\u200b", Version: "0.1\n"}, "client:co[2Jdex/0.1"},
		{"credential-shaped name dropped", mcp.Implementation{Name: "AKIA" + "IOSFODNN7EXAMPLE"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientAgentID(tc.info); got != tc.want {
				t.Errorf("clientAgentID(%+v) = %q, want %q", tc.info, got, tc.want)
			}
		})
	}

	got := clientAgentID(mcp.Implementation{Name: long})
	if len(got) > recorder.MaxAgentIDLen || !strings.HasPrefix(got, "client:é") || !utf8Valid(got) {
		t.Errorf("long name: %d bytes, %q", len(got), got)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

type fakeSession struct {
	mu   sync.Mutex
	init bool
	info mcp.Implementation
	caps mcp.ClientCapabilities
	ch   chan mcp.JSONRPCNotification
}

func (f *fakeSession) Initialize()                                         { f.mu.Lock(); f.init = true; f.mu.Unlock() }
func (f *fakeSession) Initialized() bool                                   { f.mu.Lock(); defer f.mu.Unlock(); return f.init }
func (f *fakeSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return f.ch }
func (f *fakeSession) SessionID() string                                   { return "fake" }
func (f *fakeSession) GetClientInfo() mcp.Implementation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}
func (f *fakeSession) SetClientInfo(i mcp.Implementation) { f.mu.Lock(); f.info = i; f.mu.Unlock() }
func (f *fakeSession) GetClientCapabilities() mcp.ClientCapabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps
}
func (f *fakeSession) SetClientCapabilities(c mcp.ClientCapabilities) {
	f.mu.Lock()
	f.caps = c
	f.mu.Unlock()
}

// Drives the real server through initialize and tools/call, the way a client
// does, and checks what lands in the store.
func TestWritesDefaultAgentIDToTheClient(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, closeDBs := newServer(dir, st, config.Default(), "test")
	defer closeDBs()

	sess := &fakeSession{ch: make(chan mcp.JSONRPCNotification, 10)}
	ctx := srv.WithContext(context.Background(), sess)
	send := func(method string, id int, params any) string {
		t.Helper()
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		out, _ := json.Marshal(srv.HandleMessage(ctx, msg))
		return string(out)
	}
	send("initialize", 1, map[string]any{
		"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.2.3"},
	})

	call := func(id int, name string, args map[string]any) {
		t.Helper()
		if out := send("tools/call", id, map[string]any{"name": name, "arguments": args}); strings.Contains(out, `"isError":true`) {
			t.Fatalf("%s: %s", name, out)
		}
	}
	call(2, "record_change", map[string]any{"file": "a.go", "summary": "Added retry"})
	call(3, "record_change", map[string]any{"file": "b.go", "summary": "Added retry", "agent_id": "me"})
	call(4, "record_change", map[string]any{"changes": []any{map[string]any{"file": "c.go", "summary": "Batched"}}})
	call(5, "record_outcome", map[string]any{"change_id": 1, "outcome": "worked"})

	for _, tc := range []struct {
		file string
		want []string
	}{
		{"a.go", []string{"client:test-client/1.2.3"}},
		{"b.go", []string{"me"}},
		{"c.go", []string{"client:test-client/1.2.3"}},
	} {
		rows, err := st.FileHistory(tc.file, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(tc.want) {
			t.Fatalf("%s: %d rows, want %d", tc.file, len(rows), len(tc.want))
		}
		for i, r := range rows {
			if r.AgentID != tc.want[i] {
				t.Errorf("%s row %d: agent_id %q, want %q", tc.file, i, r.AgentID, tc.want[i])
			}
		}
	}
	outcomes, err := st.OutcomesFor(1)
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("outcomes for #1: %v, %v", outcomes, err)
	}
	if got := outcomes[0].AgentID; got != "client:test-client/1.2.3" {
		t.Errorf("outcome agent_id %q", got)
	}
}
