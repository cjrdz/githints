// Package upgrade implements `githints upgrade`: bring a tracked repository in
// line with the githints binary running it. It works offline and running it
// twice changes nothing the second time.
package upgrade

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/doctor"
	"github.com/cjrdz/githints/internal/cli/indexcmd"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/managed"
)

// metaUpgradedBy records the githints version that last ran upgrade here, so
// a later run can tell whether MCP servers are serving an older binary.
const metaUpgradedBy = "upgraded_by"

// Run refreshes the hooks, the managed blocks and the index, then runs doctor.
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "show what would change without changing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("upgrade takes no arguments, got %q", rest[0])
	}

	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	exe, err := managed.RunningBinary()
	if err != nil {
		return fmt.Errorf("resolve own binary path: %w", err)
	}

	verb := func(done, would string) string {
		if *dryRun {
			return would
		}
		return done
	}
	var changed, current, skipped []string

	hooksDir, err := gitutil.HooksDir(root)
	if err != nil {
		return fmt.Errorf("locate git hooks directory: %w", err)
	}
	for _, h := range managed.Hooks {
		path := filepath.Join(hooksDir, h.Name)
		exists, mine, err := managed.HookExistsAndManaged(path)
		switch {
		case err != nil:
			return err
		case !exists:
			skipped = append(skipped, h.Name+" hook is not installed; `githints init` installs it")
			continue
		case !mine:
			skipped = append(skipped, h.Name+" hook was not installed by githints; left alone")
			continue
		}
		ok, err := managed.HookIsCurrent(path, exe, h.Command)
		if err != nil {
			return err
		}
		if ok {
			current = append(current, h.Name+" hook")
			continue
		}
		if !*dryRun {
			if err := managed.WriteHook(path, managed.HookScriptFor(exe, h.Command)); err != nil {
				return fmt.Errorf("write %s hook: %w", h.Name, err)
			}
		}
		changed = append(changed, fmt.Sprintf("%s the %s hook at %s", verb("repointed", "would repoint"), h.Name, exe))
	}

	for _, b := range managed.Blocks(root) {
		name := filepath.Base(b.Path)
		state, err := b.State()
		if err != nil {
			return err
		}
		switch {
		case state == managed.BlockCurrent:
			current = append(current, name+" block")
			continue
		case state == managed.BlockMissing && name != ".gitignore":
			// The repository writes its own instructions; init adds the block
			// if someone wants it back.
			skipped = append(skipped, name+" has no githints block; left alone")
			continue
		}
		if !*dryRun {
			if err := b.Ensure(); err != nil {
				return fmt.Errorf("update %s: %w", name, err)
			}
		}
		changed = append(changed, verb("refreshed", "would refresh")+" the githints block in "+name)
	}

	if cfg, err := config.Load(root); err != nil {
		skipped = append(skipped, "index: "+err.Error())
	} else if cfg.Index.Enabled {
		if why := doctor.IndexRebuildReason(root, cfg.Index.Languages); why != "" {
			if !*dryRun {
				if err := indexcmd.Run(nil); err != nil {
					return err
				}
			}
			changed = append(changed, verb("rebuilt", "would rebuild")+" the index: "+why)
		} else {
			current = append(current, "index")
		}
	}

	previous, _ := st.MetaGet(metaUpgradedBy) // missing: no earlier upgrade, so restart to be safe
	newBinary := previous != version
	if newBinary && !*dryRun {
		if err := st.MetaSet(metaUpgradedBy, version); err != nil {
			return err
		}
	}

	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Println(title)
		for _, s := range items {
			fmt.Println("  " + s)
		}
	}
	if len(changed) == 0 {
		fmt.Println("everything githints manages here is current")
	}
	list(verb("changed:", "would change:"), changed)
	list("already current:", current)
	list("skipped:", skipped)

	fmt.Println()
	fmt.Println("doctor:")
	doctorErr := doctor.Run()

	if newBinary && !*dryRun {
		fmt.Println()
		fmt.Printf("githints %s now manages this repository: restart your MCP servers, which keep running the binary they started with.\n", version)
	}
	return doctorErr
}
