package teardown

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/managed"
)

// Remove is the slim teardown: it stops git from tracking new commits by
// removing the hooks, but keeps the store, salt, anchors, agent files and MCP
// registrations so re-enabling is one `githints init` away.
func Remove(args []string) error {
	fset := flag.NewFlagSet("remove", flag.ExitOnError)
	dryRun := fset.Bool("dry-run", false, "show what would be done, do nothing")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fset.Arg(0))
	}
	return removeHooksOnly(*dryRun)
}

// removeHooksOnly is `uninstall -keep-data` (and the old `remove`).
func removeHooksOnly(dryRun bool) error {
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	hookRes, err := removeHooks(root, dryRun)
	if err != nil {
		return err
	}
	for _, r := range hookRes {
		printRemoveResult(root, r, dryRun)
	}

	fmt.Println()
	fmt.Println("Kept for re-enable: .githints/ store, integrity salt, agent files, MCP configs, anchors.")
	fmt.Println("Run `githints init` to start tracking again, or `githints uninstall` to remove everything.")
	return nil
}

// Uninstall is the full teardown: hooks, project MCP entries, managed
// blocks, anchors, .githints/ and the salt. Nothing outside the repository
// is touched except the salt, which lives outside by design and is removed
// because it becomes useless once the store is gone.
func Uninstall(args []string) error {
	fset := flag.NewFlagSet("uninstall", flag.ExitOnError)
	dryRun := fset.Bool("dry-run", false, "show what would be done, do nothing")
	force := fset.Bool("force", false, "actually delete the store and salt; required because uninstall is destructive")
	keepData := fset.Bool("keep-data", false, "only stop tracking: remove the git hooks and keep everything else")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fset.Arg(0))
	}
	if *keepData {
		if *force {
			return fmt.Errorf("-keep-data deletes nothing; -force does not apply")
		}
		return removeHooksOnly(*dryRun)
	}
	if !*dryRun && !*force {
		return fmt.Errorf("uninstall deletes the change log and salt; re-run with -force to confirm, or -dry-run to preview")
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	var results []Result
	add := func(name string, changed bool, err error) {
		r := Result{name: name, err: err}
		if !changed && err == nil {
			r.skipped = true
			r.detail = "not installed"
		}
		results = append(results, r)
	}

	hookRes, err := removeHooks(root, *dryRun)
	if err != nil {
		return err
	}
	results = append(results, hookRes...)

	mcpRes, err := mcpcfg.UnregisterProject(root, *dryRun)
	if err != nil {
		return err
	}
	for _, r := range mcpRes {
		results = append(results, Result{name: r.Name, skipped: r.Skipped, detail: r.Detail, err: r.Err})
	}

	changed, err := managed.RemoveBlock(filepath.Join(root, ".gitignore"), managed.GitignoreStart, managed.GitignoreEnd, *dryRun)
	add(".gitignore", changed, err)
	changed, err = managed.RemoveBlock(filepath.Join(root, "AGENTS.md"), managed.MarkdownStart, managed.MarkdownEnd, *dryRun)
	add("AGENTS.md", changed, err)
	changed, err = managed.RemoveBlock(filepath.Join(root, "CLAUDE.md"), managed.MarkdownStart, managed.MarkdownEnd, *dryRun)
	add("CLAUDE.md", changed, err)
	changed, err = removeAnchors(*dryRun)
	add("anchors", changed, err)
	changed, err = removeSalt(root, *dryRun)
	add("salt", changed, err)
	changed, err = removeGithintsDir(root, *dryRun)
	add(".githints/", changed, err)

	failed := 0
	for _, r := range results {
		printRemoveResult(root, r, *dryRun)
		if r.err != nil {
			failed++
		}
	}
	if *dryRun {
		fmt.Println("\nRe-run with -force to apply.")
	} else if failed > 0 {
		return fmt.Errorf("%d step(s) failed", failed)
	} else {
		fmt.Println("\ngithints uninstalled. Global MCP configs were not touched; remove their githints-* entries by hand if you set them up.")
	}
	return nil
}
