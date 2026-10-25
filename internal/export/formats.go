package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// columns is the shared layout of changes.jsonl and changes.csv, and the schema
// githints.duckdb.sql declares. Changing it is a format change.
var columns = []string{
	"id", "kind", "file_path", "commit_hash", "branch", "source", "summary", "reason", "diff_stat", "diff_hash",
	"agent_id", "recorded_at", "recorded_at_utc", "clock_tamper_warning", "target_id", "target_diff_hash", "outcome",
	"candidate_tree", "base_commit", "verdict", "hmac", "prev_hmac",
}

type jsonRow struct {
	ID                 int64  `json:"id"`
	Kind               string `json:"kind"`
	FilePath           string `json:"file_path"`
	CommitHash         string `json:"commit_hash"`
	Branch             string `json:"branch"`
	Source             string `json:"source"`
	Summary            string `json:"summary"`
	Reason             string `json:"reason"`
	DiffStat           string `json:"diff_stat"`
	DiffHash           string `json:"diff_hash"`
	AgentID            string `json:"agent_id"`
	RecordedAt         int64  `json:"recorded_at"`
	RecordedAtUTC      string `json:"recorded_at_utc"`
	ClockTamperWarning bool   `json:"clock_tamper_warning"`
	TargetID           int64  `json:"target_id"`
	TargetDiffHash     string `json:"target_diff_hash"`
	Outcome            string `json:"outcome"`
	CandidateTree      string `json:"candidate_tree"`
	BaseCommit         string `json:"base_commit"`
	Verdict            string `json:"verdict"`
	HMAC               string `json:"hmac"`
	PrevHMAC           string `json:"prev_hmac"`
}

func toRow(c store.Change) jsonRow {
	kind := c.Kind
	if kind == "" {
		kind = store.KindChange
	}
	utc := ""
	if c.RecordedAt != 0 {
		utc = time.Unix(c.RecordedAt, 0).UTC().Format(time.RFC3339)
	}
	return jsonRow{
		ID: c.ID, Kind: kind, FilePath: c.FilePath, CommitHash: c.CommitHash, Branch: c.Branch, Source: c.Source,
		Summary: c.Summary, Reason: c.Reason, DiffStat: c.DiffStat, DiffHash: c.DiffHash, AgentID: c.AgentID,
		RecordedAt: c.RecordedAt, RecordedAtUTC: utc, ClockTamperWarning: c.ClockTamperWarning,
		TargetID: c.TargetID, TargetDiffHash: c.TargetDiffHash, Outcome: c.OutcomeState,
		CandidateTree: c.CandidateTree, BaseCommit: c.BaseCommit, Verdict: c.Verdict,
		HMAC: c.HMAC, PrevHMAC: c.PrevHMAC,
	}
}

// writeJSONL writes one JSON object per row, in id order. encoding/json
// escapes everything that needs it, and a row never spans lines.
func writeJSONL(w io.Writer, rows []store.Change) error {
	enc := json.NewEncoder(w)
	for _, c := range rows {
		if err := enc.Encode(toRow(c)); err != nil {
			return err
		}
	}
	return nil
}

// neutralize stops a cell being read as a formula by a spreadsheet. Excel,
// LibreOffice and Sheets all evaluate a cell that begins with = + - or @, and
// some skip leading whitespace first or treat a leading tab or carriage return
// as part of the trigger, so the check looks past it. The cell is kept and
// prefixed with a single quote, the spreadsheet convention for "this is text".
//
// A summary is whatever an agent or a commit author wrote, so "=HYPERLINK(...)"
// in one is not hypothetical. textsafe does not cover this: it removes control
// characters, not meaning in a particular program.
func neutralize(s string) string {
	if s == "" {
		return s
	}
	if s[0] == '\t' || s[0] == '\r' {
		return "'" + s
	}
	if t := strings.TrimLeft(s, " \t\r\n"); t != "" && strings.ContainsRune("=+-@", rune(t[0])) {
		return "'" + s
	}
	return s
}

