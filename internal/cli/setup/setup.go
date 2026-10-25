// Package setup implements the `githints setup` command.
package setup

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/gitutil"
)

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Setup is the one command to run in a new repository: it initializes
// githints if needed, registers the MCP server with the clients in use, and
// reports what still needs attention.
func Setup(args []string) error {
	fset := flag.NewFlagSet("setup", flag.ExitOnError)
	clients := fset.String("clients", "", "comma-separated clients to register (default: those detected); 'all' for every project-level client. Global ones ("+mcpcfg.GlobalClientNames()+") are written only when named")
	dryRun := fset.Bool("dry-run", false, "show what would be written, write nothing")
	share := fset.Bool("share", false, "passed to init: share rendered markdown with the team")
	chain := fset.Bool("chain", false, "passed to init: keep existing git hooks and run them first")
	update := fset.Bool("update", false, "replace githints' existing entries (e.g. after putting githints on PATH); other servers are never touched")
	list := fset.Bool("list", false, "list the supported clients and whether each is detected, then exit")
	printOnly := fset.Bool("print", false, "print the entry for the one client named in -clients, write nothing")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (clients go in -clients=a,b)", fset.Arg(0))
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if *printOnly {
		if *clients == "" || strings.Contains(*clients, ",") || *clients == "all" {
			return fmt.Errorf("-print needs exactly one client: githints setup -print -clients=NAME")
		}
		return mcpcfg.MCPConfig([]string{*clients})
	}

	if *list {
		for _, c := range mcpcfg.Clients() {
			state := ""
			switch {
			case c.Scope() == mcpcfg.ScopeManual:
				state = "manual steps"
			case c.Scope() == mcpcfg.ScopeGlobal:
				state = "global; name it to write"
			case c.Detect(root):
				state = "detected"
			}
			fmt.Printf("  %-16s %-56s %s\n", c.Name(), c.Title(), state)
		}
		return nil
	}

	selected, err := mcpcfg.SelectClients(root, *clients)
	if err != nil {
		return err
	}

	if !exists(filepath.Join(root, ".githints", "store.db")) {
		if *dryRun {
			fmt.Println("would run: githints init")
		} else {
			var initArgs []string
			if *share {
				initArgs = append(initArgs, "-share")
			}
			if *chain {
				initArgs = append(initArgs, "-chain")
			}
			if err := initialize.InitQuiet(initArgs); err != nil {
				return err
			}
			fmt.Println("initialized githints (hooks, store, agent instructions)")
		}
	}

	if len(selected) == 0 {
		fmt.Println("\nNo MCP clients detected. Name them, e.g.:\n  githints setup -clients=claude,vscode\n(`githints setup -list` shows them all.)")
		return nil
	}

	fmt.Println("\nMCP clients:")
	var failed, manual []string
	notes := map[string]bool{}
	for _, c := range selected {
		if c.Scope() == mcpcfg.ScopeManual {
			manual = append(manual, c.Name())
			continue
		}
		results, err := mcpcfg.Register(c, root, *dryRun, *update)
		for _, r := range results {
			mcpcfg.PrintResult(root, c, r, *dryRun)
		}
		if err != nil {
			fmt.Printf("  %-30s FAILED: %v\n", c.Title(), err)
			failed = append(failed, c.Name())
			continue
		}
		if c.Note() != "" {
			notes[c.Title()+": "+c.Note()] = true
		}
	}
	for _, n := range manual {
		c, _ := mcpcfg.LookupClient(n)
		fmt.Printf("\n%s needs a manual step:\n%s\n", c.Title(), mcpcfg.ManualText(c, root))
	}
	if _, portable := mcpcfg.GithintsCommand(false); !portable {
		notes["githints is not on PATH, so these configs name this binary by its absolute path and work on this machine only. Put githints on PATH, then `githints setup -update` before committing them."] = true
	}
	if len(notes) > 0 {
		keys := make([]string, 0, len(notes))
		for k := range notes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Println("\nNotes:")
		for _, k := range keys {
			fmt.Println("  - " + k)
		}
	}
	if !*dryRun {
		fmt.Println("\nRestart (or reload) your editor or agent so it picks up the new server, then run `githints doctor`.")
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not register %s", strings.Join(failed, ", "))
	}
	return nil
}
