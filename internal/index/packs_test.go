package index

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

// packCase is the expected.json of a sample project under
// lang/packs/<language>/testdata/<case>/.
//
// Facets are written "facet framework file name [detail]" and edges
// "from -> to", as BuildGraph names nodes. Both are compared as exact sets: a
// framework pack is judged on what it misses and on what it invents, and a
// subset check would only catch the first.
type packCase struct {
	Facets []string `json:"facets"`
	Edges  []string `json:"edges"`
}

// updatePacksEnv rewrites every expected.json from what the scan found. Use it
// to start a new case, then read the diff line by line: it records whatever
// the scan does, wrong answers included.
const updatePacksEnv = "GITHINTS_UPDATE_PACKS"

// TestPackSamples runs a full scan over every sample project in the packs and
// compares the facets and edges it finds with the case's expected.json. This
// is the accuracy check for framework support: adding a framework is a
// detector, a sample project and its expected output.
func TestPackSamples(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("lang", "packs", "*", "testdata", "*", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no pack samples found under lang/packs/*/testdata")
	}
	for _, expectedPath := range cases {
		dir := filepath.Dir(expectedPath)
		// python/fastapi: the language and the case.
		name := filepath.Base(filepath.Dir(filepath.Dir(dir))) + "/" + filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			got := scanPackSample(t, dir)
			if os.Getenv(updatePacksEnv) != "" {
				var buf bytes.Buffer
				enc := json.NewEncoder(&buf)
				// Routes are full of < and >; escaped, the file is unreadable.
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				if err := enc.Encode(got); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(expectedPath, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatal(err)
			}
			var want packCase
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("parse %s: %v", expectedPath, err)
			}
			compareSets(t, "facet", want.Facets, got.Facets)
			compareSets(t, "edge", want.Edges, got.Edges)
		})
	}
}

func scanPackSample(t *testing.T, src string) packCase {
	t.Helper()
	st, tmp := tempStore(t)
	defer st.Close()
	root := filepath.Join(tmp, "repo")
	if err := copySample(src, root); err != nil {
		t.Fatalf("copy sample: %v", err)
	}
	initGitRepo(t, root)

	opts := lang.ScanOptions{Root: root, Languages: []string{lang.AutoLanguages}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	// Empty, not nil, so a case with no edges records [] rather than null.
	got := packCase{Facets: []string{}, Edges: []string{}}
	facets, err := st.Facets(FacetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facets {
		line := fmt.Sprintf("%s %s %s %s", f.Facet, f.Framework, f.FilePath, f.Name)
		if f.Detail != "" {
			line += " " + f.Detail
		}
		got.Facets = append(got.Facets, line)
	}
	g, err := BuildGraph(st, root, GraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range g.Edges {
		got.Edges = append(got.Edges, e.From+" -> "+e.To)
	}
	sort.Strings(got.Facets)
	sort.Strings(got.Edges)
	return got
}

// copySample copies a sample project, leaving out its expected.json.
func copySample(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "expected.json" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func compareSets(t *testing.T, kind string, want, got []string) {
	t.Helper()
	have := make(map[string]bool, len(got))
	for _, g := range got {
		have[g] = true
	}
	expected := make(map[string]bool, len(want))
	for _, w := range want {
		expected[w] = true
		if !have[w] {
			t.Errorf("missing %s: %s", kind, w)
		}
	}
	for _, g := range got {
		if !expected[g] {
			t.Errorf("unexpected %s: %s", kind, g)
		}
	}
}
