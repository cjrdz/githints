package index

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, dbPath: path}, nil
}

// schemaVersion is bumped whenever the shape of the index tables changes.
//
// There is no migration path and there does not need to be one: the index is a
// derived cache, gitignored and rebuilt from source. A version mismatch drops
// the data tables and recreates them, which is the same thing a migration
// would end up doing but without code that has to be right years later.
const schemaVersion = 2

// tableExists reports whether a table is present in the database.
func tableExists(db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("inspect index db schema: %w", err)
	}
	return true, nil
}

// applySchema creates the tables, resetting them first if the database was
// written by a different schema version.
//
// CREATE TABLE IF NOT EXISTS cannot add a column to a table that already
// exists, so without this an older index.db would keep its old shape and every
// query naming a new column would fail.
func applySchema(db *sql.DB) error {
	if _, err := db.Exec(metaSchema); err != nil {
		return fmt.Errorf("apply index db meta schema: %w", err)
	}

	var stored string
	err := db.QueryRow("SELECT value FROM meta WHERE key = ?", "schema_version").Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		stored = "" // fresh database, or one predating versioning
	case err != nil:
		return fmt.Errorf("read index schema version: %w", err)
	}

	current := strconv.Itoa(schemaVersion)
	if stored != current {
		// A database predating versioning has no schema_version row but does
		// have tables. Distinguishing it from a genuinely new file is what
		// makes the reset visible to the users who actually experience one.
		hadData, err := tableExists(db, "symbols")
		if err != nil {
			return err
		}
		if _, err := db.Exec(dropDataSchema); err != nil {
			return fmt.Errorf("reset index db schema: %w", err)
		}
		if hadData {
			from := stored
			if from == "" {
				from = "pre-versioning"
			}
			// Say so: the next incremental scan only touches changed files, so
			// the index stays sparse until a full rebuild.
			fmt.Fprintf(os.Stderr,
				"githints: index schema changed (%s -> %s); the structural index was reset. "+
					"Run `githints index` to rebuild it.\n", from, current)
		}
	}

	if _, err := db.Exec(dataSchema); err != nil {
		return fmt.Errorf("apply index db schema: %w", err)
	}
	if _, err := db.Exec(
		"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		"schema_version", current); err != nil {
		return fmt.Errorf("record index schema version: %w", err)
	}
	return nil
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

const metaSchema = `
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// dropDataSchema clears everything except meta, which carries the version.
const dropDataSchema = `
DROP TABLE IF EXISTS symbols;
DROP TABLE IF EXISTS imports;
DROP TABLE IF EXISTS facets;
`

const dataSchema = `
CREATE TABLE IF NOT EXISTS symbols (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	file_path TEXT NOT NULL,
	line_start INTEGER NOT NULL,
	line_end INTEGER NOT NULL,
	signature TEXT,
	language TEXT NOT NULL DEFAULT ''
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
CREATE INDEX IF NOT EXISTS idx_symbols_language ON symbols(language);

CREATE TABLE IF NOT EXISTS facets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	file_path TEXT NOT NULL,
	facet TEXT NOT NULL,
	framework TEXT NOT NULL,
	name TEXT NOT NULL,
	detail TEXT,
	line INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_facets_facet ON facets(facet);
CREATE INDEX IF NOT EXISTS idx_facets_file ON facets(file_path);
CREATE INDEX IF NOT EXISTS idx_facets_framework ON facets(framework);
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
	if _, err := s.db.Exec("DELETE FROM facets"); err != nil {
		return fmt.Errorf("clear facets: %w", err)
	}
	return nil
}

// DeleteFile removes all symbols, imports and facets for a single file path.
func (s *Store) DeleteFile(path string) error {
	if _, err := s.db.Exec("DELETE FROM symbols WHERE file_path = ?", path); err != nil {
		return fmt.Errorf("delete symbols for %s: %w", path, err)
	}
	if _, err := s.db.Exec("DELETE FROM imports WHERE file_path = ?", path); err != nil {
		return fmt.Errorf("delete imports for %s: %w", path, err)
	}
	if _, err := s.db.Exec("DELETE FROM facets WHERE file_path = ?", path); err != nil {
		return fmt.Errorf("delete facets for %s: %w", path, err)
	}
	return nil
}

