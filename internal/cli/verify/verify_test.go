package verify

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/cjrdz/githints/internal/cli/hooks"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/store"
)

func TestVerifyDetectsRowsDeletedAfterAnchoring(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// The hooks init installed would run the test binary, so the commit below
	// bypasses them and HookRun is called directly instead.
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := record.Record([]string{"-file=" + f, "-summary=add " + f}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	clitest.RunGit(t, dir, "add", "a.go", "b.go")
	clitest.RunGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "first")
	if err := hooks.HookRun(); err != nil {
		t.Fatalf("HookRun: %v", err)
	}
	if err := Run(); err != nil {
		t.Fatalf("verify on an untouched log: %v", err)
	}

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.AllChanges()
	if err != nil || len(rows) < 2 {
		t.Fatalf("AllChanges: %d rows, %v", len(rows), err)
	}
	last := rows[len(rows)-1].ID
	if err := st.WithTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM changes WHERE id = ?", last)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := Run(); err == nil {
		t.Fatal("verify passed after the newest anchored row was deleted")
	}
}
