// Package store wraps the SQLite-backed change log that lives at
// .githints/store.db. It is the single source of truth; everything in
// the per-file markdown and CHANGES.md is rendered from these rows.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Change struct {
	ID         int64
	FilePath   string
	CommitHash string
	Source     string // "agent" or "hook"
	Summary    string
	Reason     string
	DiffStat   string
	CreatedAt  string
}

const schema = `
CREATE TABLE IF NOT EXISTS changes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	file_path   TEXT NOT NULL,
	commit_hash TEXT,
	source      TEXT NOT NULL CHECK(source IN ('agent','hook')),
	summary     TEXT NOT NULL,
	reason      TEXT,
	diff_stat   TEXT,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_changes_file   ON changes(file_path);
CREATE INDEX IF NOT EXISTS idx_changes_commit ON changes(commit_hash);

CREATE VIRTUAL TABLE IF NOT EXISTS changes_fts USING fts5(
	summary, reason, content='changes', content_rowid='id'
);
CREATE TRIGGER IF NOT EXISTS changes_ai AFTER INSERT ON changes BEGIN
	INSERT INTO changes_fts(rowid, summary, reason) VALUES (new.id, new.summary, new.reason);
END;
`

// Open creates/opens the SQLite store at path and applies the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Insert records one change row and returns its id.
func (s *Store) Insert(c Change) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO changes (file_path, commit_hash, source, summary, reason, diff_stat)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.FilePath, c.CommitHash, c.Source, c.Summary, c.Reason, c.DiffStat,
	)
	if err != nil {
		return 0, fmt.Errorf("insert change: %w", err)
	}
	return res.LastInsertId()
}

// FileHistory returns the most recent changes for one file, newest first.
func (s *Store) FileHistory(filePath string, limit int) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, commit_hash, source, summary, reason, diff_stat, created_at
		 FROM changes WHERE file_path = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		filePath, limit,
	)
}

// RecentChanges returns the most recent changes across the whole repo.
func (s *Store) RecentChanges(limit int) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, commit_hash, source, summary, reason, diff_stat, created_at
		 FROM changes ORDER BY created_at DESC, id DESC LIMIT ?`,
		limit,
	)
}

// Search runs an FTS5 query over summary + reason text.
func (s *Store) Search(query string, limit int) ([]Change, error) {
	return s.query(
		`SELECT c.id, c.file_path, c.commit_hash, c.source, c.summary, c.reason, c.diff_stat, c.created_at
		 FROM changes_fts f
		 JOIN changes c ON c.id = f.rowid
		 WHERE changes_fts MATCH ?
		 ORDER BY c.created_at DESC LIMIT ?`,
		query, limit,
	)
}

// HasAgentRecordForCommit reports whether an agent already recorded this
// file for this commit, so the post-commit hook can skip it and avoid a
// duplicate, lower-quality "hook" entry.
func (s *Store) HasAgentRecordForCommit(filePath, commitHash string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM changes WHERE file_path = ? AND commit_hash = ? AND source = 'agent'`,
		filePath, commitHash,
	).Scan(&n)
	return n > 0, err
}

// AgentRecordsForCommit returns the agent-recorded rows for one file in one
// commit. The hook uses these to compare aggregated diff stats and decide
// whether a manual tweak happened after the agent's record_change call.
func (s *Store) AgentRecordsForCommit(filePath, commitHash string) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, commit_hash, source, summary, reason, diff_stat, created_at
		 FROM changes WHERE file_path = ? AND commit_hash = ? AND source = 'agent'`,
		filePath, commitHash,
	)
}

// ClaimPending assigns commitHash to any agent-recorded rows for filePath
// that are still "pending" (recorded before any commit existed for them,
// commit_hash = ''). Call this for every changed file as the first step
// of the post-commit hook, before checking HasAgentRecordForCommit — it's
// what lets a record_change call made pre-commit still match up with the
// commit that ends up containing it.
func (s *Store) ClaimPending(filePath, commitHash string) (int64, error) {
	res, err := s.db.Exec(
		`UPDATE changes SET commit_hash = ?
		 WHERE file_path = ? AND source = 'agent' AND (commit_hash = '' OR commit_hash IS NULL)`,
		commitHash, filePath,
	)
	if err != nil {
		return 0, fmt.Errorf("claim pending rows for %s: %w", filePath, err)
	}
	return res.RowsAffected()
}

func (s *Store) query(q string, args ...any) ([]Change, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.FilePath, &c.CommitHash, &c.Source, &c.Summary, &c.Reason, &c.DiffStat, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
