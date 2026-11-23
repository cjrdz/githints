package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSchemaIndex writes an index.db at path holding one symbol, stamped with
// schema version 1, as an older githints would have left it.
func oldSchemaIndex(t *testing.T, path string) {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{
		`INSERT INTO symbols (name, kind, file_path, line_start, line_end) VALUES ('Keep', 'func', 'a.go', 1, 2)`,
		`UPDATE meta SET value = '1' WHERE key = 'schema_version'`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func symbolCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// OpenReadOnly reports an index from another schema version instead of
// resetting it, and cannot write. Open on the same file would drop its tables,
// which is what doctor and `upgrade -dry-run` used to do.
func TestOpenReadOnlyLeavesAnOldIndexAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a dir #1", "index.db") // URI escaping
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSchemaIndex(t, path)

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	ok, stored, err := ro.SchemaCurrent()
	if err != nil || ok || stored != "1" {
		t.Errorf("SchemaCurrent = %v, %q, %v; want false, \"1\"", ok, stored, err)
	}
	if n := symbolCount(t, ro); n != 1 {
		t.Errorf("read-only open sees %d symbols", n)
	}
	if _, err := ro.db.Exec(`DELETE FROM symbols`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("a write on the read-only index: %v", err)
	}
	ro.Close()

	ro, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if n := symbolCount(t, ro); n != 1 {
		t.Errorf("after the read-only open the index holds %d symbols", n)
	}
}

func TestOpenReadOnlyNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("opened a missing index")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("OpenReadOnly created index.db")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ok, stored, err := ro.SchemaCurrent(); err != nil || !ok {
		t.Errorf("a current index: SchemaCurrent = %v, %q, %v", ok, stored, err)
	}
}

// synchronous and busy_timeout are per connection, so they ride the DSN: an
// Exec set them on one pooled connection only.
func TestIndexPragmasReachEveryConnection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxOpenConns(4)
	conns := make([]*sql.Conn, 4)
	for i := range conns {
		c, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns[i] = c
	}
	for i, c := range conns {
		var sync, timeout int
		if err := c.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if sync != 1 || timeout != 5000 {
			t.Errorf("connection %d: synchronous %d, busy_timeout %d; want 1 and 5000", i, sync, timeout)
		}
	}
}
