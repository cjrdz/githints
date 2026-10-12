package teardown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
)

func TestUninstallRequiresForce(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	initManagedHooks(t, dir)
	if err := Uninstall(nil); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("uninstall without -force should fail: %v", err)
	}
}

func TestUninstallDryRunDeletesNothing(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooksDir := initManagedHooks(t, dir)

	if err := Uninstall([]string{"-dry-run"}); err != nil {
		t.Fatalf("Uninstall -dry-run: %v", err)
	}
	for _, p := range []string{
		filepath.Join(hooksDir, "post-commit"),
		filepath.Join(hooksDir, "pre-commit"),
		filepath.Join(dir, ".githints"),
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, "CLAUDE.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dry-run removed %s: %v", p, err)
		}
	}
}

func TestUninstallRemovesEverything(t *testing.T) {
	dir := clitest.ChdirTempRepo(t)
	hooksDir := initManagedHooks(t, dir)

	// Add a project MCP config so unregister has something to do.
	if err := mcpcfg.MCPConfig([]string{"claude", "-write"}); err != nil {
		t.Fatalf("mcp-config claude -write: %v", err)
	}
	if err := mcpcfg.MCPConfig([]string{"codex", "-write"}); err != nil {
		t.Fatalf("mcp-config codex -write: %v", err)
	}

	saltPath := integrity.SaltPath(dir)

	if err := Uninstall([]string{"-force"}); err != nil {
		t.Fatalf("Uninstall -force: %v", err)
	}

	for _, name := range []string{"post-commit", "pre-commit"} {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints")); !os.IsNotExist(err) {
		t.Error(".githints/ still exists")
	}
	if saltPath != "" {
		if _, err := os.Stat(saltPath); !os.IsNotExist(err) {
			t.Errorf("salt still exists at %s", saltPath)
		}
	}

	// .gitignore, AGENTS.md, CLAUDE.md should have no managed block.
	for _, name := range []string{".gitignore", "AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if strings.Contains(string(data), "githints (managed)") {
			t.Errorf("%s still has the managed block", name)
		}
	}

	// MCP configs should no longer mention githints.
	for _, p := range []string{filepath.Join(dir, ".mcp.json"), filepath.Join(dir, ".codex", "config.toml")} {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if strings.Contains(string(data), "githints") {
			t.Errorf("%s still mentions githints:\n%s", p, data)
		}
	}

	// Anchors ref should be gone.
	if err := gitutil.DeleteRef("refs/notes/githints"); err != nil {
		t.Fatalf("DeleteRef after uninstall: %v", err)
	}
}
