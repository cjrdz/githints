package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
)

// Each client's documented shape (checked against vendor docs, 2026-10).
func TestClientEntryShapes(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	// claude and pi both target .mcp.json; the second registration must be a
	// no-op, not an error.
	if err := Setup([]string{"-clients=claude,pi,opencode,vscode,cursor,zed,kiro,gemini,junie,continue"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	get := func(rel string, keys ...string) map[string]any {
		m := clitest.ReadJSON(t, filepath.Join(dir, rel))
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
	if err := Setup([]string{"-clients=vscode"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".vscode", "mcp.json"))
	if string(before) != string(after) {
		t.Error("second setup rewrote the file")
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := Setup([]string{"-dry-run", "-clients=all"}); err != nil {
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
	dir := clitest.ChdirTempRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".vscode"), 0o755); err != nil { // ...but this is
		t.Fatal(err)
	}
	if err := Setup(nil); err != nil {
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

// A .pi directory in the repo marks Pi in use even without the binary, and
// setup registers the shared .mcp.json the pi-mcp-adapter extension reads.
func TestSetupDetectsPiDir(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, filepath.Base(gitPath))); err != nil {
		t.Skipf("cannot link git: %v", err)
	}
	t.Setenv("PATH", bin)
	dir := clitest.ChdirTempRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Setup(nil); err != nil {
		t.Fatal(err)
	}
	servers := clitest.ReadJSON(t, filepath.Join(dir, ".mcp.json"))["mcpServers"].(map[string]any)
	if _, ok := servers["githints"]; !ok {
		t.Errorf("detected Pi was not registered: %v", servers)
	}
	if exists(filepath.Join(dir, ".zed")) {
		t.Error("an undetected client was registered")
	}
}

// Global configs belong to every project on the machine: written only when
// named, under a per-repo name, with a backup, and only if the client exists.
func TestGlobalClientWritesPerRepoEntryWithBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("CLINE_MCP_SETTINGS_PATH", "")
	clitest.ChdirTempRepo(t)
	// The repository root as git reports it, which is what setup pins: it
	// resolves symlinks (/private/var on macOS) and uses forward slashes on
	// Windows, so it can differ from the temp dir's own path.
	root, err := gitutil.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	if err := mcpcfg.MCPConfig([]string{"windsurf", "-write"}); err == nil {
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
	if err := Setup(nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfg); string(got) != orig {
		t.Fatal("setup wrote a global config without being asked")
	}

	if err := Setup([]string{"-clients=windsurf"}); err != nil {
		t.Fatal(err)
	}
	servers := clitest.ReadJSON(t, cfg)["mcpServers"].(map[string]any)

	var e map[string]any
	for k, v := range servers {
		if strings.HasPrefix(k, "githints-") {
			e, _ = v.(map[string]any)
			break
		}
	}
	if e == nil || servers["other"] == nil {
		t.Fatalf("entries: %v", servers)
	}
	if !strings.Contains(fmt.Sprint(e["args"]), "-root="+root) || !filepath.IsAbs(fmt.Sprint(e["command"])) {
		t.Errorf("global entry must pin the root and use an absolute command: %v", e)
	}
	if bak, err := os.ReadFile(cfg + ".githints-backup"); err != nil || string(bak) != orig {
		t.Errorf("backup: %v %q", err, bak)
	}
	// Unix permission bits do not exist on Windows (Go reports 0666).
	if fi, _ := os.Stat(cfg); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %o", fi.Mode().Perm())
	}
}

// -update replaces only githints' own entry; without it an existing entry is
// never touched.
func TestSetupUpdateReplacesOnlyOwnEntry(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	path := filepath.Join(dir, ".mcp.json")
	stale := `{"mcpServers":{"githints":{"command":"/old/githints","args":["serve"]},"other":{"command":"x"}}}`
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Setup([]string{"-clients=claude"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != stale {
		t.Fatal("existing entry changed without -update")
	}
	if err := Setup([]string{"-clients=claude", "-update"}); err != nil {
		t.Fatal(err)
	}
	servers := clitest.ReadJSON(t, path)["mcpServers"].(map[string]any)
	if fmt.Sprint(servers["githints"].(map[string]any)["command"]) == "/old/githints" || servers["other"] == nil {
		t.Fatalf("after -update: %v", servers)
	}
}
