package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
)

func TestIndexGraphCommand(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := Run(nil); err == nil || !strings.Contains(err.Error(), "githints index") {
		t.Fatalf("graph before indexing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}

	// Build the structural index directly instead of going through indexcmd,
	// because indexcmd imports graph and a graph package test cannot import
	// indexcmd without creating an import cycle.
	db, err := index.Open(lang.IndexDBPath(dir))
	if err != nil {
		t.Fatalf("open index db: %v", err)
	}
	cfg := config.Default()
	if err := index.FullScan(db, lang.ScanOptions{
		Root:         dir,
		Languages:    cfg.Index.Languages,
		MaxFileSize:  int64(cfg.Index.MaxFileSize),
		ParseTimeout: time.Duration(cfg.Index.ParseTimeoutMS) * time.Millisecond,
		Obsidian:     cfg.Index.ObsidianWikilinks,
	}, false, cfg.Index.MaxBytes); err != nil {
		t.Fatalf("full scan: %v", err)
	}
	db.Close()

	if _, err := clitest.CaptureStdout(t, func() error { return Run(nil) }); err != nil {
		t.Fatalf("index graph: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(dir, ".githints", "graph.html"))
	if err != nil || !strings.Contains(string(page), "main.go") {
		t.Fatalf("graph.html: %v", err)
	}

	out, err := clitest.CaptureStdout(t, func() error { return Run([]string{"-format=dot", "-external"}) })
	if err != nil || !strings.Contains(out, `"main.go" -> "ext:fmt"`) {
		t.Fatalf("dot to stdout: %v\n%s", err, out)
	}
	if err := Run([]string{"-format=svg"}); err == nil {
		t.Error("unknown format accepted")
	}
	if err := Run([]string{"-focus=../x"}); err == nil {
		t.Error("traversal in -focus accepted")
	}
}
