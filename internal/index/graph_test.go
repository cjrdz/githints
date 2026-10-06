package index

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

// graphFixture builds a repo with a two-file Go package, a one-file Go
// package, a root package, and two TypeScript modules, then indexes it.
func graphFixture(t *testing.T) (*Store, string) {
	t.Helper()
	st, dir := tempStore(t)
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	for _, d := range []string{"store", "util", "web"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGo(t, root, "go.mod", "module example.com/m\n\ngo 1.23\n")
	writeGo(t, root, "main.go", "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/m/store\"\n)\n\nfunc main() { fmt.Println(store.Get()) }\n")
	writeGo(t, root, "store/store.go", "package store\n\nimport \"example.com/m/util\"\n\nfunc Get() int { return util.One() }\n")
	writeGo(t, root, "store/store_more.go", "package store\n\nfunc More() {}\n")
	writeGo(t, root, "util/util.go", "package util\n\nfunc One() int { return 1 }\n")
	writeGo(t, root, "web/app.ts", "import { b } from './b';\nexport const a = b;\n")
	writeGo(t, root, "web/b.ts", "export const b = 1;\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"go", "typescript"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	return st, root
}

func edgeSet(g Graph) map[[2]string]bool {
	m := map[[2]string]bool{}
	for _, e := range g.Edges {
		m[[2]string{e.From, e.To}] = true
	}
	return m
}

func TestBuildGraphGroupsPackagesAndResolvesFiles(t *testing.T) {
	st, root := graphFixture(t)
	g, err := BuildGraph(st, root, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	edges := edgeSet(g)
	for _, want := range [][2]string{
		{"main.go", "store/"},      // single-file root package stays a file
		{"store/", "util/util.go"}, // single-file package stays a file
		{"web/app.ts", "web/b.ts"},
	} {
		if !edges[want] {
			t.Errorf("missing edge %v; have %v", want, g.Edges)
		}
	}
	var pkg *GraphNode
	for i := range g.Nodes {
		if g.Nodes[i].ID == "store/" {
			pkg = &g.Nodes[i]
		}
	}
	if pkg == nil || len(pkg.Files) != 2 || pkg.InDegree != 1 || pkg.Language != "go" {
		t.Fatalf("store package node = %+v", pkg)
	}
	for _, n := range g.Nodes {
		if n.External {
			t.Errorf("external node %s without IncludeExternal", n.ID)
		}
	}
}

func TestBuildGraphExternalFocusAndCap(t *testing.T) {
	st, root := graphFixture(t)

	g, err := BuildGraph(st, root, GraphOptions{IncludeExternal: true})
	if err != nil {
		t.Fatal(err)
	}
	if !edgeSet(g)[[2]string{"main.go", "ext:fmt"}] {
		t.Errorf("external import not included: %v", g.Edges)
	}

	// Focus on a file inside the package resolves to the package node.
	g, err = BuildGraph(st, root, GraphOptions{Focus: "store/store_more.go", Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	if g.Focus != "store/" || !ids["main.go"] || !ids["util/util.go"] || ids["web/app.ts"] {
		t.Errorf("focus graph: focus=%q nodes=%v", g.Focus, ids)
	}

	// The cap keeps the focus even when it is not among the most connected.
	g, err = BuildGraph(st, root, GraphOptions{Focus: "web/b.ts", Depth: 3, MaxNodes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Truncated || len(g.Nodes) != 1 || g.Nodes[0].ID != "web/b.ts" {
		t.Errorf("capped focus graph: %+v", g)
	}

	for _, bad := range []GraphOptions{{Focus: "nope.go", Depth: 1}, {Focus: "main.go", Depth: 0}, {MaxNodes: MaxGraphNodes + 1}} {
		if _, err := BuildGraph(st, root, bad); err == nil {
			t.Errorf("BuildGraph(%+v) accepted", bad)
		}
	}
}
