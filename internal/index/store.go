package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cjrdz/githints/internal/index/lang"
	_ "modernc.org/sqlite"
)

// Store is the SQLite-backed index cache. It is a separate database from
// store.db so it can be freely deleted and rebuilt without affecting the
// integrity-verified change log.
type Store struct {
	db     *sql.DB
	dbPath string
}

// Open opens or creates index.db at path, applying the schema if necessary.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create index db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set index db wal mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set index db busy timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply index db schema: %w", err)
	}
	return &Store{db: db, dbPath: path}, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DBPath returns the underlying sqlite connection's data source path.
func (s *Store) DBPath() string {
	return s.dbPath
}

// Size returns the on-disk size of the database file, or -1 if it cannot be
// determined.
func (s *Store) Size() int64 {
	if s.dbPath == "" {
		return -1
	}
	info, err := os.Stat(s.dbPath)
	if err != nil {
		return -1
	}
	return info.Size()
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS symbols (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	file_path TEXT NOT NULL,
	line_start INTEGER NOT NULL,
	line_end INTEGER NOT NULL,
	signature TEXT
);

CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);

CREATE TABLE IF NOT EXISTS imports (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	file_path TEXT NOT NULL,
	imported_path TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_imports_file ON imports(file_path);
CREATE INDEX IF NOT EXISTS idx_imports_path ON imports(imported_path);
`

func (s *Store) metaGet(key string) (string, error) {
	var val string
	row := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key)
	if err := row.Scan(&val); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (s *Store) metaSet(key, value string) error {
	_, err := s.db.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?", key, value, value)
	return err
}

// LastIndexedAt returns the stored timestamp or 0 if never indexed.
func (s *Store) LastIndexedAt() (int64, error) {
	v, err := s.metaGet("last_indexed_at")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	var n int64
	_, err = fmt.Sscanf(v, "%d", &n)
	if err != nil {
		return 0, fmt.Errorf("read last_indexed_at: %w", err)
	}
	return n, nil
}

// SetMeta records the post-scan totals.
func (s *Store) SetMeta(meta lang.IndexMeta) error {
	lc, err := lang.EncodeLanguageCounts(meta.LanguageCounts)
	if err != nil {
		return err
	}
	pairs := map[string]string{
		"last_indexed_at":   fmt.Sprintf("%d", meta.LastIndexedAt),
		"file_count":        fmt.Sprintf("%d", meta.FileCount),
		"symbol_count":      fmt.Sprintf("%d", meta.SymbolCount),
		"language_counts":   lc,
		"skipped_count":     fmt.Sprintf("%d", meta.SkippedCount),
		"unsupported_count": fmt.Sprintf("%d", meta.UnsupportedCount),
	}
	for k, v := range pairs {
		if err := s.metaSet(k, v); err != nil {
			return fmt.Errorf("set meta %s: %w", k, err)
		}
	}
	return nil
}

// Meta returns the current metadata row.
func (s *Store) Meta() (lang.IndexMeta, error) {
	var m lang.IndexMeta
	var err error
	m.LastIndexedAt, err = s.LastIndexedAt()
	if err != nil {
		return m, err
	}
	m.FileCount, err = s.metaInt("file_count")
	if err != nil {
		return m, err
	}
	m.SymbolCount, err = s.metaInt("symbol_count")
	if err != nil {
		return m, err
	}
	lc, err := s.metaGet("language_counts")
	if err != nil {
		return m, err
	}
	m.LanguageCounts, err = lang.DecodeLanguageCounts(lc)
	if err != nil {
		return m, err
	}
	m.SkippedCount, err = s.metaInt("skipped_count")
	if err != nil {
		return m, err
	}
	m.UnsupportedCount, err = s.metaInt("unsupported_count")
	if err != nil {
		return m, err
	}
	return m, nil
}

func (s *Store) metaInt(key string) (int, error) {
	v, err := s.metaGet(key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	var n int
	_, err = fmt.Sscanf(v, "%d", &n)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", key, err)
	}
	return n, nil
}

// Clear deletes all symbol and import rows. Used before a full re-index.
func (s *Store) Clear() error {
	if _, err := s.db.Exec("DELETE FROM symbols"); err != nil {
		return fmt.Errorf("clear symbols: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM imports"); err != nil {
		return fmt.Errorf("clear imports: %w", err)
	}
	return nil
}

// DeleteFile removes all symbols and imports for a single file path.
func (s *Store) DeleteFile(path string) error {
	if _, err := s.db.Exec("DELETE FROM symbols WHERE file_path = ?", path); err != nil {
		return fmt.Errorf("delete symbols for %s: %w", path, err)
	}
	if _, err := s.db.Exec("DELETE FROM imports WHERE file_path = ?", path); err != nil {
		return fmt.Errorf("delete imports for %s: %w", path, err)
	}
	return nil
}

// InsertSymbols writes a batch of symbols.
func (s *Store) InsertSymbols(symbols []lang.Symbol) error {
	if len(symbols) == 0 {
		return nil
	}
	stmt, err := s.db.Prepare("INSERT INTO symbols (name, kind, file_path, line_start, line_end, signature) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare symbols insert: %w", err)
	}
	defer stmt.Close()
	for _, sym := range symbols {
		_, err := stmt.Exec(sym.Name, string(sym.Kind), sym.FilePath, sym.LineStart, sym.LineEnd, sym.Signature)
		if err != nil {
			return fmt.Errorf("insert symbol %s in %s: %w", sym.Name, sym.FilePath, err)
		}
	}
	return nil
}

// InsertImports writes a batch of imports.
func (s *Store) InsertImports(imports []lang.Import) error {
	if len(imports) == 0 {
		return nil
	}
	stmt, err := s.db.Prepare("INSERT INTO imports (file_path, imported_path) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare imports insert: %w", err)
	}
	defer stmt.Close()
	for _, imp := range imports {
		_, err := stmt.Exec(imp.FilePath, imp.ImportedPath)
		if err != nil {
			return fmt.Errorf("insert import %s in %s: %w", imp.ImportedPath, imp.FilePath, err)
		}
	}
	return nil
}

// SymbolCount returns the total number of symbols.
func (s *Store) SymbolCount() (int, error) {
	row := s.db.QueryRow("SELECT COUNT(*) FROM symbols")
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ImportCount returns the total number of imports.
func (s *Store) ImportCount() (int, error) {
	row := s.db.QueryRow("SELECT COUNT(*) FROM imports")
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// FileCount returns the number of distinct files that have symbols.
func (s *Store) FileCount() (int, error) {
	row := s.db.QueryRow("SELECT COUNT(DISTINCT file_path) FROM symbols")
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SymbolsForFile returns symbols for one file, ordered by line.
func (s *Store) SymbolsForFile(path string) ([]lang.Symbol, error) {
	rows, err := s.db.Query("SELECT name, kind, file_path, line_start, line_end, signature FROM symbols WHERE file_path = ? ORDER BY line_start, name", path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.Symbol
	for rows.Next() {
		var sym lang.Symbol
		var kind string
		if err := rows.Scan(&sym.Name, &kind, &sym.FilePath, &sym.LineStart, &sym.LineEnd, &sym.Signature); err != nil {
			return nil, err
		}
		sym.Kind = lang.SymbolKind(kind)
		out = append(out, sym)
	}
	return out, rows.Err()
}

// FindSymbolsByName returns exact and prefix matches for a name across the repo.
func (s *Store) FindSymbolsByName(name string) ([]lang.Symbol, error) {
	rows, err := s.db.Query("SELECT name, kind, file_path, line_start, line_end, signature FROM symbols WHERE name LIKE ? ESCAPE '\\' ORDER BY LENGTH(name), file_path, line_start", lang.EscapeLike(name)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.Symbol
	for rows.Next() {
		var sym lang.Symbol
		var kind string
		if err := rows.Scan(&sym.Name, &kind, &sym.FilePath, &sym.LineStart, &sym.LineEnd, &sym.Signature); err != nil {
			return nil, err
		}
		sym.Kind = lang.SymbolKind(kind)
		out = append(out, sym)
	}
	return out, rows.Err()
}

// FilesImporting returns the reverse lookup: files that import the given path
// and the import statement in that file.
func (s *Store) FilesImporting(importPath string) ([]lang.Import, error) {
	rows, err := s.db.Query("SELECT file_path, imported_path FROM imports WHERE imported_path = ? ORDER BY file_path", importPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.Import
	for rows.Next() {
		var imp lang.Import
		if err := rows.Scan(&imp.FilePath, &imp.ImportedPath); err != nil {
			return nil, err
		}
		out = append(out, imp)
	}
	return out, rows.Err()
}

// TopFilesByInDegree returns the most depended-on files up to limit. The
// file reported is the imported_path, and the count is how many files import it.
func (s *Store) TopFilesByInDegree(limit int) ([]lang.FileInDegreeSummary, error) {
	rows, err := s.db.Query("SELECT imported_path, COUNT(*) AS c FROM imports GROUP BY imported_path ORDER BY c DESC, imported_path LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.FileInDegreeSummary
	for rows.Next() {
		var item lang.FileInDegreeSummary
		if err := rows.Scan(&item.File, &item.Dependents); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AllIndexedFiles returns every file path that has symbols.
func (s *Store) AllIndexedFiles() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT file_path FROM symbols ORDER BY file_path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Vacuum optimizes the index database after large writes/deletes.
func (s *Store) Vacuum() error {
	_, err := s.db.Exec("VACUUM")
	return err
}
