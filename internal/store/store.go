// Package store wraps the SQLite-backed change log that lives at
// .githints/store.db. It is the single source of truth; everything in
// the per-file markdown and CHANGES.md is rendered from these rows.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// ClockSkewTolerance is how many seconds a new recorded_at is allowed to
// be earlier than the previous one before we flag it as possible clock
// tampering. Small negative jumps happen on concurrent inserts; large
// jumps are suspicious.
const ClockSkewTolerance = 5

type Store struct {
	db *sql.DB

	// mu protects lastRecordedAt. Concurrent Record() calls can race on
	// the ordering timestamp; the mutex lets us detect backward jumps
	// relative to the most recent value we have seen in this process.
	mu             sync.Mutex
	lastRecordedAt int64
}

// Change is one row in the change log. recorded_at is the authoritative
// ordering timestamp because it is set by the Go process (not SQLite), so
// it is resistant to system-clock tampering. hmac + prev_hmac form a chain
// that makes the log tamper-evident. diff_hash binds the actual unified
// diff content into that chain. clock_tamper_warning is set when a row's
// recorded_at jumps backward by more than ClockSkewTolerance seconds.
type Change struct {
	ID                 int64
	FilePath           string
	CommitHash         string
	Branch             string
	Source             string // "agent", "llm", or "fallback"
	Summary            string
	Reason             string
	DiffStat           string
	DiffHash           string
	AgentID            string
	RecordedAt         int64
	HMAC               string
	PrevHMAC           string
	ClockTamperWarning bool
	CreatedAt          string
}

const schema = `
CREATE TABLE IF NOT EXISTS changes (
	id                     INTEGER PRIMARY KEY AUTOINCREMENT,
	file_path              TEXT NOT NULL,
	commit_hash            TEXT,
	branch                 TEXT NOT NULL DEFAULT '',
	source                 TEXT NOT NULL,
	summary                TEXT NOT NULL,
	reason                 TEXT,
	diff_stat              TEXT,
	diff_hash              TEXT NOT NULL DEFAULT '',
	agent_id               TEXT NOT NULL DEFAULT '',
	recorded_at            INTEGER NOT NULL DEFAULT 0,
	hmac                   TEXT NOT NULL DEFAULT '',
	prev_hmac              TEXT NOT NULL DEFAULT '',
	clock_tamper_warning   INTEGER NOT NULL DEFAULT 0,
	created_at             TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_changes_file   ON changes(file_path);
CREATE INDEX IF NOT EXISTS idx_changes_commit ON changes(commit_hash);

CREATE VIRTUAL TABLE IF NOT EXISTS changes_fts USING fts5(
	summary, reason, content='changes', content_rowid='id'
);
CREATE TRIGGER IF NOT EXISTS changes_ai AFTER INSERT ON changes BEGIN
	INSERT INTO changes_fts(rowid, summary, reason) VALUES (new.id, new.summary, new.reason);
END;

CREATE TABLE IF NOT EXISTS githints_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// migrations runs additive ALTER statements that are safe to re-apply.
// Each is guarded so it only adds the column if it is missing, which keeps
// existing stores upgradeable without a separate migration framework.
// The branch index lives here (not in schema) so it is only created once
// the branch column exists on legacy stores — schema runs first, its
// CREATE TABLE IF NOT EXISTS is a no-op on a pre-existing table, then
// migrations add the column and the index that depends on it.
const migrations = `
ALTER TABLE changes ADD COLUMN branch TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_changes_branch ON changes(branch);
ALTER TABLE changes ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN recorded_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE changes ADD COLUMN hmac TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN prev_hmac TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_changes_recorded ON changes(recorded_at);
ALTER TABLE changes ADD COLUMN diff_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE changes ADD COLUMN clock_tamper_warning INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS githints_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
ALTER TABLE changes ADD COLUMN source_new TEXT NOT NULL DEFAULT 'fallback';
UPDATE changes SET source_new = CASE WHEN source = 'hook' THEN 'fallback' ELSE source END;
ALTER TABLE changes DROP COLUMN source;
ALTER TABLE changes RENAME COLUMN source_new TO source;
`

// pragma sets the connection-level options that make SQLite safe for the
// concurrent access pattern githints has: the MCP server (long-lived) and
// the post-commit hook (short-lived) can both touch store.db at once, and
// an agent mid-record can overlap a git commit. WAL gives readers and
// writers independent views; busy_timeout makes a writer wait briefly
// instead of failing with "database is locked".
const pragma = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA synchronous=NORMAL;
`

// Open creates/opens the SQLite store at path, applies the schema and any
// additive migrations, and sets the connection pragmas.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(pragma); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply pragmas: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	var lastRecorded int64
	if v, err := metaGet(db, "last_recorded_at"); err == nil && v != "" {
		lastRecorded, _ = strconv.ParseInt(v, 10, 64)
	}

	return &Store{db: db, lastRecordedAt: lastRecorded}, nil
}