// InsertSymbols writes a batch of symbols.
func (s *Store) InsertSymbols(symbols []lang.Symbol) error {
	if len(symbols) == 0 {
		return nil
	}
	stmt, err := s.db.Prepare("INSERT INTO symbols (name, kind, file_path, line_start, line_end, signature, language) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare symbols insert: %w", err)
	}
	defer stmt.Close()
	for _, sym := range symbols {
		_, err := stmt.Exec(sym.Name, string(sym.Kind), sym.FilePath, sym.LineStart, sym.LineEnd, sym.Signature, sym.Language)
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
	rows, err := s.db.Query("SELECT name, kind, file_path, line_start, line_end, signature, language FROM symbols WHERE file_path = ? ORDER BY line_start, name", path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.Symbol
	for rows.Next() {
		var sym lang.Symbol
		var kind string
		if err := rows.Scan(&sym.Name, &kind, &sym.FilePath, &sym.LineStart, &sym.LineEnd, &sym.Signature, &sym.Language); err != nil {
			return nil, err
		}
		sym.Kind = lang.SymbolKind(kind)
		out = append(out, sym)
	}
	return out, rows.Err()
}

// FindSymbolsByName returns exact and prefix matches for a name across the repo.
func (s *Store) FindSymbolsByName(name string) ([]lang.Symbol, error) {
	rows, err := s.db.Query("SELECT name, kind, file_path, line_start, line_end, signature, language FROM symbols WHERE name LIKE ? ESCAPE '\\' ORDER BY LENGTH(name), file_path, line_start", lang.EscapeLike(name)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lang.Symbol
	for rows.Next() {
		var sym lang.Symbol
		var kind string
		if err := rows.Scan(&sym.Name, &kind, &sym.FilePath, &sym.LineStart, &sym.LineEnd, &sym.Signature, &sym.Language); err != nil {
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

// AllIndexedFiles returns every file path that has symbols or imports.
// Import-only files (barrel re-exports, pages with no declarations) are
// included so they get notes and can act as link targets for dependents.
func (s *Store) AllIndexedFiles() ([]string, error) {
	// Facets are part of the union: a path-gated detector can match a file
	// that declares nothing and imports nothing, and such a file would
	// otherwise hold rows that never render a note.
	rows, err := s.db.Query("SELECT file_path FROM symbols UNION SELECT file_path FROM imports UNION SELECT file_path FROM facets ORDER BY file_path")
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

// ImportsForFile returns every import declared by file, ordered by imported
// path. It is the forward lookup backing the Imports note section.
func (s *Store) ImportsForFile(file string) ([]lang.Import, error) {
	rows, err := s.db.Query("SELECT file_path, imported_path FROM imports WHERE file_path = ? ORDER BY imported_path", file)
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

// Vacuum optimizes the index database after large writes/deletes.
func (s *Store) Vacuum() error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// InsertFacets writes a batch of detected facets.
func (s *Store) InsertFacets(facets []lang.Detected) error {
	if len(facets) == 0 {
		return nil
	}
	stmt, err := s.db.Prepare("INSERT INTO facets (file_path, facet, framework, name, detail, line) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare facets insert: %w", err)
	}
	defer stmt.Close()
	for _, f := range facets {
		if _, err := stmt.Exec(f.FilePath, f.Facet, f.Framework, f.Name, f.Detail, f.Line); err != nil {
			return fmt.Errorf("insert facet %s in %s: %w", f.Facet, f.FilePath, err)
		}
	}
	return nil
}

// FacetsForFile returns every facet detected in one file.
func (s *Store) FacetsForFile(path string) ([]lang.Detected, error) {
	return s.Facets(FacetFilter{File: path})
}

// FacetFilter narrows a facet query. Every field is optional; the zero value
// asks for everything, which is how a caller finds out what frameworks are in
// a repository at all.
type FacetFilter struct {
	Facet     string
	Framework string
	File      string
	Limit     int
}

// Facets returns detected facets matching a filter.
//
// The limit is applied in SQL rather than by slicing the result, so a query
// against a large repository does not materialize every row to return twenty.
func (s *Store) Facets(f FacetFilter) ([]lang.Detected, error) {
	query := "SELECT file_path, facet, framework, name, detail, line FROM facets"
	var (
		where []string
		args  []any
	)
	if f.Facet != "" {
		where = append(where, "facet = ?")
		args = append(args, f.Facet)
	}
	if f.Framework != "" {
		where = append(where, "framework = ?")
		args = append(args, f.Framework)
	}
	if f.File != "" {
		where = append(where, "file_path = ?")
		args = append(args, f.File)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY facet, file_path, line"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	return s.queryFacets(query, args...)
}

// FacetSummary is one row of the facet breakdown.
type FacetSummary struct {
	Facet     string
	Framework string
	Count     int
}

// FacetBreakdown counts facets by kind and framework, which is the fastest
// answer to "what is this repository built with".
func (s *Store) FacetBreakdown() ([]FacetSummary, error) {
	rows, err := s.db.Query("SELECT facet, framework, COUNT(*) FROM facets GROUP BY facet, framework ORDER BY facet, framework")
	if err != nil {
		return nil, fmt.Errorf("query facet breakdown: %w", err)
	}
	defer rows.Close()

	var out []FacetSummary
	for rows.Next() {
		var r FacetSummary
		if err := rows.Scan(&r.Facet, &r.Framework, &r.Count); err != nil {
			return nil, fmt.Errorf("scan facet breakdown: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) queryFacets(query string, args ...any) ([]lang.Detected, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query facets: %w", err)
	}
	defer rows.Close()

	var out []lang.Detected
	for rows.Next() {
		var f lang.Detected
		var detail sql.NullString
		if err := rows.Scan(&f.FilePath, &f.Facet, &f.Framework, &f.Name, &detail, &f.Line); err != nil {
			return nil, fmt.Errorf("scan facet: %w", err)
		}
		f.Detail = detail.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// FacetCount returns the number of detected facets.
func (s *Store) FacetCount() (int, error) {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM facets").Scan(&n); err != nil {
		return 0, fmt.Errorf("count facets: %w", err)
	}
	return n, nil
}
