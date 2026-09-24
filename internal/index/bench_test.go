package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

// The index had no benchmarks, so every optimization was argued from reading.
// These exist to make the suspected costs separately attributable:
//
//	BenchmarkFullScan         walk + ignore checks + parse + write + render
//	BenchmarkRenderNotes      render alone, against a populated store
//	BenchmarkIncrementalScan  the post-commit hook path, which users feel
//	BenchmarkInsertSymbols    the store write path on its own
//
// Run with:
//
//	go test -bench=. -benchmem ./internal/index/
//
// The 10000-file cases are skipped under -short so `go test ./...` stays fast.

type benchRepoOptions struct {
	goFiles        int
	tsFiles        int
	symbolsPerFile int
	// assetFiles are files no parser claims. They exist because the walk pays
	// its ignore check per path before asking whether any parser wants the
	// file, so a realistic asset ratio is part of what is being measured.
	assetFiles int
}

// benchRepo writes a synthetic repository and returns its root. It writes the
// minimal .git fixture the correctness tests use rather than shelling out to
// `git init`: check-ignore only needs a discoverable repository, and one fewer
// subprocess per setup keeps the timing honest.
func benchRepo(tb testing.TB, opts benchRepoOptions) string {
	tb.Helper()

	root := filepath.Join(tb.TempDir(), "repo")
	write := func(rel, content string) {
		tb.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			tb.Fatalf("write %s: %v", rel, err)
		}
	}

	write("go.mod", "module example.com/bench\n\ngo 1.26\n")
	write(".git/HEAD", "ref: refs/heads/main\n")
	write(".git/config", "[core]\n\trepositoryformatversion = 0\n")
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(root, ".git", d), 0o755); err != nil {
			tb.Fatalf("mkdir .git/%s: %v", d, err)
		}
	}

	// Spread files across directories so directory pruning and note-tree
	// creation see a realistic shape rather than one flat directory.
	const perDir = 20

	for i := range opts.goFiles {
		write(fmt.Sprintf("pkg/p%d/file%d.go", i/perDir, i), benchGoSource(i, opts.symbolsPerFile))
	}
	for i := range opts.tsFiles {
		write(fmt.Sprintf("src/m%d/mod%d.ts", i/perDir, i), benchTSSource(i, opts.symbolsPerFile))
	}
	for i := range opts.assetFiles {
		write(fmt.Sprintf("assets/a%d/asset%d.png", i/perDir, i), "not-really-a-png-"+strconv.Itoa(i))
	}
	return root
}

// benchGoSource emits a parseable Go file with n exported symbols and an
// intra-repo import, so import resolution and the "Imported by" render path
// both have real edges to follow.
func benchGoSource(i, n int) string {
	id := strconv.Itoa(i)
	var b []byte
	b = append(b, "package p"+strconv.Itoa(i/20)+"\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\n"...)
	b = append(b, "// Config"+id+" is a type.\ntype Config"+id+" struct {\n\tName string\n\tSize int\n}\n\n"...)
	for j := range n {
		sid := id + "_" + strconv.Itoa(j)
		b = append(b, "// Func"+sid+" does work.\nfunc Func"+sid+"(s string) (string, error) {\n\treturn fmt.Sprintf(\"%s\", strings.ToUpper(s)), nil\n}\n\n"...)
	}
	b = append(b, "const Version"+id+" = \"1.0\"\n\nvar Default"+id+" = Config"+id+"{}\n"...)
	return string(b)
}

// benchTSSource emits a parseable TypeScript module. The TS parser re-scans
// forward per symbol to find a declaration's end, so symbol density is the
// interesting dimension here.
func benchTSSource(i, n int) string {
	id := strconv.Itoa(i)
	var b []byte
	b = append(b, "import { helper } from \"./mod"+id+"_dep\";\nimport type { Thing } from \"@core/things\";\n\n"...)
	b = append(b, "export interface Shape"+id+" {\n  name: string;\n  size: number;\n}\n\n"...)
	for j := range n {
		sid := id + "_" + strconv.Itoa(j)
		b = append(b, "export function fn"+sid+"(s: string): string {\n  const t = `${s} literal with } brace`;\n  return helper(t);\n}\n\n"...)
	}
	b = append(b, "export class Klass"+id+" {\n  run(): void {}\n}\n\nexport const NAME"+id+" = \"x\";\n"...)
	return string(b)
}