// metaGet reads a value from githints_meta. It returns the empty string and
// no error when the key is absent.
func metaGet(q queryer, key string) (string, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM githints_meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func metaSet(q queryer, key, value string) error {
	_, err := q.Exec(
		`INSERT INTO githints_meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

// queryer is the common surface needed by metaGet/metaSet so they work with
// both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}

// applyMigrations runs each ALTER and ignores the "duplicate column" error
// that SQLite returns when the column already exists. This is the cheapest
// way to support upgrading an existing store in place.
func applyMigrations(db *sql.DB) error {
	for _, stmt := range strings.Split(migrations, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			// modernc.org/sqlite reports a duplicate-column add as a
			// specific error string; treat that as success.
			if !strings.Contains(err.Error(), "duplicate column") {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Insert records one change row and returns its id. It also checks for a
// backward recorded_at jump and sets clock_tamper_warning if one is found.
func (s *Store) Insert(c Change) (int64, error) {
	s.mu.Lock()
	warning := c.RecordedAt != 0 && s.lastRecordedAt != 0 && c.RecordedAt < s.lastRecordedAt-ClockSkewTolerance
	if c.RecordedAt > s.lastRecordedAt {
		s.lastRecordedAt = c.RecordedAt
	}
	s.mu.Unlock()

	if warning {
		fmt.Fprintf(os.Stderr,
			"githints: WARNING: recorded_at jumped backward by %d seconds — possible clock tamper; row flagged\n",
			s.lastRecordedAt-c.RecordedAt)
	}

	if err := metaSet(s.db, "last_recorded_at", strconv.FormatInt(s.lastRecordedAt, 10)); err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}

	res, err := s.db.Exec(
		`INSERT INTO changes (file_path, commit_hash, branch, source, summary, reason, diff_stat, diff_hash, agent_id, recorded_at, hmac, prev_hmac, clock_tamper_warning)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.FilePath, c.CommitHash, c.Branch, c.Source, c.Summary, c.Reason, c.DiffStat, c.DiffHash,
		c.AgentID, c.RecordedAt, c.HMAC, c.PrevHMAC, boolToInt(warning),
	)
	if err != nil {
		return 0, fmt.Errorf("insert change: %w", err)
	}
	return res.LastInsertId()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FileHistory returns the most recent changes for one file, newest first.
// Ordering is by recorded_at (Go-managed, tamper-resistant) then id.
func (s *Store) FileHistory(filePath string, limit int) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes WHERE file_path = ? ORDER BY recorded_at DESC, id DESC LIMIT ?`,
		filePath, limit,
	)
}

// RecentChanges returns the most recent changes across the whole repo.
func (s *Store) RecentChanges(limit int) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes ORDER BY recorded_at DESC, id DESC LIMIT ?`,
		limit,
	)
}

// Search runs an FTS5 query over summary + reason text.
func (s *Store) Search(query string, limit int) ([]Change, error) {
	return s.query(
		`SELECT c.id, c.file_path, COALESCE(c.commit_hash,''), COALESCE(c.branch,''), c.source, c.summary, COALESCE(c.reason,''), COALESCE(c.diff_stat,''), COALESCE(c.diff_hash,''), COALESCE(c.agent_id,''), c.recorded_at, COALESCE(c.hmac,''), COALESCE(c.prev_hmac,''), c.clock_tamper_warning, c.created_at
		 FROM changes_fts f
		 JOIN changes c ON c.id = f.rowid
		 WHERE changes_fts MATCH ?
		 ORDER BY c.recorded_at DESC LIMIT ?`,
		query, limit,
	)
}

// ChangesInRange returns changes with recorded_at between since and until
// (inclusive), optionally filtered to one file. Newest first.
func (s *Store) ChangesInRange(since, until int64, filePath string, limit int) ([]Change, error) {
	if filePath != "" {
		return s.query(
			`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		     FROM changes
		     WHERE file_path = ? AND recorded_at >= ? AND recorded_at <= ?
		     ORDER BY recorded_at DESC, id DESC LIMIT ?`,
			filePath, since, until, limit,
		)
	}
	return s.query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes
		 WHERE recorded_at >= ? AND recorded_at <= ?
		 ORDER BY recorded_at DESC, id DESC LIMIT ?`,
		since, until, limit,
	)
}

// ChangesForCommit returns all rows associated with one commit hash.
func (s *Store) ChangesForCommit(commitHash string) ([]Change, error) {
	return s.query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes WHERE commit_hash = ? ORDER BY id ASC`,
		commitHash,
	)
}

// AllChanges returns every row ordered by id (the natural chain order).
// Used for HMAC verification and Merkle-root computation.
func (s *Store) AllChanges() ([]Change, error) {
	return s.query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes ORDER BY id ASC`,
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
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes WHERE file_path = ? AND commit_hash = ? AND source = 'agent'`,
		filePath, commitHash,
	)
}

