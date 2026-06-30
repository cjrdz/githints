package store

import (
	"os"
	"path/filepath"
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
				Source:     "hook",
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
		Source:     "hook",
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
	mustInsert(t, st, Change{FilePath: "x.go", CommitHash: "", Source: "hook", Summary: "should not be claimed"})

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
		Source:     "hook",
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
