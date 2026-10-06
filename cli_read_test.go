package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI diff is what CLI-mode agents read, so it carries the same
// guarantee as get_diff: credential files are redacted on the default path.
func TestCLIDiffScrubsUnconditionally(t *testing.T) {
	dir := chdirTempRepo(t)
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".env")
	runGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "env")
	if err := os.WriteFile(env, []byte("A=1\nDB_PASSWORD=hunter2-very-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return cmdDiff([]string{"-file=.env"}) })
	if err != nil {
		t.Fatalf("cmdDiff: %v", err)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "REDACTED") {
		t.Fatalf("diff was not scrubbed:\n%s", out)
	}
}

func TestCLIReadCommandsValidate(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	chdirTempRepo(t)
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"history without file": cmdHistory(nil),
		"history traversal":    cmdHistory([]string{"-file=../x"}),
		"recent limit 0":       cmdRecent([]string{"-limit=0"}),
		"search empty":         cmdSearch(nil),
		"search too long":      cmdSearch([]string{"-query=" + strings.Repeat("a", 501)}),
		"diff flag as hash":    cmdDiff([]string{"-file=a.go", "-hash=--output=/tmp/x"}),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := cmdRecord([]string{"-file=a.go", "-summary=rotate signing key", "-reason=security"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdSearch([]string{"-query=signing"}) })
	if err != nil || !strings.Contains(out, "rotate signing key") || !strings.Contains(out, "why: security") {
		t.Fatalf("search: %v\n%s", err, out)
	}
}

func TestIndexGraphCommand(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)
	if err := cmdIndexGraph(nil); err == nil || !strings.Contains(err.Error(), "githints index") {
		t.Fatalf("graph before indexing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return cmdIndex(nil) }); err != nil {
		t.Fatalf("index: %v", err)
	}

	if _, err := captureStdout(t, func() error { return cmdIndexGraph(nil) }); err != nil {
		t.Fatalf("index graph: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(dir, ".githints", "graph.html"))
	if err != nil || !strings.Contains(string(page), "main.go") {
		t.Fatalf("graph.html: %v", err)
	}

	out, err := captureStdout(t, func() error { return cmdIndexGraph([]string{"-format=dot", "-external"}) })
	if err != nil || !strings.Contains(out, `"main.go" -> "ext:fmt"`) {
		t.Fatalf("dot to stdout: %v\n%s", err, out)
	}
	if err := cmdIndexGraph([]string{"-format=svg"}); err == nil {
		t.Error("unknown format accepted")
	}
	if err := cmdIndexGraph([]string{"-focus=../x"}); err == nil {
		t.Error("traversal in -focus accepted")
	}
}
