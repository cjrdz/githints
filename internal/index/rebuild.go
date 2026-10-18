package index

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/cjrdz/githints/internal/index/lang"
)

// metaKeysComplete is set when file_keys holds the key of every indexed file:
// after a full rebuild, or after the hook recomputed them all. Until then the
// hook recomputes every key, as it always used to.
const metaKeysComplete = "import_keys_complete"

// rebuildIndexes are the secondary indexes on the tables a full rebuild
// replaces. They are dropped before the rows go in and created after:
// maintaining them row by row was most of a full index's database write
// (280,000 symbols: 3.5 s, against 1.4 s this way). The names and
// definitions must match dataSchema; TestRebuildIndexesMatchSchema keeps
// them in step.
var rebuildIndexes = []string{
	"idx_symbols_file ON symbols(file_path)",
	"idx_symbols_name ON symbols(name)",
	"idx_symbols_language ON symbols(language)",
	"idx_imports_file ON imports(file_path)",
	"idx_imports_path ON imports(imported_path)",
	"idx_facets_facet ON facets(facet)",
	"idx_facets_file ON facets(file_path)",
	"idx_facets_framework ON facets(framework)",
	"idx_file_keys_key ON file_keys(import_key)",
}

// rebuildRows is everything a full scan writes.
type rebuildRows struct {
	symbols   []lang.Symbol
	imports   []lang.Import
	facets    []lang.Detected
	meta      lang.IndexMeta
	languages []string

	// keys supplies each indexed file's import key. It is called after the
	// rows are written: the keys are computed while the rows go in.
	keys func() map[string]string
}

// rebuild replaces the whole index in one transaction.
//
// One transaction rather than a clear and a commit per table: a reader such
// as the MCP server used to be able to see an empty or half-written index
// between them, and a crash left one behind.
func (s *Store) rebuild(r rebuildRows) error {
	return s.withTx(func(tx *sql.Tx) error {
		for _, table := range []string{"symbols", "imports", "facets", "file_keys"} {
			if _, err := tx.Exec("DELETE FROM " + table); err != nil {
				return fmt.Errorf("clear %s: %w", table, err)
			}
		}
		for _, def := range rebuildIndexes {
			name, _, _ := strings.Cut(def, " ")
			if _, err := tx.Exec("DROP INDEX IF EXISTS " + name); err != nil {
				return fmt.Errorf("drop index %s: %w", name, err)
			}
		}
		if err := insertRows(tx, "symbols", []string{"name", "kind", "file_path", "line_start", "line_end", "signature", "language"},
			len(r.symbols), func(i int, args []any) []any {
				s := r.symbols[i]
				return append(args, s.Name, string(s.Kind), s.FilePath, s.LineStart, s.LineEnd, s.Signature, s.Language)
			}); err != nil {
			return err
		}
		if err := insertRows(tx, "imports", []string{"file_path", "imported_path"},
			len(r.imports), func(i int, args []any) []any {
				return append(args, r.imports[i].FilePath, r.imports[i].ImportedPath)
			}); err != nil {
			return err
		}
		if err := insertRows(tx, "facets", []string{"file_path", "facet", "framework", "name", "detail", "line"},
			len(r.facets), func(i int, args []any) []any {
				f := r.facets[i]
				return append(args, f.FilePath, f.Facet, f.Framework, f.Name, f.Detail, f.Line)
			}); err != nil {
			return err
		}
		var keys map[string]string
		if r.keys != nil {
			keys = r.keys()
		}
		files := lang.SortedKeys(countKeys(keys))
		if err := insertRows(tx, "file_keys", []string{"file_path", "import_key"},
			len(files), func(i int, args []any) []any {
				return append(args, files[i], keys[files[i]])
			}); err != nil {
			return err
		}
		for _, def := range rebuildIndexes {
			if _, err := tx.Exec("CREATE INDEX " + def); err != nil {
				return fmt.Errorf("create index %s: %w", def, err)
			}
		}
		if err := setMetaOn(tx, r.meta); err != nil {
			return err
		}
		if err := metaSetOn(tx, "resolver_version", fmt.Sprintf("%d", ResolverVersion)); err != nil {
			return err
		}
		if err := setIndexedLanguagesOn(tx, r.languages); err != nil {
			return err
		}
		return metaSetOn(tx, metaKeysComplete, "1")
	})
}

// countKeys adapts a key map for SortedKeys, which sorts map[string]int.
func countKeys(m map[string]string) map[string]int {
	out := make(map[string]int, len(m))
	for k := range m {
		out[k] = 0
	}
	return out
}

