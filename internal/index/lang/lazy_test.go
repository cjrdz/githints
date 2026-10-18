package lang

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// TestDetectLoadsInputsOnlyWhenNeeded: a file no detector applies to is
// never blanked; dependencies are read only when a detector gets as far as
// its dependency gate; and a detector for another language is never
// consulted at all.
func TestDetectLoadsInputsOnlyWhenNeeded(t *testing.T) {
	set := mustSet(t,
		Detector{Framework: "imp", Languages: []string{"go"}, WhenImports: []string{"x"},
			Rules: []DetectorRule{{Facet: FacetRoute, Pattern: `(?P<name>never)`}}},
		Detector{Framework: "dep", Languages: []string{"go"}, WhenDependencies: []string{"y"},
			Rules: []DetectorRule{{Facet: FacetRoute, Requires: "r", Pattern: `r\("(?P<name>[^"]*)"`, KeepStrings: true}}},
		Detector{Framework: "ts", Languages: []string{"typescript"}, PathGlobs: []string{"*"},
			Rules: []DetectorRule{{Facet: FacetRoute, Pattern: `(?P<name>x)`}}},
	)
	views, deps := 0, 0
	in := func(imports []string, declared []string) DetectInput {
		di := DetectInput{FilePath: "a.go", Language: "go",
			LoadDependencies: func() []string { deps++; return declared },
			LoadViews: func() ([]string, []string) {
				views++
				return []string{`r("/a")`}, []string{`r("/a")`}
			}}
		for _, i := range imports {
			di.Imports = append(di.Imports, Import{ImportedPath: i})
		}
		return di
	}

	// Nothing applies: the dependency gate is reached (and read once), the
	// views never are.
	if got := set.Detect(in(nil, nil)); len(got) != 0 {
		t.Fatalf("detected %v", summary(got))
	}
	if views != 0 || deps != 1 {
		t.Fatalf("views loaded %d times, dependencies %d; want 0 and 1", views, deps)
	}

	// A dependency gate opens: views load once, dependencies were read once.
	views, deps = 0, 0
	if got := set.Detect(in(nil, []string{"y"})); len(got) != 1 {
		t.Fatalf("detected %v, want one route", summary(got))
	}
	if views != 1 || deps != 1 {
		t.Fatalf("views loaded %d times, dependencies %d; want 1 and 1", views, deps)
	}

	// The import gate opens first: the "imp" detector applies, "dep" still
	// needs its dependencies checked, and a TypeScript detector is never
	// considered for a Go file even though its path glob matches anything.
	views, deps = 0, 0
	set.Detect(in([]string{"x"}, nil))
	if views != 1 || deps != 1 {
		t.Fatalf("views loaded %d times, dependencies %d; want 1 and 1", views, deps)
	}
	if got := set.forLanguage("go"); len(got) != 2 {
		t.Fatalf("go detectors = %v, want the two go detectors", got)
	}
	if got := set.forLanguage("ruby"); len(got) != 0 {
		t.Fatalf("ruby detectors = %v, want none", got)
	}
}

// TestManifestsConcurrentLookups: workers asking about overlapping directories
// at once get exactly the answers a sequential lookup gives (run under
// -race; each directory is computed under its own sync.Once).
func TestManifestsConcurrentLookups(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"package.json": `{"devDependencies":{"typescript":"5"}}`}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("apps/a%d/package.json", i)] = fmt.Sprintf(`{"dependencies":{"dep%d":"1"}}`, i)
		files[fmt.Sprintf("apps/a%d/src/x/y.ts", i)] = ""
	}
	writeTree(t, root, files)

	want := map[string][]string{}
	seq := NewManifests(root)
	for i := 0; i < 8; i++ {
		f := fmt.Sprintf("apps/a%d/src/x/y.ts", i)
		want[f] = seq.DependenciesFor(f)
	}
	_ = seq.Close()

	m := NewManifests(root)
	defer m.Close()
	var wg sync.WaitGroup
	for round := 0; round < 20; round++ {
		for f, w := range want {
			wg.Add(1)
			go func(f string, w []string) {
				defer wg.Done()
				if got := m.DependenciesFor(f); !reflect.DeepEqual(got, w) {
					t.Errorf("%s: %v, want %v", f, got, w)
				}
			}(f, w)
		}
	}
	wg.Wait()
}

// TestWalkedManifestsMatchListing: the dependencies a full scan gets from the
// manifests its walk recorded equal those a directory listing finds, for
// every directory, including one with no manifest at all.
func TestWalkedManifestsMatchListing(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"package.json":             `{"devDependencies":{"typescript":"5"}}`,
		"api/requirements-dev.txt": "pytest\n",
		"api/pyproject.toml":       "[project]\ndependencies = [\"fastapi\"]\n",
		"web/package.json":         `{"dependencies":{"next":"15"}}`,
		"svc/Api.csproj":           `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"web/app/page.tsx":         "",
		"api/app/main.py":          "",
		"svc/Program.cs":           "",
		"docs/notes.md":            "",
	})
	listing := NewManifests(root)
	defer listing.Close()
	walked := NewManifests(root)
	for _, rel := range []string{"package.json", "api/requirements-dev.txt", "api/pyproject.toml", "web/package.json", "svc/Api.csproj"} {
		walked.Seen(rel)
	}
	walked.MarkWalked()
	defer walked.Close()
	for _, f := range []string{"web/app/page.tsx", "api/app/main.py", "svc/Program.cs", "docs/notes.md", "top.go"} {
		if got, want := walked.DependenciesFor(f), listing.DependenciesFor(f); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: walked %v, listed %v", f, got, want)
		}
	}
}
