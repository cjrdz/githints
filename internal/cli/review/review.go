// Package review implements `githints review`: how deep a review the staged
// change needs and why, and whether an approved receipt covers it (the shell
// twin of review_status), and `githints review record` (record_review).
package review

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/risk"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Run dispatches `githints review`.
func Run(args []string) error {
	if len(args) > 0 && args[0] == "record" {
		return Record(args[1:])
	}
	return Status(args)
}

// Record signs a review receipt for exactly what is staged now.
func Record(args []string) error {
	fs := flag.NewFlagSet("review record", flag.ExitOnError)
	verdict := fs.String("verdict", "", "approved, escalated or inconclusive (required)")
	findings := fs.String("findings", "", "what the review found; required unless approved")
	lenses := fs.String("lenses", "", "comma-separated lenses covered: "+strings.Join(risk.Lenses, ","))
	agentID := fs.String("agent-id", "", "who reviewed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var lensList []string
	if *lenses != "" {
		lensList = strings.Split(*lenses, ",")
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
	in, closeIdx, err := inputs(root, st)
	if err != nil {
		return err
	}
	defer closeIdx()
	c, rep, err := risk.RecordReview(st, key, in, risk.ReviewInput{
		Verdict: *verdict, Findings: *findings, Lenses: lensList, AgentID: *agentID,
	})
	if err != nil {
		return err
	}
	fmt.Printf("recorded review receipt #%d: %s\n", c.ID, textsafe.OneLine(c.Summary))
	receipts, err := risk.CurrentReceipts(st, root, rep.Level)
	if err != nil {
		return err
	}
	fmt.Print(receipts.Format())
	return nil
}

// inputs gathers what risk.Assess reads. The returned func closes the index.
func inputs(root string, st *store.Store) (risk.Inputs, func(), error) {
	cfg, err := config.Load(root)
	if err != nil {
		return risk.Inputs{}, nil, err
	}
	in := risk.Inputs{Store: st, Root: root, Config: cfg.Review}
	if cfg.Index.Enabled {
		if idx := core.OpenExistingIndex(root); idx != nil {
			in.Index = idx
			return in, func() { _ = idx.Close() }, nil
		}
	}
	return in, func() {}, nil
}

// Status assesses the staged change, or the paths given.
func Status(args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths := fs.Args()
	for _, p := range paths {
		if err := recorder.ValidateFilePath(p); err != nil {
			return err
		}
	}

	root, st, err := core.OpenInitialized()
	if err != nil {
		return err
	}
	defer st.Close()
	staged := len(paths) == 0
	if staged {
		if paths, err = risk.Staged(); err != nil {
			return fmt.Errorf("list staged files: %w", err)
		}
		if len(paths) == 0 {
			fmt.Println("nothing staged; stage the change to review, or name paths")
			return nil
		}
	}
	in, closeIdx, err := inputs(root, st)
	if err != nil {
		return err
	}
	defer closeIdx()
	rep, err := risk.Assess(in, paths)
	if err != nil {
		return err
	}
	// Receipts are for the staged tree; named paths are only assessed.
	var receipts *risk.Receipts
	if staged {
		r, err := risk.CurrentReceipts(st, root, rep.Level)
		if err != nil {
			return err
		}
		receipts = &r
	}
	if *asJSON {
		out := struct {
			Risk    risk.Report  `json:"risk"`
			Receipt *jsonReceipt `json:"receipt,omitempty"`
		}{Risk: rep}
		if receipts != nil {
			out.Receipt = &jsonReceipt{Tree: receipts.Tree, Base: receipts.Base, Valid: receipts.Valid, Why: receipts.Why}
			if receipts.Current != nil {
				out.Receipt.ID, out.Receipt.Verdict = receipts.Current.ID, receipts.Current.Verdict
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Print(rep.Format())
	if receipts != nil {
		fmt.Print(receipts.Format())
	}
	return nil
}

// jsonReceipt is the receipt state in `githints review -json`, for scripts
// and CI.
type jsonReceipt struct {
	Tree    string `json:"tree"`
	Base    string `json:"base"`
	ID      int64  `json:"id,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Valid   bool   `json:"valid"`
	Why     string `json:"why,omitempty"`
}
