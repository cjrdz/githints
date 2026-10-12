// Package serve implements the githints serve command that runs the MCP
// stdio server.
package serve

import (
	"flag"
	"fmt"
	"os"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/mcpserver"
)

// Run starts the MCP stdio server. Assumes `githints init` already ran.
// The repo root normally comes from the working directory, but not every MCP
// client launches a server from the project directory — Codex CLI's config is
// global, and some clients start servers from an arbitrary cwd. -root (or
// GITHINTS_ROOT) lets the client pin it. Precedence: flag, GITHINTS_ROOT,
// CLAUDE_PROJECT_DIR, cwd.
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	rootFlag := fs.String("root", "", "path to the git repo to serve (default: current directory, or $GITHINTS_ROOT)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pinned := *rootFlag
	if pinned == "" {
		pinned = os.Getenv("GITHINTS_ROOT")
	}
	if pinned == "" {
		// Claude Code sets this for the servers it starts; its docs do not
		// promise the working directory.
		pinned = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if pinned != "" {
		if err := os.Chdir(pinned); err != nil {
			return fmt.Errorf("cannot use %s as repo root: %w", pinned, err)
		}
	}

	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	return mcpserver.Run(root, st, cfg, version)
}
