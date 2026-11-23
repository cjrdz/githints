package mcpcfg

import (
	"os"
	"path/filepath"
	"testing"
)

// ProjectCommands reads back exactly what setup writes, for every project
// client: a JSON command string, opencode's command array, and Codex's TOML,
// with a path that needs escaping in each.
func TestProjectCommandsReadsWhatSetupWrites(t *testing.T) {
	root := t.TempDir()
	cmd := `/opt/my tools/git"hints\bin`
	ctx := EntryCtx{Command: cmd, Root: root}
	want := map[string]bool{}
	for _, c := range clientList {
		if c.scope != ScopeProject {
			continue
		}
		paths, err := c.paths(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			if want[p] {
				continue // a shared file (Claude Code and Pi)
			}
			want[p] = true
			body := SnippetJSON(c, "githints", ctx)
			if c.toml {
				body = SnippetTOML("githints", ctx)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := ProjectCommands(root)
	for _, c := range got {
		if c.Command != cmd {
			t.Errorf("%s: command %q, want %q", c.Path, c.Command, cmd)
		}
		delete(want, c.Path)
	}
	if len(want) != 0 {
		t.Errorf("no command read from %v", want)
	}
}

// Files it cannot read with confidence are skipped, never guessed at.
func TestProjectCommandsSkipsWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".mcp.json", `{"mcpServers":{"other":{"command":"/x/other"}}}`)       // no githints entry
	write(".zed/settings.json", "// comments: JSONC\n{\"context_servers\":{}}") // not plain JSON
	write(".codex/config.toml", "[mcp_servers.other]\ncommand = \"/x/other\"\n\n[mcp_servers.githints]\nargs = [\"serve\"]\n")
	write(".vscode/mcp.json", `{"servers":{"githints":{"command":["githints"]}}}`) // array form also accepted
	got := ProjectCommands(root)
	if len(got) != 1 || got[0].Command != "githints" || filepath.Base(got[0].Path) != "mcp.json" {
		t.Errorf("ProjectCommands = %+v; want only the vscode entry", got)
	}
}
