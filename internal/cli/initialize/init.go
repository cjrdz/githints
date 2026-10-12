// Package initialize implements the `githints init` command and the quiet
// variant used by setup.
package initialize

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/managed"
	"github.com/cjrdz/githints/internal/recorder"
)

// Init creates .githints/, the store, installs the git hooks, and writes
// the .gitignore rule for githints' output.
func Init(args []string) error { return runInit(args, true) }

// InitQuiet is init for setup, which prints its own summary.
func InitQuiet(args []string) error { return runInit(args, false) }

func runInit(args []string, verbose bool) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing git hooks even if not managed by githints")
	chain := fs.Bool("chain", false, "keep existing git hooks: move each aside and run it before githints'")
	share := fs.Bool("share", false, "share rendered markdown with the team (only state files are gitignored)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, st, err := core.OpenRootAndStore()
	if err != nil {
		// repo root resolves fine even before .githints/ exists; the only
		// failure case here is "not a git repo", which OpenRootAndStore
		// already reports clearly.
		return err
	}
	defer st.Close()

	if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
		return err
	}

	if _, err := integrity.LoadOrCreateSalt(root); err != nil {
		return fmt.Errorf("integrity salt: %w", err)
	}

	if err := managed.EnsureAgentFiles(root); err != nil {
		return fmt.Errorf("write agent instruction files: %w", err)
	}

	if err := managed.EnsureGitignore(root, *share); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own binary path: %w", err)
	}

	// Install both hooks from one template so the post-commit reactive
	// recorder and the pre-commit proactive gate stay in sync.
	// On Windows, Git for Windows runs hook scripts through its bundled sh,
	// so forward-slash paths work everywhere.
	//
	// The recorded path is tried first, so a developer running a local build
	// keeps using it even with another githints on PATH, and nothing changes for
	// an existing install. PATH is the fallback for when that path stops existing
	// -- which is exactly what a package manager does on upgrade, since
	// os.Executable resolves symlinks and records the versioned target (Homebrew's
	// Cellar, Scoop's apps directory) rather than the stable shim.
	//
	// If neither resolves, the hook says so and exits 0: a missing binary must
	// not block a commit, and the pre-commit gate is a warning by design.
	hookScript := func(cmd string) string { return managed.HookScriptFor(exe, cmd) }

	hooksDir, err := gitutil.HooksDir(root)
	if err != nil {
		return fmt.Errorf("locate git hooks directory: %w", err)
	}
	// core.hooksPath pointing inside the work tree is a hook manager (husky,
	// lefthook, a committed .githooks/). Those files are committed and shared,
	// so writing a hook carrying this machine's binary path there would ship
	// it to everyone. Say what to add instead.
	if rel, err := filepath.Rel(root, hooksDir); err == nil && filepath.IsLocal(rel) && !strings.HasPrefix(filepath.ToSlash(rel), ".git/") && !*force {
		return fmt.Errorf("git runs hooks from %s, inside the repository (core.hooksPath; husky or lefthook?).\n"+
			"githints will not write machine-specific hooks into a committed directory. Add these lines to the hooks there:\n"+
			"  post-commit:  githints hook-run\n"+
			"  pre-commit:   githints hook-precommit\n"+
			"or re-run with -force to write them anyway", rel)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	postCommit := filepath.Join(hooksDir, "post-commit")
	preCommit := filepath.Join(hooksDir, "pre-commit")

	for _, path := range []string{postCommit, preCommit} {
		exists, managedHook, err := managed.HookExistsAndManaged(path)
		if err != nil {
			return err
		}
		if !exists || managedHook {
			continue
		}
		switch {
		case *chain:
			aside := path + managed.ChainedSuffix
			if _, err := os.Lstat(aside); err == nil {
				return fmt.Errorf("%s already exists; refusing to overwrite it with %s", aside, path)
			}
			if err := os.Rename(path, aside); err != nil {
				return fmt.Errorf("move existing hook aside: %w", err)
			}
			fmt.Printf("kept your existing hook as %s; it runs before githints'\n", aside)
		case *force:
			// Overwritten below.
		default:
			return fmt.Errorf("%s already exists and was not installed by githints;\n"+
				"use -chain to keep it (it will run first), or -force to replace it", path)
		}
	}

	for path, cmd := range map[string]string{postCommit: "hook-run", preCommit: "hook-precommit"} {
		if err := managed.WriteHook(path, hookScript(cmd)); err != nil {
			return fmt.Errorf("write %s hook: %w", filepath.Base(path), err)
		}
	}

	mode := "private"
	if *share {
		mode = "shared"
	}
	if !verbose {
		return nil
	}
	fmt.Printf("githints initialized at %s/.githints\nhooks installed at %s, %s\nmode: %s\n", root, postCommit, preCommit, mode)
	fmt.Print(`wrote agent instructions to AGENTS.md and CLAUDE.md (managed blocks)

Next: register the MCP server with your editors and agents:

  githints setup               # every client detected here
  githints setup -list         # what is supported and detected

Then run ` + "`githints doctor`" + ` to check the whole setup.
`)
	return nil
}
