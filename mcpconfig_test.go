package main

import (
	"encoding/json"
	"fmt"
	"github.com/cjrdz/githints/internal/gitutil"
	"os"
	"os/exec"
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

func TestMCPConfigRejectsUnknownClientAndManualWrite(t *testing.T) {
	chdirTempRepo(t)
	if err := cmdMCPConfig([]string{"nope"}); err == nil {
		t.Error("unknown client accepted")
	}
	if err := cmdMCPConfig([]string{"goose", "-write"}); err == nil {
		t.Error("goose -write accepted; its config is YAML and is not written")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, data)
	}
	return doc
}

// Each client's documented shape (checked against vendor docs, 2026-10).
func TestClientEntryShapes(t *testing.T) {
	dir := chdirTempRepo(t)
	if err := cmdSetup([]string{"-clients=claude,opencode,vscode,cursor,zed,kiro,gemini,junie,continue"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	get := func(rel string, keys ...string) map[string]any {
		m := readJSON(t, filepath.Join(dir, rel))
		for _, k := range keys {
			next, ok := m[k].(map[string]any)
			if !ok {
				t.Fatalf("%s: missing %s in %v", rel, k, m)
			}
			m = next
		}
		return m
	}
	if e := get(".mcp.json", "mcpServers", "githints"); e["type"] != "stdio" {
		t.Errorf("claude: %v", e)
	}
	if e := get("opencode.json", "mcp", "githints"); e["type"] != "local" || e["enabled"] != true {
		t.Errorf("opencode: %v", e)
	} else if cmd, _ := e["command"].([]any); len(cmd) != 2 || cmd[1] != "serve" {
		t.Errorf("opencode command must be an array with serve: %v", e)
	}
	if e := get(".vscode/mcp.json", "servers", "githints"); e["type"] != "stdio" {
		t.Errorf("vscode uses servers + type stdio: %v", e)
	}
	if e := get(".cursor/mcp.json", "mcpServers", "githints"); e["type"] != "stdio" || !strings.Contains(fmt.Sprint(e["args"]), "${workspaceFolder}") {
		t.Errorf("cursor: %v", e)
	}
	if e := get(".zed/settings.json", "context_servers", "githints"); e["source"] != nil || e["args"] == nil {
		t.Errorf("zed must be the flat shape: %v", e)
	}
	if e := get(".kiro/settings/mcp.json", "mcpServers", "githints"); !strings.Contains(fmt.Sprint(e["args"]), "-root=") {
		t.Errorf("kiro pins the root: %v", e)
	}
	get(".gemini/settings.json", "mcpServers", "githints")
	get(".junie/mcp/mcp.json", "mcpServers", "githints")
	get(".continue/mcpServers/githints.json", "mcpServers", "githints")

	// Idempotent: a second run changes nothing.
	before, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if err := cmdSetup([]string{"-clients=vscode"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if string(before) != string(after) {
		t.Error("second setup rewrote the file")
	}
}

// Codex rejects unknown fields, so the table carries only command and args,
// and the existing file is appended to, never rewritten.
func TestCodexTOMLAppendOnly(t *testing.T) {
	dir := chdirTempRepo(t)
	path := filepath.Join(dir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# mine\nmodel = \"o4\"\n\n[mcp_servers.other]\ncommand = \"x\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdMCPConfig([]string{"codex", "-write"}); err != nil {
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
	if err := cmdMCPConfig([]string{"codex", "-write"}); err != nil {
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

// Global configs belong to every project on the machine: written only when
// named, under a per-repo name, with a backup, and only if the client exists.
func TestGlobalClientWritesPerRepoEntryWithBackup(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("CLINE_MCP_SETTINGS_PATH", "")
	chdirTempRepo(t)
	// The repository root as git reports it, which is what setup pins: it
	// resolves symlinks (/private/var on macOS) and uses forward slashes on
	// Windows, so it can differ from the temp dir's own path.
	root, err := gitutil.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmdMCPConfig([]string{"windsurf", "-write"}); err == nil {
		t.Fatal("wrote a config for a client that is not installed")
	}

	cfg := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := `{"mcpServers":{"other":{"command":"x"}}}`
	if err := os.WriteFile(cfg, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	// setup with no -clients never touches global configs.
	if err := cmdSetup(nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfg); string(got) != orig {
		t.Fatal("setup wrote a global config without being asked")
	}

	if err := cmdSetup([]string{"-clients=windsurf"}); err != nil {
		t.Fatal(err)
	}
	servers := readJSON(t, cfg)["mcpServers"].(map[string]any)
	name := globalServerName(root)
	e, ok := servers[name].(map[string]any)
	if !ok || servers["other"] == nil {
		t.Fatalf("entries: %v", servers)
	}
	if !strings.Contains(fmt.Sprint(e["args"]), "-root="+root) || !filepath.IsAbs(fmt.Sprint(e["command"])) {
		t.Errorf("global entry must pin the root and use an absolute command: %v", e)
	}
	if bak, err := os.ReadFile(cfg + ".githints-backup"); err != nil || string(bak) != orig {
		t.Errorf("backup: %v %q", err, bak)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %o", fi.Mode().Perm())
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)
	if err := cmdSetup([]string{"-dry-run", "-clients=all"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".githints", ".mcp.json", ".vscode", "opencode.json", ".codex"} {
		if exists(filepath.Join(dir, p)) {
			t.Errorf("dry run created %s", p)
		}
	}
}

func TestSetupInitializesAndDetects(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	// A PATH with git and nothing else, so no client is detected from PATH...
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	// Keep the name, so Windows finds git.exe.
	if err := os.Symlink(gitPath, filepath.Join(bin, filepath.Base(gitPath))); err != nil {
		t.Skipf("cannot link git: %v", err)
	}
	t.Setenv("PATH", bin)
	dir := chdirTempRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".vscode"), 0o755); err != nil { // ...but this is
		t.Fatal(err)
	}
	if err := cmdSetup(nil); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, ".githints", "store.db")) {
		t.Error("setup did not initialize")
	}
	if !exists(filepath.Join(dir, ".vscode", "mcp.json")) {
		t.Error("detected VS Code was not registered")
	}
	if exists(filepath.Join(dir, ".zed")) {
		t.Error("an undetected client was registered")
	}
}

// -update replaces only githints' own entry; without it an existing entry is
// never touched.
func TestSetupUpdateReplacesOnlyOwnEntry(t *testing.T) {
	dir := chdirTempRepo(t)
	path := filepath.Join(dir, ".mcp.json")
	stale := `{"mcpServers":{"githints":{"command":"/old/githints","args":["serve"]},"other":{"command":"x"}}}`
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetup([]string{"-clients=claude"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != stale {
		t.Fatal("existing entry changed without -update")
	}
	if err := cmdSetup([]string{"-clients=claude", "-update"}); err != nil {
		t.Fatal(err)
	}
	servers := readJSON(t, path)["mcpServers"].(map[string]any)
	if fmt.Sprint(servers["githints"].(map[string]any)["command"]) == "/old/githints" || servers["other"] == nil {
		t.Fatalf("after -update: %v", servers)
	}
}
