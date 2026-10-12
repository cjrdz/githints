package mcpcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/clitest"
)

func TestMCPConfigWriteMergesAndKeepsOtherServers(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	existing := `{"mcpServers":{"other":{"command":"x"}},"keep":true}`
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MCPConfig([]string{"claude", "-write"}); err != nil {
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
	if err := MCPConfig([]string{"-write", "claude"}); err != nil {
		t.Fatalf("second -write: %v", err)
	}
}

func TestMCPConfigCreatesNestedFile(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	if err := MCPConfig([]string{"gemini", "-write"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	if err != nil || !strings.Contains(string(data), `"githints"`) {
		t.Fatalf("gemini settings: %v\n%s", err, data)
	}
}

// Pi's pi-mcp-adapter reads the shared .mcp.json format, so Pi gets the same
// stdio entry as Claude Code.
func TestMCPConfigPiWritesSharedMCPJSON(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	if err := MCPConfig([]string{"pi", "-write"}); err != nil {
		t.Fatal(err)
	}
	servers := clitest.ReadJSON(t, filepath.Join(dir, ".mcp.json"))["mcpServers"].(map[string]any)
	e, ok := servers["githints"].(map[string]any)
	if !ok || e["type"] != "stdio" || e["command"] == nil || e["args"] == nil {
		t.Fatalf("pi entry must be a stdio server with command and args: %v", servers)
	}
}

// opencode accepts JSON with comments; rewriting it through encoding/json
// would drop them, so it is refused instead.
func TestMCPConfigRefusesNonJSON(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	jsonc := "{\n  // mine\n  \"mcp\": {}\n}\n"
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(jsonc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MCPConfig([]string{"opencode", "-write"}); err == nil {
		t.Fatal("rewrote a file with comments")
	}
	if got, _ := os.ReadFile(path); string(got) != jsonc {
		t.Fatal("file was modified")
	}
}

func TestMCPConfigRejectsUnknownClientAndManualWrite(t *testing.T) {
	clitest.ChdirTempRepo(t)
	if err := MCPConfig([]string{"nope"}); err == nil {
		t.Error("unknown client accepted")
	}
	if err := MCPConfig([]string{"goose", "-write"}); err == nil {
		t.Error("goose -write accepted; its config is YAML and is not written")
	}
}

// Codex rejects unknown fields, so the table carries only command and args,
// and the existing file is appended to, never rewritten.
func TestCodexTOMLAppendOnly(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	path := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# mine\nmodel = \"o4\"\n\n[mcp_servers.other]\ncommand = \"x\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MCPConfig([]string{"codex", "-write"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), orig) {
		t.Fatalf("existing content changed:\n%s", got)
	}
	tail := strings.TrimPrefix(string(got), orig)
	if !strings.Contains(tail, "[mcp_servers.githints]") || !strings.Contains(tail, `args = ["serve", "-root=`) || strings.Contains(tail, "type") {
		t.Fatalf("appended table:\n%s", tail)
	}
	if err := MCPConfig([]string{"codex", "-write"}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(got) {
		t.Fatal("second write appended a duplicate table")
	}
	if tomlString(`C:\a "b"`) != `"C:\\a \"b\""` {
		t.Errorf("tomlString = %s", tomlString(`C:\a "b"`))
	}
}

// Claude Code and Pi share .mcp.json; doctor must not list the file twice.
func TestProjectConfigFilesDeduplicatesSharedFiles(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	seen := map[string]int{}
	for _, p := range ProjectConfigFiles(dir) {
		seen[p]++
	}
	for p, n := range seen {
		if n > 1 {
			t.Errorf("%s listed %d times", p, n)
		}
	}
}
