// Package record implements the record, status, and render CLI commands.
package record

import (
	"flag"
	"fmt"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/hint"
	"github.com/cjrdz/githints/internal/integrity"
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

	c, err := recorder.RecordChange(st, root, key, recorder.Input{
		FilePath: *file,
		Summary:  *summary,
		Reason:   *reason,
		AgentID:  *agentID,
		Source:   "agent",
	})
	if err != nil {
		return err
	}
	// The id is what `githints outcome -id` takes later.
	fmt.Printf("recorded change #%d for %s\n", c.ID, textsafe.OneLine(*file))
	return nil
}

// Outcome records how an earlier change turned out. Mirrors record_outcome.
func Outcome(args []string) error {
	fs := flag.NewFlagSet("outcome", flag.ExitOnError)
	id := fs.Int64("id", 0, "id of the change, as shown by history/recent (required)")
	state := fs.String("state", "", "worked, partial, failed, reverted or unknown (required)")
	note := fs.String("note", "", "what happened; required unless the state is worked or unknown")
	agentID := fs.String("agent-id", "", "agent/session fingerprint (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id < 1 || *state == "" {
		return fmt.Errorf("-id and -state are required")
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

	o, err := recorder.RecordOutcome(st, root, key, recorder.OutcomeInput{
		ChangeID: *id,
		State:    *state,
		Note:     *note,
		AgentID:  *agentID,
	})
	if err != nil {
		return err
	}
	fmt.Printf("recorded outcome %s for change #%d (%s)\n", o.OutcomeState, o.TargetID, textsafe.OneLine(o.FilePath))
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
