package record

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

func TestRenderCommand(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)

	if err := initialize.Init([]string{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Record([]string{"-file=main.go", "-summary=add main"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	changesPath := filepath.Join(dir, ".githints", "CHANGES.md")
	if _, err := os.Stat(changesPath); err != nil {
		t.Fatalf("CHANGES.md not rendered after record: %v", err)
	}
	if err := os.Remove(changesPath); err != nil {
		t.Fatalf("remove CHANGES.md: %v", err)
	}

	// Calling Render directly now exercises the exported command. The command
	// table coverage lives in the parent cli package tests.
	if err := Render(); err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := os.Stat(changesPath); err != nil {
		t.Fatalf("CHANGES.md not rendered after render command: %v", err)
	}
}

// TestUsageMatchesCommandTable enforces that every command advertised in
// usageText is dispatchable and every dispatchable command is advertised.
// `githints render` shipped broken because usage() promised a command the
// dispatch switch never handled.
