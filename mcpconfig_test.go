package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigWriteMergesAndKeepsOtherServers(t *testing.T) {
	dir := chdirTempRepo(t)
	existing := `{"mcpServers":{"other":{"command":"x"}},"keep":true}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCPConfig([]string{"claude", "-write"}); err != nil {
		t.Fatalf("mcp-config claude -write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]any `json:"mcpServers"`
		Keep       bool           `json:"keep"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, data)
	}
	if doc.MCPServers["other"] == nil || doc.MCPServers["githints"] == nil || !doc.Keep {
		t.Fatalf("merge lost or missed an entry:\n%s", data)
	}

	// Idempotent.
	if err := cmdMCPConfig([]string{"-write", "claude"}); err != nil {
		t.Fatalf("second -write: %v", err)
	}
}

func TestMCPConfigCreatesNestedFile(t *testing.T) {
	dir := chdirTempRepo(t)
	if err := cmdMCPConfig([]string{"gemini", "-write"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	if err != nil || !strings.Contains(string(data), `"githints"`) {
		t.Fatalf("gemini settings: %v\n%s", err, data)
	}
}

// opencode accepts JSON with comments; rewriting it through encoding/json
// would drop them, so it is refused instead.
func TestMCPConfigRefusesNonJSON(t *testing.T) {
	dir := chdirTempRepo(t)
	jsonc := "{\n  // mine\n  \"mcp\": {}\n}\n"
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(jsonc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCPConfig([]string{"opencode", "-write"}); err == nil {
		t.Fatal("rewrote a file with comments")
	}
	if got, _ := os.ReadFile(path); string(got) != jsonc {
		t.Fatal("file was modified")
	}
}

func TestMCPConfigRejectsUnknownClientAndCodexWrite(t *testing.T) {
	chdirTempRepo(t)
	if err := cmdMCPConfig([]string{"nope"}); err == nil {
		t.Error("unknown client accepted")
	}
	if err := cmdMCPConfig([]string{"codex", "-write"}); err == nil {
		t.Error("codex -write accepted; its config is global")
	}
}
