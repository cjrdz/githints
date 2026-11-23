package index

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OpenReadOnly opens an existing index.db for inspection, for `doctor` and
// `upgrade -dry-run`, which promise to change nothing. Open is not that: it
// applies the schema, and on a schema version mismatch it drops every data
// table, so merely asking whether an index from another githints was stale
// used to destroy it. Here no schema is applied, a missing file is an error
// rather than created, and every write fails (SQLite's mode=ro). Ask
// SchemaCurrent before trusting the other queries.
func OpenReadOnly(path string) (*Store, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("database path %q contains '?', which the sqlite driver cannot open safely", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("open index db read-only: %w", err)
	}
	if _, err := os.Lstat(abs); err != nil {
		return nil, fmt.Errorf("open index db read-only: %w", err)
	}
	// Only a name starting with "file:" reaches SQLite as a URI, so only then
	// does mode=ro apply; a plain name would silently open read-write.
	db, err := sql.Open("sqlite", readOnlyURI(abs)+"?mode=ro&_pragma=trusted_schema(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open index db read-only: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open index db read-only: %w", err)
	}
	return &Store{db: db, dbPath: abs}, nil
}

// readOnlyURI is abs as a file: URI, escaped so that '#', '%' and spaces in a
// path stay part of the name. Windows drive paths get the leading slash the
// URI form needs (file:///C:/...).
func readOnlyURI(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// SchemaCurrent reports whether the index was written by this githints'
// schema version, and the version it was written by when not ("" for an
// index predating versioning or never written). Open would reset such an
// index; a reader should say a rebuild is needed instead.
func (s *Store) SchemaCurrent() (bool, string, error) {
	var stored string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", "schema_version").Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, "", nil
	case err != nil && strings.Contains(err.Error(), "no such table"):
		return false, "", nil
	case err != nil:
		return false, "", fmt.Errorf("read index schema version: %w", err)
	}
	return stored == strconv.Itoa(schemaVersion), stored, nil
}
