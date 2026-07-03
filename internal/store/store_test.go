package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st, func() { st.Close() }
}

func mustInsert(t *testing.T, st *Store, c Change) int64 {
	t.Helper()
	id, err := st.Insert(c)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id
}

func TestOpenCreatesSchema(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	var name string
	err := st.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='changes'").Scan(&name)
	if err != nil {
		t.Fatalf("schema table not found: %v", err)
	}
	if name != "changes" {
		t.Fatalf("unexpected table name %q", name)
	}

	var meta string
	if err := st.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='githints_meta'").Scan(&meta); err != nil {
		t.Fatalf("meta table not found: %v", err)
	}
}

func TestMetaAndCounts(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	if v, err := st.MetaGet("foo"); err != nil || v != "" {
		t.Fatalf("MetaGet missing key = %q, err %v", v, err)
	}
	if err := st.MetaSet("foo", "bar"); err != nil {
		t.Fatalf("MetaSet: %v", err)
	}
	if v, err := st.MetaGet("foo"); err != nil || v != "bar" {
		t.Fatalf("MetaGet = %q, err %v", v, err)
	}

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "x", CommitHash: "abc"})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "y"})

	total, err := st.Count()
	if err != nil || total != 2 {
		t.Fatalf("Count = %d, err %v", total, err)
	}
	pending, err := st.UncommittedCount()
	if err != nil || pending != 1 {
		t.Fatalf("UncommittedCount = %d, err %v", pending, err)
	}
}

func TestLastRecordedAtPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.Insert(Change{FilePath: "a.go", Source: "agent", Summary: "x", RecordedAt: 12345}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer st2.Close()

	v, err := st2.MetaGet("last_recorded_at")
	if err != nil {
		t.Fatalf("MetaGet: %v", err)
	}
	if v != "12345" {
		t.Fatalf("last_recorded_at = %q, want 12345", v)
	}
}

func TestInsertAndFileHistory(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	tests := []struct {
		name string
		in   Change
	}{
		{
			name: "agent record with reason",
			in: Change{
				FilePath:   "cmd/api/main.go",
				CommitHash: "abc123",
				Source:     "agent",
				Summary:    "add health check endpoint",
				Reason:     "ops asked for it",
			},
		},
		{
			name: "hook record with diff stat",
			in: Change{
				FilePath:   "cmd/api/main.go",
				CommitHash: "def456",
				Source:     "fallback",
				Summary:    "change detected by hook",
				DiffStat:   "+3 -1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := mustInsert(t, st, tt.in)
			rows, err := st.FileHistory(tt.in.FilePath, 10)
			if err != nil {
				t.Fatalf("FileHistory: %v", err)
			}
			if len(rows) == 0 {
				t.Fatalf("expected at least one row")
			}
			got := rows[0]
			if got.ID != id {
				t.Errorf("id = %d, want %d", got.ID, id)
			}
			if got.FilePath != tt.in.FilePath {
				t.Errorf("file_path = %q, want %q", got.FilePath, tt.in.FilePath)
			}
			if got.Source != tt.in.Source {
				t.Errorf("source = %q, want %q", got.Source, tt.in.Source)
			}
			if got.Summary != tt.in.Summary {
				t.Errorf("summary = %q, want %q", got.Summary, tt.in.Summary)
			}
			if got.Reason != tt.in.Reason {
				t.Errorf("reason = %q, want %q", got.Reason, tt.in.Reason)
			}
			if got.DiffStat != tt.in.DiffStat {
				t.Errorf("diff_stat = %q, want %q", got.DiffStat, tt.in.DiffStat)
			}
		})
	}
}

func TestRecentChangesOrdering(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "first"})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "second"})
	mustInsert(t, st, Change{FilePath: "c.go", Source: "agent", Summary: "third"})

	rows, err := st.RecentChanges(10)
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	want := []string{"third", "second", "first"}
	for i, r := range rows {
		if r.Summary != want[i] {
			t.Errorf("row[%d].summary = %q, want %q", i, r.Summary, want[i])
		}
	}
}

