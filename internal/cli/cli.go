// Package cli is the root CLI dispatcher. It wires together the command
// subpackages and implements the tiny core commands (help, version) that do
// not deserve their own package.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/cjrdz/githints/internal/cli/anchors"
	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/doctor"
	"github.com/cjrdz/githints/internal/cli/exportcmd"
	"github.com/cjrdz/githints/internal/cli/hooks"
	"github.com/cjrdz/githints/internal/cli/indexcmd"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/notecmd"
	"github.com/cjrdz/githints/internal/cli/read"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/cli/review"
	"github.com/cjrdz/githints/internal/cli/salt"
	"github.com/cjrdz/githints/internal/cli/serve"
	"github.com/cjrdz/githints/internal/cli/setup"
	"github.com/cjrdz/githints/internal/cli/teardown"
	"github.com/cjrdz/githints/internal/cli/updatecmd"
	"github.com/cjrdz/githints/internal/cli/upgrade"
	"github.com/cjrdz/githints/internal/cli/verify"
	"github.com/cjrdz/githints/internal/update"
)

// Main is the CLI entry point. It returns an exit code.
//
// Exit codes: 0 ok, 1 error, 2 usage error, 3 verify found problems.
func Main(args []string, version string) int {
	cliVersion = version
	update.CleanupOld()
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
		if now, removed := removedCommands[args[1]]; removed {
			fmt.Fprintf(os.Stderr, "githints: %q was removed in 0.4.0; use `githints %s`\n", args[1], now)
		} else {
			fmt.Fprintf(os.Stderr, "githints: unknown command %q; run `githints help` for the list\n", args[1])
		}
		return 2
	}
	if err := cmd(args[2:]); err != nil {
		if errors.Is(err, core.ErrUpdateAvailable) {
			return core.ExitUpdateAvailable // update -check printed its own line
		}
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
	"uninstall":      teardown.Uninstall,
	"verify":         noArgs(verify.Run),
	"anchors":        anchors.Run,
	"log":            read.Log,
	"brief":          read.Brief,
	"diff":           read.Diff,
	"review":         review.Run,
	"salt":           salt.Salt,
	"render":         noArgs(record.Render),
	"index":          indexcmd.Run,
	"doctor":         noArgs(doctor.Run),
	"setup":          setup.Setup,
	"update":         func(args []string) error { return updatecmd.Run(args, versionString()) },
	"upgrade":        func(args []string) error { return upgrade.Run(args, versionString()) },
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

// removedCommands are names that dispatched until 0.4.0, mapped to what
// replaced them, so scripts and agent memories that still use one get pointed
// at the new command instead of a bare "unknown command".
var removedCommands = map[string]string{
	"history":     "log -file=F",
	"recent":      "log",
	"search":      "log -query=Q",
	"changes":     "log -since=T -until=T",
	"status":      "doctor",
	"rotate-salt": "salt rotate",
	"remove":      "uninstall -keep-data",
	"mcp-config":  "setup -print -clients=NAME",
}

func usage() {
	fmt.Fprintln(os.Stderr, usageText)
}

const usageText = `githints — lightweight change tracking for AI coding agents

Usage:
  githints init [-chain|-force] [-share]
                                  set up .githints/ + install the git hooks
  githints uninstall [-dry-run] [-force | -keep-data]
                                  remove hooks, MCP entries, agent blocks, anchors, .githints/
                                  and salt; -keep-data removes only the hooks
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
  githints anchors push|fetch [REMOTE]
                                  share the Merkle anchors (refs/notes/githints) with a remote
  githints log [-file=F] [-query=Q] [-since=T] [-until=T] [-limit=N]
                                  the change log, newest first; filters combine (query_changes)
  githints brief [-budget=N]      session-start digest: failed attempts, uncommitted work, blast radius
  githints diff -file=F [-hash=H] redacted diff of the working tree or one commit (get_diff)
  githints review [-json] [PATH...]
                                  how deep a review the staged change (or PATHs) needs, and why
  githints salt path|export [-o FILE]|import [-force] FILE|rotate [-force]
                                  locate, back up, restore or rotate the integrity salt
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
  githints doctor                 check hooks, salt, store, log, config, index and MCP setup
  githints setup [-clients=a,b|all] [-dry-run] [-list] [-update] [-print]
                                  init if needed, then register the MCP server with
                                  every client detected (Claude Code, opencode, Codex,
                                  VS Code, Cursor, Zed, Kiro, Gemini, Junie, ...);
                                  -print -clients=NAME only prints that client's entry
  githints update [-check] [-version=TAG] [-force] [-yes]
                                  replace this binary with the latest verified release
                                  (the only command that uses the network)
  githints upgrade [-dry-run]     refresh this repository's hooks, managed blocks and
                                  index for the running githints, then run doctor
  githints version                print the version, commit and build date
  githints help                   show this list

Run any command with -h for its flags. Exit status: 0 ok, 1 error,
2 usage error, 3 verify found problems, 10 (update -check) an update is available.`

func cmdHelp() error {
	fmt.Println(usageText)
	return nil
}

func cmdVersion() error {
	fmt.Println(versionString())
	return nil
}