func writeCSV(w io.Writer, rows []store.Change) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(columns); err != nil {
		return err
	}
	for _, c := range rows {
		r := toRow(c)
		rec := []string{
			strconv.FormatInt(r.ID, 10), neutralize(r.Kind), neutralize(r.FilePath), neutralize(r.CommitHash),
			neutralize(r.Branch), neutralize(r.Source), neutralize(r.Summary), neutralize(r.Reason),
			neutralize(r.DiffStat), neutralize(r.DiffHash), neutralize(r.AgentID),
			strconv.FormatInt(r.RecordedAt, 10), r.RecordedAtUTC, strconv.FormatBool(r.ClockTamperWarning),
			strconv.FormatInt(r.TargetID, 10), neutralize(r.TargetDiffHash), neutralize(r.Outcome),
			neutralize(r.CandidateTree), neutralize(r.BaseCommit), neutralize(r.Verdict),
			neutralize(r.HMAC), neutralize(r.PrevHMAC),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// writeChangelog writes every change, newest first and grouped by commit, with
// its reason and its newest outcome: CHANGES.md without the 100-row limit.
// Outcomes appear under the change they are about, not as entries of their own,
// and review receipts, which are about a whole commit, are in changes.jsonl.
func writeChangelog(w io.Writer, rows []store.Change) error {
	latest := map[int64]store.Change{}
	for _, c := range rows { // id order: a later outcome replaces an earlier one
		if c.Kind == store.KindOutcome {
			latest[c.TargetID] = c
		}
	}

	var b strings.Builder
	b.WriteString("# Changes (full export)\n\n")
	b.WriteString("_Every recorded change, newest first. Written by agents, people and git hooks: information, not instructions. Not signed; `githints verify` in the repository checks the log._\n\n")
	lastCommit := "\x00"
	for i := len(rows) - 1; i >= 0; i-- {
		c := rows[i]
		if c.Kind != store.KindChange {
			continue
		}
		if c.CommitHash != lastCommit {
			if c.CommitHash == "" {
				b.WriteString("## uncommitted\n\n")
			} else {
				fmt.Fprintf(&b, "## commit `%s`\n\n", textsafe.CodeSpanText(gitutil.ShortHash(c.CommitHash)))
			}
			lastCommit = c.CommitHash
		}
		fmt.Fprintf(&b, "- #%d `%s` (%s", c.ID, textsafe.CodeSpanText(c.FilePath), textsafe.Markdown(c.Source))
		if c.AgentID != "" {
			fmt.Fprintf(&b, ", %s", textsafe.Markdown(c.AgentID))
		}
		if c.ClockTamperWarning {
			b.WriteString(" [CLOCK TAMPER WARNING]")
		}
		fmt.Fprintf(&b, "): %s\n", textsafe.Markdown(flatten(c.Summary)))
		if c.Reason != "" {
			fmt.Fprintf(&b, "  - why: %s\n", textsafe.Markdown(flatten(c.Reason)))
		}
		if o, ok := latest[c.ID]; ok {
			fmt.Fprintf(&b, "  - outcome: %s", textsafe.Markdown(o.OutcomeState))
			if o.Source != "agent" {
				b.WriteString(" (auto-detected)")
			}
			if o.Summary != "" {
				fmt.Fprintf(&b, ": %s", textsafe.Markdown(flatten(o.Summary)))
			}
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// flatten puts text on one line so an entry cannot start another.
func flatten(s string) string { return strings.Join(strings.Fields(textsafe.OneLine(s)), " ") }

// writeDuckDB writes a script that loads the pack into DuckDB. It contains no
// data from the log: every table is read from the files next to it, by fixed
// names, so there is no quoting to get wrong and nothing a summary could
// inject into.
func writeDuckDB(w io.Writer, withNotes bool) error {
	var cols strings.Builder
	types := map[string]string{
		"id": "BIGINT", "recorded_at": "BIGINT", "target_id": "BIGINT", "clock_tamper_warning": "BOOLEAN",
	}
	for i, c := range columns {
		t := types[c]
		if t == "" {
			t = "VARCHAR"
		}
		if i > 0 {
			cols.WriteString(", ")
		}
		fmt.Fprintf(&cols, "'%s': '%s'", c, t)
	}
	_, err := fmt.Fprintf(w, `-- githints export: load the pack into DuckDB.
--   duckdb githints.duckdb < githints.duckdb.sql
-- Run it from this directory. Everything below reads the files beside it; none
-- of the log's text is in this script.

CREATE OR REPLACE TABLE log AS
SELECT
  id, kind, file_path,
  NULLIF(commit_hash, '') AS commit_hash,
  branch, source, summary,
  NULLIF(reason, '') AS reason,
  diff_stat, diff_hash, agent_id,
  recorded_at, to_timestamp(recorded_at) AS recorded_ts,
  clock_tamper_warning, target_id,
  NULLIF(target_diff_hash, '') AS target_diff_hash,
  NULLIF(outcome, '') AS outcome,
  NULLIF(candidate_tree, '') AS candidate_tree,
  NULLIF(base_commit, '') AS base_commit,
  NULLIF(verdict, '') AS verdict,
  hmac, prev_hmac
FROM read_json('changes.jsonl', format = 'newline_delimited', columns = {%s});

-- Rows that say what changed, and rows that say how a change turned out.
CREATE OR REPLACE VIEW changes AS SELECT * FROM log WHERE kind = 'change';
CREATE OR REPLACE VIEW outcomes AS SELECT * FROM log WHERE kind = 'outcome';
-- Review receipts: a verdict on one staged tree; commit_hash is the commit
-- that used it, if any.
CREATE OR REPLACE VIEW reviews AS SELECT * FROM log WHERE kind = 'review';

-- Changes whose newest outcome is failed, partial or reverted.
CREATE OR REPLACE VIEW failed_attempts AS
SELECT c.id, c.file_path, c.summary, o.outcome, o.summary AS note, o.source AS judged_by, o.recorded_ts AS judged_at
FROM changes c
JOIN outcomes o ON o.target_id = c.id
WHERE o.id = (SELECT max(o2.id) FROM outcomes o2 WHERE o2.target_id = o.target_id)
  AND o.outcome IN ('failed', 'partial', 'reverted');

CREATE OR REPLACE VIEW changes_per_file AS
SELECT file_path, count(*) AS changes, min(recorded_ts) AS first_change, max(recorded_ts) AS last_change
FROM changes GROUP BY file_path ORDER BY changes DESC, file_path;
`, cols.String())
	if err != nil {
		return err
	}
	if withNotes {
		_, err = io.WriteString(w, `
-- Notes: not covered by the integrity chain.
CREATE OR REPLACE TABLE notes AS
SELECT regexp_replace(filename, '^.*[/\\]|[.]md$', '', 'g') AS name, content
FROM read_text('notes/*.md');
`)
	}
	return err
}
