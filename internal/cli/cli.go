// Package cli is the root CLI dispatcher. It wires together the command
// subpackages and implements the tiny core commands (help, version) that do
// not deserve their own package.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/doctor"
	"github.com/cjrdz/githints/internal/cli/exportcmd"
	"github.com/cjrdz/githints/internal/cli/hooks"
	"github.com/cjrdz/githints/internal/cli/indexcmd"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/cli/notecmd"
	"github.com/cjrdz/githints/internal/cli/read"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/cli/salt"
	"github.com/cjrdz/githints/internal/cli/serve"
	"github.com/cjrdz/githints/internal/cli/setup"
	"github.com/cjrdz/githints/internal/cli/teardown"
	"github.com/cjrdz/githints/internal/cli/verify"
)

// Main is the CLI entry point. It returns an exit code.
//
// Exit codes: 0 ok, 1 error, 2 usage error, 3 verify found problems.
func Main(args []string, version string) int {
	cliVersion = version
	if len(args) < 2 {
		usage()
		return 2
	}

	switch args[1] {
	case "-h", "-help", "--help":
		_ = cmdHelp() // cannot fail
		return 0
	case "-v", "-version", "--version":
		fmt.Println(versionString())
		return 0
	}

	cmd, ok := commands[args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "githints: unknown command %q; run `githints help` for the list\n", args[1])
		return 2
	}
	if err := cmd(args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "githints: "+err.Error())
		if errors.Is(err, core.ErrVerifyFailed) {
			return 3
		}
		return 1
	}
	return 0
}

// commands maps every subcommand name to its handler. It is the single source
// of dispatch: a command that exists as a cmdX function but is missing here is
// unreachable from the CLI, which is how `githints render` stayed broken while
// being advertised in usage(). TestUsageMatchesCommandTable keeps this table
// and usageText in sync so the mismatch cannot recur silently.
var commands = map[string]func(args []string) error{
	"init":           initialize.Init,
	"serve":          func(args []string) error { return serve.Run(args, versionString()) },
	"hook-run":       noArgs(hooks.HookRun),
	"hook-precommit": noArgs(hooks.PreCommit),
	"record":         record.Record,
	"outcome":        record.Outcome,
	"note":           notecmd.Run,
	"export":         func(args []string) error { return exportcmd.Run(args, versionString()) },
	"remove":         teardown.Remove,
	"uninstall":      teardown.Uninstall,
	"verify":         noArgs(verify.Run),
	"changes":        read.Changes,
	"history":        read.History,
	"recent":         read.Recent,
	"brief":          read.Brief,
	"search":         read.Search,
	"diff":           read.Diff,
	"rotate-salt":    salt.RotateSalt,
	"salt":           salt.Salt,
	"status":         noArgs(record.Status),
	"render":         noArgs(record.Render),
	"index":          indexcmd.Run,
	"doctor":         noArgs(doctor.Run),
	"mcp-config":     mcpcfg.MCPConfig,
	"setup":          setup.Setup,
	"version":        noArgs(cmdVersion),
	"help":           noArgs(cmdHelp),
}

// noArgs adapts a flagless command to the table's handler signature.
func noArgs(fn func() error) func([]string) error {
	return func([]string) error { return fn() }
}

// hiddenCommands are dispatchable but left out of usage: git hooks call them,
// people do not. TestUsageMatchesCommandTable checks this list too.
var hiddenCommands = map[string]bool{
	"hook-run":       true,
	"hook-precommit": true,
}

func usage() {
	fmt.Fprintln(os.Stderr, usageText)
}

const usageText = `githints — lightweight change tracking for AI coding agents

Usage:
  githints init [-chain|-force] [-share]
                                  set up .githints/ + install the git hooks
  githints remove [-dry-run]      stop tracking: remove the git hooks, keep everything else
  githints uninstall [-dry-run] [-force]
                                  remove hooks, MCP entries, agent blocks, anchors, .githints/ and salt
  githints serve [-root=PATH]     run the MCP stdio server (root also via $GITHINTS_ROOT)
  githints record -file=... -summary=... [-reason=...] [-agent-id=...]
                                  manually record a change (useful for testing)
  githints outcome -id=N -state=S [-note=...] [-agent-id=...]
                                  how change #N turned out: worked, partial, failed, reverted, unknown
  githints note list|search|get|record|delete|render
                                  markdown notes under .githints/notes/ (githints note help)
  githints export -o DIR [-duckdb] [-no-notes]
                                  write the log, hints and notes as files for people and notebooks
  githints verify                 check HMAC chain + markdown consistency
  githints history -file=F [-limit=N]
                                  why a file looks the way it does (get_file_history)
  githints recent [-limit=N]      latest changes across the repo (get_recent_changes)
  githints brief [-budget=N]      session-start digest: failed attempts, uncommitted work, blast radius
  githints search -query=Q [-limit=N]
                                  full-text search of summaries and reasons (search_changes)
  githints diff -file=F [-hash=H] redacted diff of the working tree or one commit (get_diff)
  githints changes -since=T -until=T [-file=F] [-limit=N]
                                  timeline query (get_changes_in_range)
  githints rotate-salt [-force]   generate a new integrity salt and re-sign the chain
  githints salt path|export [-o FILE]|import [-force] FILE
                                  locate, back up, or restore the integrity salt
  githints status                 show store health and pending records
  githints render                 re-render all markdown from the store
  githints index                  re-index the structural symbol cache
  githints index status           show index statistics
  githints index verify           report drift between the index and the repo
  githints index languages        list the languages this binary can index
  githints index facets [-facet=] [-framework=] [-file=] [-limit=]
                                  list detected framework constructs
  githints index graph [-format=html|json|dot|mermaid] [-o=FILE] [-focus=F -depth=N]
                                  export the file dependency graph (default: an
                                  offline viewer at .githints/graph.html)
  githints doctor                 check hooks, salt, store, config, index and MCP setup
  githints setup [-clients=a,b|all] [-dry-run] [-list] [-update]
                                  init if needed, then register the MCP server with
                                  every client detected (Claude Code, opencode, Codex,
                                  VS Code, Cursor, Zed, Kiro, Gemini, Junie, ...)
  githints mcp-config CLIENT [-write]
                                  print or add the MCP entry for one client
  githints version                print the version, commit and build date
  githints help                   show this list

Run any command with -h for its flags. Exit status: 0 ok, 1 error,
2 usage error, 3 verify found problems.`

func cmdHelp() error {
	fmt.Println(usageText)
	return nil
}

func cmdVersion() error {
	fmt.Println(versionString())
	return nil
}