// benchScanOptions mirrors what cmdIndex builds from config defaults, so the
// benchmark measures the real configuration.
func benchScanOptions(root string) lang.ScanOptions {
	return lang.ScanOptions{
		Root:         root,
		Languages:    []string{"go", "typescript"},
		MaxFileSize:  1 << 20,
		ParseTimeout: 5 * time.Second,
	}
}

// benchStore opens a fresh index.db outside the scanned tree, so the store
// file is never a candidate for the walk.
func benchStore(tb testing.TB) *Store {
	tb.Helper()
	st, err := Open(filepath.Join(tb.TempDir(), "index.db"))
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	return st
}

var benchSizes = []struct {
	name  string
	opts  benchRepoOptions
	large bool
}{
	{
		name: "100files",
		opts: benchRepoOptions{goFiles: 60, tsFiles: 40, symbolsPerFile: 8, assetFiles: 40},
	},
	{
		name: "1000files",
		opts: benchRepoOptions{goFiles: 600, tsFiles: 400, symbolsPerFile: 8, assetFiles: 400},
	},
	{
		name:  "10000files",
		opts:  benchRepoOptions{goFiles: 6000, tsFiles: 4000, symbolsPerFile: 8, assetFiles: 4000},
		large: true,
	},
}

func skipLarge(b *testing.B, large bool) {
	if large && testing.Short() {
		b.Skip("skipping large corpus in -short mode")
	}
}

// BenchmarkFullScan measures a whole rebuild.
func BenchmarkFullScan(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(size.name, func(b *testing.B) {
			skipLarge(b, size.large)
			root := benchRepo(b, size.opts)
			opts := benchScanOptions(root)

			for b.Loop() {
				st := benchStore(b)
				if err := FullScan(st, opts, true, 0); err != nil {
					b.Fatalf("FullScan: %v", err)
				}
			}
		})
	}
}

// BenchmarkRenderNotes isolates rendering from parsing, so the two can be
// compared against BenchmarkFullScan rather than guessed at.
func BenchmarkRenderNotes(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(size.name, func(b *testing.B) {
			skipLarge(b, size.large)
			root := benchRepo(b, size.opts)
			st := benchStore(b)
			if err := FullScan(st, benchScanOptions(root), true, 0); err != nil {
				b.Fatalf("FullScan: %v", err)
			}

			for b.Loop() {
				if err := RenderNotes(st, root, false); err != nil {
					b.Fatalf("RenderNotes: %v", err)
				}
			}
		})
	}
}

// BenchmarkIncrementalScan measures the post-commit hook path: a small set of
// changed files against an already-populated index. This is the benchmark that
// matters most, because it runs on every commit.
func BenchmarkIncrementalScan(b *testing.B) {
	const changed = 20

	for _, size := range benchSizes {
		b.Run(size.name, func(b *testing.B) {
			skipLarge(b, size.large)
			root := benchRepo(b, size.opts)
			st := benchStore(b)
			if err := FullScan(st, benchScanOptions(root), true, 0); err != nil {
				b.Fatalf("FullScan: %v", err)
			}

			opts := benchScanOptions(root)
			paths := make([]string, 0, changed)
			for i := range changed {
				paths = append(paths, fmt.Sprintf("pkg/p%d/file%d.go", i/20, i))
			}

			for b.Loop() {
				if err := IncrementalScan(st, opts, paths, 0); err != nil {
					b.Fatalf("IncrementalScan: %v", err)
				}
			}
		})
	}
}

// BenchmarkInsertSymbols isolates the store write path. Neither InsertSymbols
// nor InsertImports opens a transaction, so each row is its own autocommit and
// WAL fsync; this is the baseline a transaction change should move.
func BenchmarkInsertSymbols(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(n)+"rows", func(b *testing.B) {
			syms := make([]lang.Symbol, n)
			for i := range syms {
				syms[i] = lang.Symbol{
					Name:      "Sym" + strconv.Itoa(i),
					Kind:      lang.KindFunc,
					FilePath:  fmt.Sprintf("pkg/p%d/file%d.go", i/20, i),
					LineStart: i,
					LineEnd:   i + 3,
					Signature: "func Sym" + strconv.Itoa(i) + "(s string) error",
					Language:  "go",
				}
			}

			for b.Loop() {
				st := benchStore(b)
				if err := st.InsertSymbols(syms); err != nil {
					b.Fatalf("InsertSymbols: %v", err)
				}
			}
		})
	}
}
