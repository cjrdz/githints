package index

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/safefs"
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

// End to end through FullScan: a polyglot monorepo whose cross-package
// imports only connect with per-module go.mod, per-package tsconfig and
// workspace packages, and per-project Python roots.
func TestMonorepoGraphConnectsAcrossPackages(t *testing.T) {
	st, dir := tempStore(t)
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	files := map[string]string{
		// Go: two modules, no root go.mod.
		"go/api/go.mod":        "module example.com/api\n\ngo 1.23\n\nrequire example.com/shared v0.0.0\n",
		"go/api/main.go":       "package main\n\nimport \"example.com/shared/log\"\n\nfunc main() { log.Print() }\n",
		"go/shared/go.mod":     "module example.com/shared\n\ngo 1.23\n",
		"go/shared/log/log.go": "package log\n\nfunc Print() {}\n",
		// TypeScript: an app importing a workspace package and its own alias.
		"web/app/tsconfig.json": `{"compilerOptions":{"paths":{"~/*":["./src/*"]}}}`,
		"web/app/src/main.ts":   "import { Button } from \"@acme/ui\";\nimport { cfg } from \"~/config\";\nexport const m = Button;\n",
		"web/app/src/config.ts": "export const cfg = 1;\n",
		"web/ui/package.json":   `{"name":"@acme/ui","main":"dist/index.js"}`,
		"web/ui/src/index.ts":   "export const Button = 1;\n",
		// Python: a src-layout service with a relative import.
		"py/svc/pyproject.toml":      "[project]\nname='svc'\n",
		"py/svc/src/svc/__init__.py": "",
		"py/svc/src/svc/db.py":       "def connect():\n    pass\n",
		"py/svc/src/svc/api.py":      "from .db import connect\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeGo(t, root, rel, body)
	}
	opts := lang.ScanOptions{Root: root, Languages: []string{"go", "typescript", "python"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	g, err := BuildGraph(st, root, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	edges := edgeSet(g)
	for _, want := range [][2]string{
		{"go/api/main.go", "go/shared/log/log.go"},
		{"web/app/src/main.ts", "web/ui/src/index.ts"},
		{"web/app/src/main.ts", "web/app/src/config.ts"},
		{"py/svc/src/svc/api.py", "py/svc/src/svc/db.py"},
	} {
		if !edges[want] {
			t.Errorf("missing edge %v\nhave %v", want, g.Edges)
		}
	}
}

func TestFullScanRecordsResolverVersion(t *testing.T) {
	st, _ := graphFixture(t)
	if v, err := st.IndexResolverVersion(); err != nil || v != ResolverVersion {
		t.Fatalf("resolver version = %d, %v; want %d", v, err, ResolverVersion)
	}
}

// TestBuildGraphMarksTestCode: an import only test files make is a test
// edge, even between packages whose other files are code, and a node of test
// files only is a test node. The viewer hides both by default.
func TestBuildGraphMarksTestCode(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	for _, d := range []string{"store", "util", "testkit", "web"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGo(t, root, "go.mod", "module example.com/m\n\ngo 1.23\n")
	writeGo(t, root, "store/store.go", "package store\n\nimport \"example.com/m/util\"\n\nfunc Get() int { return util.One() }\n")
	writeGo(t, root, "store/store_test.go", "package store\n\nimport (\n\t\"example.com/m/testkit\"\n\t\"example.com/m/util\"\n)\n\nfunc TestGet() { testkit.Use(); util.One() }\n")
	writeGo(t, root, "util/util.go", "package util\n\nfunc One() int { return 1 }\n")
	writeGo(t, root, "testkit/kit.go", "package testkit\n\nfunc Use() {}\n")
	writeGo(t, root, "web/b.ts", "export const b = 1;\n")
	writeGo(t, root, "web/b.test.ts", "import { b } from './b';\nexport const t = b;\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"go", "typescript"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, false, 0); err != nil {
		t.Fatal(err)
	}
	g, err := BuildGraph(st, root, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	test := map[[2]string]bool{}
	for _, e := range g.Edges {
		test[[2]string{e.From, e.To}] = e.Test
	}
	for edge, want := range map[[2]string]bool{
		{"store/", "testkit/kit.go"}:  true,  // only store_test.go imports it
		{"store/", "util/util.go"}:    false, // store.go imports it too
		{"web/b.test.ts", "web/b.ts"}: true,
	} {
		got, ok := test[edge]
		if !ok || got != want {
			t.Errorf("edge %v: present=%v test=%v, want test=%v", edge, ok, got, want)
		}
	}
	for _, n := range g.Nodes {
		want := n.ID == "web/b.test.ts"
		if n.Test != want {
			t.Errorf("node %s test=%v, want %v", n.ID, n.Test, want)
		}
	}
}

func TestIsTestFile(t *testing.T) {
	for p, want := range map[string]bool{
		"store/store_test.go": true, "store/store.go": false,
		"src/a.test.ts": true, "src/a.spec.tsx": true, "src/__tests__/a.ts": true, "src/attest.ts": false,
		"tests/test_api.py": true, "app/test_api.py": true, "app/api_test.py": true, "app/conftest.py": true, "app/testing.py": false,
		"src/test/java/a/FooTest.java": true, "src/main/java/a/Foo.java": false, "tests/Feature/UserTest.php": true,
		"app/Http/Controllers/TestController.php": false, "x/testdata/sample.go": true, "spec/models/user_spec.rb": true,
		"src/net/http_test.cc": true, "src/net/http_unittest.cpp": true, "src/test_util.c": true, "src/latest.c": false,
		"src/protest_runner.cpp": false, "src/util_test.h": false,
	} {
		if got := IsTestFile(p); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestBuildGraphLeavesOutUnlinkedConfig: a YAML or Dockerfile with no edges
// is not a node, one that an import links is, and a focus on an unlinked
// one still works.
func TestBuildGraphLeavesOutUnlinkedConfig(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, root)
	writeGo(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeGo(t, root, "Dockerfile", "FROM golang:1.23 AS build\n")
	writeGo(t, root, ".github/workflows/ci.yml", "jobs:\n  release:\n    uses: ./.github/workflows/release.yml\n")
	writeGo(t, root, ".github/workflows/release.yml", "jobs:\n  publish:\n    runs-on: x\n")
	writeGo(t, root, "settings.yml", "name: x\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"auto"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, false, 0); err != nil {
		t.Fatal(err)
	}
	g, err := BuildGraph(st, root, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for id, want := range map[string]bool{
		"main.go": true, ".github/workflows/ci.yml": true, ".github/workflows/release.yml": true,
		"Dockerfile": false, "settings.yml": false,
	} {
		if ids[id] != want {
			t.Errorf("node %s present=%v, want %v (nodes %v)", id, ids[id], want, ids)
		}
	}
	g, err = BuildGraph(st, root, GraphOptions{Focus: "settings.yml", Depth: 1})
	if err != nil {
		t.Fatalf("focus on an unlinked file: %v", err)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].ID != "settings.yml" {
		t.Errorf("focus graph = %+v", g.Nodes)
	}
}

// TestBulkAndPerFileNotesAreIdentical: a full render loads the index in four
// queries and the hook queries per file; both must write the same bytes, or a
// note would change on every commit for no reason. Checked over every pack
// sample, which between them cover every language's symbols, facets and
// edges.
func TestBulkAndPerFileNotesAreIdentical(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("lang", "packs", "*", "testdata", "*"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no samples: %v", err)
	}
	for _, sample := range cases {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(sample)))+"/"+filepath.Base(sample), func(t *testing.T) {
			st, tmp := tempStore(t)
			defer st.Close()
			root := filepath.Join(tmp, "repo")
			if err := copySample(sample, root); err != nil {
				t.Fatal(err)
			}
			initGitRepo(t, root)
			opts := lang.ScanOptions{Root: root, Languages: []string{"auto"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
			if err := FullScan(st, opts, true, 0); err != nil {
				t.Fatal(err)
			}
			files, err := st.AllIndexedFiles()
			if err != nil {
				t.Fatal(err)
			}
			bulk := map[string]string{}
			for _, f := range files {
				p, _, err := lang.IndexNotePath(root, f)
				if err != nil {
					continue
				}
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("note for %s: %v", f, err)
				}
				bulk[f] = string(b)
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			registry := lang.NewRegistryForRoot(root)
			keys := resolveImportPaths(st, root, registry)
			w, err := safefs.NewWriter(githintsDir(root), safefs.StateDirPerm, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			for f, want := range bulk {
				if err := renderFileNote(w, dbNotes{st}, root, f, keys); err != nil {
					t.Fatal(err)
				}
				p, _, _ := lang.IndexNotePath(root, f)
				got, _ := os.ReadFile(p)
				if string(got) != want {
					t.Errorf("%s: per-file note differs from bulk\nbulk:\n%s\nper-file:\n%s", f, want, got)
				}
			}
		})
	}
}