// HasPendingAgentRecord reports whether there is at least one agent-recorded
// row for filePath that is still pending (commit_hash empty). The pre-commit
// hook uses this to flag staged files the agent forgot to record_change.
func (s *Store) HasPendingAgentRecord(filePath string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM changes
		 WHERE file_path = ? AND source = 'agent'
		   AND (commit_hash = '' OR commit_hash IS NULL)`,
		filePath,
	).Scan(&n)
	return n > 0, err
}

// ClaimPending assigns commitHash to any agent-recorded rows for filePath
// that are still "pending" (recorded before any commit existed for them,
// commit_hash = ”). Call this for every changed file as the first step
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
	return scanChanges(rows)
}

func scanChanges(rows *sql.Rows) ([]Change, error) {
	var out []Change
	for rows.Next() {
		var c Change
		var warning int
		if err := rows.Scan(
			&c.ID, &c.FilePath, &c.CommitHash, &c.Branch, &c.Source,
			&c.Summary, &c.Reason, &c.DiffStat, &c.DiffHash, &c.AgentID,
			&c.RecordedAt, &c.HMAC, &c.PrevHMAC, &warning, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		c.ClockTamperWarning = warning != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// WithTx runs fn inside a SQLite transaction. It is the building block for
// the post-commit hook's atomic claim-check-insert sequence, preventing the
// race where a concurrent record_change lands between ClaimPending and the
// fallback Insert.
func (s *Store) WithTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback after error (%v): %w", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// InsertTx is the transaction-scoped variant of Insert. It does NOT perform
// the write-time clock-skew check because that check is process-global and
// cannot be rolled back with the transaction; callers should use Insert for
// normal agent recordings and InsertTx only inside the post-commit hook.
func InsertTx(tx *sql.Tx, c Change) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO changes (file_path, commit_hash, branch, source, summary, reason, diff_stat, diff_hash, agent_id, recorded_at, hmac, prev_hmac, clock_tamper_warning)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.FilePath, c.CommitHash, c.Branch, c.Source, c.Summary, c.Reason, c.DiffStat, c.DiffHash,
		c.AgentID, c.RecordedAt, c.HMAC, c.PrevHMAC, boolToInt(c.ClockTamperWarning),
	)
	if err != nil {
		return 0, fmt.Errorf("insert change: %w", err)
	}
	if err := metaSet(tx, "last_recorded_at", fmt.Sprintf("%d", c.RecordedAt)); err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}
	return res.LastInsertId()
}

func AgentRecordsForCommitTx(tx *sql.Tx, filePath, commitHash string) ([]Change, error) {
	rows, err := tx.Query(
		`SELECT id, file_path, COALESCE(commit_hash,''), COALESCE(branch,''), source, summary, COALESCE(reason,''), COALESCE(diff_stat,''), COALESCE(diff_hash,''), COALESCE(agent_id,''), recorded_at, COALESCE(hmac,''), COALESCE(prev_hmac,''), clock_tamper_warning, created_at
		 FROM changes WHERE file_path = ? AND commit_hash = ? AND source = 'agent'`,
		filePath, commitHash,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return scanChanges(rows)
}

func ClaimPendingTx(tx *sql.Tx, filePath, commitHash string) (int64, error) {
	res, err := tx.Exec(
		`UPDATE changes SET commit_hash = ?
		 WHERE file_path = ? AND source = 'agent' AND (commit_hash = '' OR commit_hash IS NULL)`,
		commitHash, filePath,
	)
	if err != nil {
		return 0, fmt.Errorf("claim pending rows for %s: %w", filePath, err)
	}
	return res.RowsAffected()
}

// LastHMAC returns the hmac of the most recently inserted row (by id), or
// an empty string if the table is empty. It is used to link a new row into
// the tamper-evident chain.
func (s *Store) LastHMAC() (string, error) {
	var hmac string
	err := s.db.QueryRow(`SELECT COALESCE(hmac,'') FROM changes ORDER BY id DESC LIMIT 1`).Scan(&hmac)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("last hmac: %w", err)
	}
	return hmac, nil
}

func LastHMACTx(tx *sql.Tx) (string, error) {
	var hmac string
	err := tx.QueryRow(`SELECT COALESCE(hmac,'') FROM changes ORDER BY id DESC LIMIT 1`).Scan(&hmac)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("last hmac: %w", err)
	}
	return hmac, nil
}

// FilesWithHistory returns every distinct file_path that has at least one
// recorded change, ordered alphabetically. Used by the markdown integrity
// verifier to know which per-file hint files should exist.
func (s *Store) FilesWithHistory() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT file_path FROM changes ORDER BY file_path`)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *Store) MetaGet(key string) (string, error) {
	v, err := metaGet(s.db, key)
	if err != nil {
		return "", fmt.Errorf("meta get %s: %w", key, err)
	}
	return v, nil
}

func (s *Store) MetaSet(key, value string) error {
	if err := metaSet(s.db, key, value); err != nil {
		return fmt.Errorf("meta set %s: %w", key, err)
	}
	return nil
}

func (s *Store) UncommittedCount() (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM changes WHERE commit_hash = '' OR commit_hash IS NULL`,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("uncommitted count: %w", err)
	}
	return n, nil
}

func (s *Store) Count() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM changes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count changes: %w", err)
	}
	return n, nil
}
