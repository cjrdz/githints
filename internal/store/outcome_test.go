package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func insertOutcome(t *testing.T, st *Store, target Change, state, note string) int64 {
	t.Helper()
	id, err := st.Insert(Change{
		FilePath: target.FilePath, Source: "agent", Summary: note, RecordedAt: 2000,
		Kind: KindOutcome, TargetID: target.ID, TargetDiffHash: target.DiffHash, OutcomeState: state,
	})
	if err != nil {
		t.Fatalf("insert outcome: %v", err)
	}
	return id
}

// A database written before outcomes existed has none of the new columns.
// Opening it must add them and read the old rows as ordinary changes.
func TestOpenUpgradesStoreWithoutOutcomeColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX idx_changes_target`,
		`ALTER TABLE changes DROP COLUMN kind`,
		`ALTER TABLE changes DROP COLUMN target_id`,
		`ALTER TABLE changes DROP COLUMN target_diff_hash`,
		`ALTER TABLE changes DROP COLUMN outcome`,
		`INSERT INTO changes (file_path, source, summary, recorded_at, hmac) VALUES ('old.go', 'agent', 'before outcomes', 1000, 'abc')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	raw.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	rows, err := st.FileHistory("old.go", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("FileHistory = %v, %v", rows, err)
	}
	if rows[0].Kind != KindChange || rows[0].TargetID != 0 || rows[0].OutcomeState != "" {
		t.Errorf("old row read as %+v, want an ordinary change", rows[0])
	}
}

// Outcome rows live in the same table so they share the chain, which means
// every reader that means "changes" has to leave them out.
func TestOutcomeRowsAreNotChanges(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	id := mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "added retry loop", DiffHash: "dh", RecordedAt: 1000})
	target, ok, err := st.GetChange(id)
	if err != nil || !ok {
		t.Fatalf("GetChange: %v %v", ok, err)
	}
	insertOutcome(t, st, target, OutcomeFailed, "retry loop deadlocks")

	hist, err := st.FileHistory("a.go", 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("FileHistory = %d rows, %v; want only the change", len(hist), err)
	}
	if hist[0].LatestOutcome == nil || hist[0].LatestOutcome.OutcomeState != OutcomeFailed {
		t.Errorf("outcome not attached: %+v", hist[0].LatestOutcome)
	}

	if rows, _ := st.RecentChanges(10); len(rows) != 1 {
		t.Errorf("RecentChanges = %d rows, want 1", len(rows))
	}
	if rows, _ := st.ChangesInRange(0, 9999, "", 10); len(rows) != 1 {
		t.Errorf("ChangesInRange = %d rows, want 1", len(rows))
	}
	// The note is indexed like any summary, but a search returns changes.
	if rows, err := st.Search("deadlocks", 10); err != nil || len(rows) != 0 {
		t.Errorf("Search matched an outcome row: %v %v", rows, err)
	}
	if n, _ := st.Count(); n != 1 {
		t.Errorf("Count = %d, want 1", n)
	}
	if n, _ := st.UncommittedCount(); n != 1 {
		t.Errorf("UncommittedCount = %d, want 1", n)
	}
	// The integrity paths still see everything.
	if all, _ := st.AllChanges(); len(all) != 2 {
		t.Errorf("AllChanges = %d rows, want 2", len(all))
	}
}

// An outcome must never be claimed by a commit or counted as the agent having
// described the file, or the hook would skip its fallback row.
func TestOutcomeRowsAreNotClaimedByCommits(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	id := mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "change", RecordedAt: 1000})
	target, _, _ := st.GetChange(id)
	insertOutcome(t, st, target, OutcomeWorked, "")

	if err := st.WithTx(func(tx *sql.Tx) error {
		n, err := ClaimPendingTx(tx, "a.go", "deadbeef")
		if err != nil || n != 1 {
			t.Errorf("claimed %d rows (%v), want only the change", n, err)
		}
		rows, err := AgentRecordsForCommitTx(tx, "a.go", "deadbeef")
		if err != nil || len(rows) != 1 || rows[0].Kind != KindChange {
			t.Errorf("agent rows for commit = %+v (%v)", rows, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.HasPendingAgentRecord("a.go"); has {
		t.Error("an outcome row counts as a pending agent record")
	}
}

func TestLatestOutcomeWins(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	id := mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "change", RecordedAt: 1000})
	target, _, _ := st.GetChange(id)
	insertOutcome(t, st, target, OutcomeWorked, "")
	insertOutcome(t, st, target, OutcomeReverted, "broke prod")

	hist, _ := st.FileHistory("a.go", 10)
	if got := hist[0].LatestOutcome; got == nil || got.OutcomeState != OutcomeReverted {
		t.Errorf("latest outcome = %+v, want reverted", got)
	}
}

// Open runs the migrations every time, and the hook opens the store while the
// MCP server is writing to it. The migrations must leave the table alone once
// it is current: they used to drop and re-add the source column on every open,
// and a writer in that window failed with "no column named source".
func TestReopeningLeavesTheTableAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	columns := func() string {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT group_concat(name, ',') FROM pragma_table_info('changes')`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var cols string
		for rows.Next() {
			_ = rows.Scan(&cols)
		}
		return cols
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "s", RecordedAt: 1})
	st.Close()
	first := columns()
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	if got := columns(); got != first {
		t.Errorf("columns changed across reopen:\n first %s\n later %s", first, got)
	}
}

func TestOpenMigratesLegacyHookSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO changes (file_path, source, summary, recorded_at) VALUES ('h.go', 'hook', 'legacy', 1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, _ := st.FileHistory("h.go", 10)
	if len(rows) != 1 || rows[0].Source != "fallback" {
		t.Errorf("legacy hook row = %+v, want source fallback", rows)
	}
}