func TestRecentChangesLimit(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "one"})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "two"})

	rows, err := st.RecentChanges(1)
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Summary != "two" {
		t.Errorf("summary = %q, want %q", rows[0].Summary, "two")
	}
}

func TestSearch(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{
		FilePath: "auth.go",
		Source:   "agent",
		Summary:  "fix password hashing",
		Reason:   "use bcrypt instead of md5",
	})
	mustInsert(t, st, Change{
		FilePath: "db.go",
		Source:   "agent",
		Summary:  "add connection pool",
		Reason:   "reduce latency",
	})

	tests := []struct {
		query    string
		wantFile string
	}{
		{"bcrypt", "auth.go"},
		{"latency", "db.go"},
		{"password", "auth.go"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rows, err := st.Search(tt.query, 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", tt.query, err)
			}
			if len(rows) == 0 {
				t.Fatalf("Search(%q) returned no rows", tt.query)
			}
			if rows[0].FilePath != tt.wantFile {
				t.Errorf("Search(%q) first file = %q, want %q", tt.query, rows[0].FilePath, tt.wantFile)
			}
		})
	}
}

func TestHasAgentRecordForCommit(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{
		FilePath:   "x.go",
		CommitHash: "aaa",
		Source:     "agent",
		Summary:    "agent record",
	})
	mustInsert(t, st, Change{
		FilePath:   "x.go",
		CommitHash: "aaa",
		Source:     "fallback",
		Summary:    "hook fallback",
	})

	tests := []struct {
		file string
		hash string
		want bool
	}{
		{"x.go", "aaa", true},
		{"x.go", "bbb", false},
		{"y.go", "aaa", false},
	}

	for _, tt := range tests {
		t.Run(tt.file+"-"+tt.hash, func(t *testing.T) {
			got, err := st.HasAgentRecordForCommit(tt.file, tt.hash)
			if err != nil {
				t.Fatalf("HasAgentRecordForCommit: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaimPending(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "x.go", Source: "agent", Summary: "uncommitted edit 1"})
	mustInsert(t, st, Change{FilePath: "x.go", Source: "agent", Summary: "uncommitted edit 2"})
	mustInsert(t, st, Change{FilePath: "x.go", CommitHash: "old", Source: "agent", Summary: "already committed"})
	mustInsert(t, st, Change{FilePath: "x.go", Source: "hook", Summary: "should not be claimed"})

	n, err := st.ClaimPending("x.go", "abc123")
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if n != 2 {
		t.Fatalf("claimed %d rows, want 2", n)
	}

	got, err := st.HasAgentRecordForCommit("x.go", "abc123")
	if err != nil {
		t.Fatalf("HasAgentRecordForCommit: %v", err)
	}
	if !got {
		t.Fatal("expected agent record for abc123 after claiming")
	}

	rows, err := st.FileHistory("x.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	claimed := 0
	for _, r := range rows {
		if r.CommitHash == "abc123" {
			claimed++
		}
	}
	if claimed != 2 {
		t.Fatalf("found %d claimed rows, want 2", claimed)
	}
}

func TestAgentRecordsForCommit(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{
		FilePath:   "x.go",
		CommitHash: "aaa",
		Source:     "agent",
		Summary:    "agent record",
		DiffStat:   "+5 -2",
	})
	mustInsert(t, st, Change{
		FilePath:   "x.go",
		CommitHash: "aaa",
		Source:     "agent",
		Summary:    "second agent record",
		DiffStat:   "+1 -1",
	})
	mustInsert(t, st, Change{
		FilePath:   "x.go",
		CommitHash: "aaa",
		Source:     "fallback",
		Summary:    "hook fallback",
	})

	rows, err := st.AgentRecordsForCommit("x.go", "aaa")
	if err != nil {
		t.Fatalf("AgentRecordsForCommit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	var totalAdd int
	for _, r := range rows {
		totalAdd += len(r.DiffStat) // cheap presence check
	}
	if totalAdd == 0 {
		t.Fatal("expected diff stats on agent rows")
	}
}

func TestOpenCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "githints", "store.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file not created: %v", err)
	}
}

func TestOpenSetsWALPragma(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	var mode string
	if err := st.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenUpgradesLegacySchema(t *testing.T) {
	// Simulate a store created before the branch column existed: open a
	// bare sqlite db, create the old schema, close it, then let store.Open
	// migrate it. The migration must add branch without losing rows.
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	bare, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	oldSchema := `CREATE TABLE changes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path   TEXT NOT NULL,
		commit_hash TEXT,
		source      TEXT NOT NULL CHECK(source IN ('agent','hook')),
		summary     TEXT NOT NULL,
		reason      TEXT,
		diff_stat   TEXT,
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);`
	if _, err := bare.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := bare.Exec(`INSERT INTO changes (file_path, source, summary) VALUES ('legacy.go', 'agent', 'pre-migration')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := bare.Close(); err != nil {
		t.Fatalf("close bare: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy store: %v", err)
	}
	defer st.Close()

	rows, err := st.FileHistory("legacy.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (legacy row must survive migration)", len(rows))
	}
	if rows[0].Branch != "" {
		t.Errorf("migrated row branch = %q, want empty default", rows[0].Branch)
	}
	if rows[0].Summary != "pre-migration" {
		t.Errorf("migrated row summary = %q, want pre-migration", rows[0].Summary)
	}
	if rows[0].RecordedAt != 0 {
		t.Errorf("migrated row recorded_at = %d, want 0", rows[0].RecordedAt)
	}
}

func TestLegacyHookSourceBecomesFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	bare, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	oldSchema := `CREATE TABLE changes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		file_path   TEXT NOT NULL,
		commit_hash TEXT,
		source      TEXT NOT NULL CHECK(source IN ('agent','hook')),
		summary     TEXT NOT NULL,
		reason      TEXT,
		diff_stat   TEXT,
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);`
	if _, err := bare.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := bare.Exec(`INSERT INTO changes (file_path, source, summary) VALUES ('hook.go', 'hook', 'old hook row')`); err != nil {
		t.Fatalf("seed hook row: %v", err)
	}
	if err := bare.Close(); err != nil {
		t.Fatalf("close bare: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy store: %v", err)
	}
	defer st.Close()

	rows, err := st.FileHistory("hook.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Source != "fallback" {
		t.Errorf("migrated source = %q, want fallback", rows[0].Source)
	}
}

func TestNewSourceValuesAllowed(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "agent"})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "llm", Summary: "llm"})
	mustInsert(t, st, Change{FilePath: "c.go", Source: "fallback", Summary: "fallback"})

	for _, f := range []string{"a.go", "b.go", "c.go"} {
		rows, err := st.FileHistory(f, 10)
		if err != nil {
			t.Fatalf("FileHistory %s: %v", f, err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 row for %s", f)
		}
	}
}

func TestInsertPersistsRecordedAtAgentIDHMAC(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{
		FilePath:   "auth/token.go",
		Branch:     "feature/rotate-keys",
		Source:     "agent",
		Summary:    "rotate signing key",
		AgentID:    "session-abc",
		RecordedAt: 1234567890,
		HMAC:       "deadbeef",
		PrevHMAC:   "cafebabe",
	})

	rows, err := st.FileHistory("auth/token.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Branch != "feature/rotate-keys" {
		t.Errorf("branch = %q, want feature/rotate-keys", got.Branch)
	}
	if got.AgentID != "session-abc" {
		t.Errorf("agent_id = %q, want session-abc", got.AgentID)
	}
	if got.RecordedAt != 1234567890 {
		t.Errorf("recorded_at = %d, want 1234567890", got.RecordedAt)
	}
	if got.HMAC != "deadbeef" {
		t.Errorf("hmac = %q, want deadbeef", got.HMAC)
	}
	if got.PrevHMAC != "cafebabe" {
		t.Errorf("prev_hmac = %q, want cafebabe", got.PrevHMAC)
	}
}

func TestOrderingByRecordedAt(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "older", RecordedAt: 100})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "newer", RecordedAt: 300})
	mustInsert(t, st, Change{FilePath: "c.go", Source: "agent", Summary: "middle", RecordedAt: 200})

	rows, err := st.RecentChanges(10)
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	want := []string{"newer", "middle", "older"}
	for i, r := range rows {
		if r.Summary != want[i] {
			t.Errorf("row[%d].summary = %q, want %q", i, r.Summary, want[i])
		}
	}
}

func TestChangesInRange(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "early", RecordedAt: 100})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "mid", RecordedAt: 200})
	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "late", RecordedAt: 300})

	rows, err := st.ChangesInRange(150, 350, "", 10)
	if err != nil {
		t.Fatalf("ChangesInRange: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Summary != "late" || rows[1].Summary != "mid" {
		t.Errorf("unexpected order: %+v", rows)
	}

	rows, err = st.ChangesInRange(150, 350, "a.go", 10)
	if err != nil {
		t.Fatalf("ChangesInRange file filter: %v", err)
	}
	if len(rows) != 1 || rows[0].Summary != "late" {
		t.Fatalf("expected only 'late' for a.go, got %+v", rows)
	}
}

func TestAllChangesAndFilesWithHistory(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "one", RecordedAt: 10})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "two", RecordedAt: 20})

	all, err := st.AllChanges()
	if err != nil {
		t.Fatalf("AllChanges: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d rows, want 2", len(all))
	}
	if all[0].ID >= all[1].ID {
		t.Errorf("AllChanges not ordered by id: %d >= %d", all[0].ID, all[1].ID)
	}

	files, err := st.FilesWithHistory()
	if err != nil {
		t.Fatalf("FilesWithHistory: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
}

func TestLastHMAC(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	hmac, err := st.LastHMAC()
	if err != nil {
		t.Fatalf("LastHMAC empty: %v", err)
	}
	if hmac != "" {
		t.Fatalf("expected empty hmac for empty store, got %q", hmac)
	}

	mustInsert(t, st, Change{FilePath: "x.go", Source: "agent", Summary: "first", HMAC: "aaa"})
	mustInsert(t, st, Change{FilePath: "y.go", Source: "agent", Summary: "second", HMAC: "bbb"})

	hmac, err = st.LastHMAC()
	if err != nil {
		t.Fatalf("LastHMAC: %v", err)
	}
	if hmac != "bbb" {
		t.Errorf("last hmac = %q, want bbb", hmac)
	}
}

func TestInsertFlagsClockTamper(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 100})
	mustInsert(t, st, Change{FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 50})

	rows, err := st.AllChanges()
	if err != nil {
		t.Fatalf("AllChanges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if !rows[1].ClockTamperWarning {
		t.Errorf("expected clock_tamper_warning=1 for backward jump, got %v", rows[1].ClockTamperWarning)
	}
	if rows[0].ClockTamperWarning {
		t.Errorf("expected clock_tamper_warning=0 for first row, got %v", rows[0].ClockTamperWarning)
	}
}

func TestHasPendingAgentRecord(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "x.go", Source: "agent", Summary: "pending edit"})
	mustInsert(t, st, Change{FilePath: "x.go", CommitHash: "already", Source: "agent", Summary: "claimed"})
	mustInsert(t, st, Change{FilePath: "x.go", Source: "hook", Summary: "hook pending should not count"})

	tests := []struct {
		file string
		want bool
	}{
		{"x.go", true},
		{"y.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := st.HasPendingAgentRecord(tt.file)
			if err != nil {
				t.Fatalf("HasPendingAgentRecord: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithTxAtomicClaimAndInsert(t *testing.T) {
	st, cleanup := openTestStore(t)
	defer cleanup()

	mustInsert(t, st, Change{FilePath: "x.go", Source: "agent", Summary: "pending"})

	err := st.WithTx(func(tx *sql.Tx) error {
		claimed, err := ClaimPendingTx(tx, "x.go", "abc123")
		if err != nil {
			return err
		}
		if claimed != 1 {
			return fmt.Errorf("claimed %d rows, want 1", claimed)
		}
		rows, err := AgentRecordsForCommitTx(tx, "x.go", "abc123")
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("expected 1 agent row after claim, got %d", len(rows))
		}
		_, err = InsertTx(tx, Change{
			FilePath:   "x.go",
			CommitHash: "abc123",
			Source:     "fallback",
			Summary:    "fallback",
		})
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	rows, err := st.FileHistory("x.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}
