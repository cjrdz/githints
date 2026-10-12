// Package record implements the record, status, and render CLI commands.
package record

import (
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/managed"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Record lets you (or a script) write a change row from the shell, without
// going through the MCP tool. Mirrors record_change exactly.
func Record(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	file := fs.String("file", "", "repo-relative path to the changed file (required)")
	summary := fs.String("summary", "", "one to two sentence summary (required)")
	reason := fs.String("reason", "", "why the change was made (optional)")
	agentID := fs.String("agent-id", "", "agent/session fingerprint (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *summary == "" {
		return fmt.Errorf("-file and -summary are required")
	}
	if err := recorder.ValidateFilePath(*file); err != nil {
		return err
	}

	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		return fmt.Errorf("integrity key: %w", err)
	}

	return recorder.Record(st, root, key, recorder.Input{
		FilePath: *file,
		Summary:  *summary,
		Reason:   *reason,
		AgentID:  *agentID,
		Source:   "agent",
	})
}

// Status prints a quick health/dashboard view of the githints store.
func Status() error {
	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("store: %s\n", filepath.Join(root, ".githints", "store.db"))

	hooksOK := true
	statuses, err := managed.CheckHooks(root)
	if err != nil {
		fmt.Printf("hooks: %v\n", err)
		hooksOK = false
	} else {
		for _, r := range statuses {
			if r.Level != managed.HookOK {
				hooksOK = false
				fmt.Printf("hooks: %s (fix: %s)\n", r.Detail, r.Fix)
			}
		}
	}
	if hooksOK {
		fmt.Println("hooks: installed")
	}
	if _, path, err := integrity.ExistingKey(root); err != nil {
		fmt.Printf("salt: %v\n", err)
	} else {
		fmt.Printf("salt: %s\n", path)
	}

	total, err := st.Count()
	if err != nil {
		return err
	}
	fmt.Printf("total changes: %d\n", total)

	pending, err := st.UncommittedCount()
	if err != nil {
		return err
	}
	fmt.Printf("uncommitted: %d\n", pending)

	if lastRecorded, _ := st.MetaGet("last_recorded_at"); lastRecorded != "" {
		if ts, err := strconv.ParseInt(lastRecorded, 10, 64); err == nil {
			fmt.Printf("last recorded: %s\n", time.Unix(ts, 0).Format(time.RFC3339))
		}
	}

	if lastVerify, _ := st.MetaGet("last_verify_at"); lastVerify != "" {
		if ts, err := strconv.ParseInt(lastVerify, 10, 64); err == nil {
			fmt.Printf("last verified: %s\n", time.Unix(ts, 0).Format(time.RFC3339))
		}
	}

	recent, err := st.RecentChanges(1)
	if err != nil {
		return err
	}
	if len(recent) > 0 && recent[0].Branch != "" {
		fmt.Printf("latest branch: %s\n", textsafe.OneLine(recent[0].Branch))
	}

	fmt.Println("(run `githints doctor` for a full health check)")
	return nil
}

// Render re-generates every per-file hint and the root CHANGES.md from the
// current store. Useful after resolving a merge conflict on the rendered
// markdown or after switching shared-history modes.
func Render() error {
	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()

	files, err := st.FilesWithHistory()
	if err != nil {
		return fmt.Errorf("list files with history: %w", err)
	}
	for _, f := range files {
		if err := hint.RenderFile(st, root, f, recorder.HistoryLimitPerFile); err != nil {
			return fmt.Errorf("render file hint for %s: %w", f, err)
		}
	}
	if err := hint.RenderChangelog(st, root, recorder.ChangelogLimit); err != nil {
		return fmt.Errorf("render changelog: %w", err)
	}
	fmt.Printf("rendered %d file hint(s) + CHANGES.md\n", len(files))
	return nil
}
