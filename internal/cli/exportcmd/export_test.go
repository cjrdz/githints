package exportcmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/cli/record"
	"github.com/cjrdz/githints/internal/clitest"
)

func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestExportCommand(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := record.Record([]string{"-file=a.go", "-summary=added a.go", "-reason=because"}); err != nil {
		t.Fatal(err)
	}
	if err := record.Outcome([]string{"-id=1", "-state=failed", "-note=does not build"}); err != nil {
		t.Fatal(err)
	}

	pack := filepath.Join(t.TempDir(), "pack")
	out, err := capture(t, func() error { return Run([]string{"-o", pack, "-duckdb"}, "test-version") })
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, want := range []string{"exported to", "changes: 1", "outcomes: 1", "verify at export time: chain ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, f := range []string{"changes.jsonl", "changes.csv", "CHANGES-full.md", "manifest.json", "githints.duckdb.sql", "hints/a.go.md"} {
		if _, err := os.Stat(filepath.Join(pack, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	if m, _ := os.ReadFile(filepath.Join(pack, "manifest.json")); !strings.Contains(string(m), `"version": "test-version"`) {
		t.Errorf("the version is not in the manifest:\n%s", m)
	}
	// An export leaves the repository's own state alone.
	if _, err := os.Stat(filepath.Join(dir, "changes.jsonl")); err == nil {
		t.Error("the export wrote into the repository")
	}
}

func TestExportCommandRefusals(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)

	// Only init creates .githints/.
	if _, err := capture(t, func() error { return Run([]string{"-o", filepath.Join(t.TempDir(), "p")}, "v") }); err == nil {
		t.Error("export succeeded in an uninitialized repository")
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints")); err == nil {
		t.Error("export created .githints/")
	}

	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for name, args := range map[string][]string{
		"no -o":     {},
		"stray arg": {"-o", filepath.Join(t.TempDir(), "p"), "extra"},
	} {
		if _, err := capture(t, func() error { return Run(args, "v") }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