// rowsPerInsert is how many rows one INSERT statement carries. A statement
// per row spent most of its time crossing database/sql and the driver.
const rowsPerInsert = 100

// insertRows inserts n rows into table, rowsPerInsert at a time. args
// appends row i's values, in column order.
func insertRows(tx *sql.Tx, table string, cols []string, n int, args func(i int, args []any) []any) error {
	if n == 0 {
		return nil
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	prefix := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES "
	var full *sql.Stmt
	defer func() {
		if full != nil {
			_ = full.Close() // closed with the transaction regardless
		}
	}()
	vals := make([]any, 0, rowsPerInsert*len(cols))
	for start := 0; start < n; start += rowsPerInsert {
		count := min(rowsPerInsert, n-start)
		vals = vals[:0]
		for i := start; i < start+count; i++ {
			vals = args(i, vals)
		}
		var err error
		if count == rowsPerInsert {
			if full == nil {
				if full, err = tx.Prepare(prefix + strings.TrimSuffix(strings.Repeat(row+",", count), ",")); err != nil {
					return fmt.Errorf("prepare %s insert: %w", table, err)
				}
			}
			_, err = full.Exec(vals...)
		} else {
			_, err = tx.Exec(prefix+strings.TrimSuffix(strings.Repeat(row+",", count), ","), vals...)
		}
		if err != nil {
			return fmt.Errorf("insert %s: %w", table, err)
		}
	}
	return nil
}

// keyLookup answers the two questions a note asks about import keys: the key
// a file is imported by, and the file that provides a key (the first such
// file in path order, when several share one, as a Go package's do).
type keyLookup interface {
	keyOf(file string) (string, bool)
	fileFor(key string) (string, bool)
}

func (k importKeys) keyOf(file string) (string, bool) {
	v, ok := k.ofFile[file]
	return v, ok
}

func (k importKeys) fileFor(key string) (string, bool) {
	v, ok := k.toFile[key]
	return v, ok
}

// storedKeys looks keys up in file_keys. The hook renders a handful of notes,
// and recomputing every indexed file's key for them was most of its time on a
// large repository (60 ms on Qt's source).
type storedKeys struct{ db *Store }

func (k storedKeys) keyOf(file string) (string, bool) {
	var key string
	if err := k.db.db.QueryRow("SELECT import_key FROM file_keys WHERE file_path = ?", file).Scan(&key); err != nil {
		return "", false
	}
	return key, true
}

func (k storedKeys) fileFor(key string) (string, bool) {
	var file string
	if err := k.db.db.QueryRow("SELECT file_path FROM file_keys WHERE import_key = ? ORDER BY file_path LIMIT 1", key).Scan(&file); err != nil {
		return "", false
	}
	return file, true
}

// keysComplete reports whether file_keys holds every indexed file's key.
func (s *Store) keysComplete() bool {
	v, err := s.metaGet(metaKeysComplete)
	return err == nil && v == "1"
}

// replaceKeys stores a complete set of keys.
func (s *Store) replaceKeys(keys map[string]string) error {
	files := lang.SortedKeys(countKeys(keys))
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM file_keys"); err != nil {
			return fmt.Errorf("clear import keys: %w", err)
		}
		if err := insertRows(tx, "file_keys", []string{"file_path", "import_key"}, len(files), func(i int, args []any) []any {
			return append(args, files[i], keys[files[i]])
		}); err != nil {
			return err
		}
		return metaSetOn(tx, metaKeysComplete, "1")
	})
}

// setKeys stores the keys of the files a scan just wrote. Their old keys went
// with their rows (DeleteFile).
func (s *Store) setKeys(keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	files := lang.SortedKeys(countKeys(keys))
	return s.withTx(func(tx *sql.Tx) error {
		for _, f := range files {
			if _, err := tx.Exec("DELETE FROM file_keys WHERE file_path = ?", f); err != nil {
				return fmt.Errorf("replace import key for %s: %w", f, err)
			}
		}
		return insertRows(tx, "file_keys", []string{"file_path", "import_key"}, len(files), func(i int, args []any) []any {
			return append(args, files[i], keys[files[i]])
		})
	})
}

// storedKeyMap returns every stored key, for verification.
func (s *Store) storedKeyMap() (map[string]string, error) {
	rows, err := s.db.Query("SELECT file_path, import_key FROM file_keys")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var f, k string
		if err := rows.Scan(&f, &k); err != nil {
			return nil, err
		}
		out[f] = k
	}
	return out, rows.Err()
}
