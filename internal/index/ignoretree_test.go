package index

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
)

// keptFiles walks root the way a full scan does and returns the files left
// after pruning ignored directories and dropping ignored files.
func keptFiles(t *testing.T, root string, dirIgnored, fileIgnored func(string) bool) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == ".git" || rel == ".githints" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel != "." && dirIgnored(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !fileIgnored(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestIgnoreListingMatchesPerPathChecks pins the one-call listing a full
// scan uses against the per-path check it replaced, on a tree built to
// disagree if either is wrong: nested .gitignore files, a negation, an
// ignored directory holding a force-added tracked file, an ignored tracked
// file, .githintsignore on top, and awkward names.
func TestIgnoreListingMatchesPerPathChecks(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	files := map[string]string{
		".gitignore":                   "node_modules/\n*.log\nbuild/*\n!build/keep/\nsecret.txt\n",
		".githintsignore":              "generated/\nvendor\nfixtures/*.json\n",
		"src/app/main.go":              "package main\n",
		"src/app/debug.log":            "x\n",
		"src/app/.gitignore":           "local/\n!keep.log\n",
		"src/app/keep.log":             "x\n",
		"src/app/local/x.go":           "package local\n",
		"src/app/node_modules/p/i.js":  "x\n",
		"node_modules/a/b/c.js":        "x\n",
		"build/keep/k.go":              "package keep\n",
		"build/drop/d.go":              "package drop\n",
		"generated/api/z.go":           "package api\n",
		"vendor/v.go":                  "package v\n",
		"fixtures/a.json":              "{}\n",
		"fixtures/a.go":                "package fixtures\n",
		"docs/with space/é ü.go":       "package docs\n",
		"dist-tracked/forced/f.go":     "package forced\n",
		"secret.txt":                   "x\n",
		"deep/a/b/c/d/e/f/g/h/i.go":    "package i\n",
		"deep/a/b/c/d/e/f/g/h/i.log":   "x\n",
		"deep/a/b/c/.gitignore":        "d/e/\n",
		"deep/a/b/c/d/keep.go":         "package d\n",
		"deep/a/b/c/d/e/inner/skip.go": "package skip\n",
	}
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A tracked file inside a directory the rules ignore, and a tracked file
	// the rules ignore: check-ignore --no-index ignores both.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(files[".gitignore"]+"dist-tracked/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-f", "dist-tracked/forced/f.go", "secret.txt"},
		{"add", "src/app/main.go"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	tree, err := listIgnored(root)
	if err != nil {
		t.Fatal(err)
	}
	got := keptFiles(t, root, func(r string) bool { return tree.dirs[r] }, func(r string) bool { return tree.files[r] })
	want := keptFiles(t, root, func(r string) bool { return shouldIgnoreFile(root, r) }, func(r string) bool { return shouldIgnoreFile(root, r) })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listing kept %q\nper-path kept %q", got, want)
	}
	for _, f := range []string{"src/app/main.go", "src/app/keep.log", "build/keep/k.go", "fixtures/a.go", "docs/with space/é ü.go", "deep/a/b/c/d/keep.go"} {
		if !slices.Contains(got, f) {
			t.Errorf("%s should be kept", f)
		}
	}
	for _, f := range []string{"src/app/debug.log", "node_modules/a/b/c.js", "build/drop/d.go", "generated/api/z.go", "vendor/v.go",
		"fixtures/a.json", "dist-tracked/forced/f.go", "secret.txt", "deep/a/b/c/d/e/inner/skip.go", "deep/a/b/c/d/e/f/g/h/i.go"} {
		if slices.Contains(got, f) {
			t.Errorf("%s should be ignored", f)
		}
	}

	// And the fallback answers the same.
	fallback := ignoreTree{dirs: ignoredDirs(root)}
	var rels []string
	keptFiles(t, root, func(r string) bool { return fallback.dirs[r] }, func(r string) bool { rels = append(rels, r); return false })
	resolved := resolveIgnored(root, rels)
	if fb := keptFiles(t, root, func(r string) bool { return fallback.dirs[r] }, func(r string) bool { return resolved[r] }); !reflect.DeepEqual(fb, want) {
		t.Fatalf("per-depth fallback kept %q\nper-path kept %q", fb, want)
	}
}
