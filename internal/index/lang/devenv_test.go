package lang

import (
	"reflect"
	"strings"
	"testing"
)

// TestFileNameClaims: files without a useful extension are selected by
// name, a generated lock file is not taken as YAML, and the registry and a
// scan's FileMap agree.
func TestFileNameClaims(t *testing.T) {
	r := NewRegistry()
	parsers, _, err := r.ResolveKnownLanguages([]string{AutoLanguages})
	if err != nil {
		t.Fatal(err)
	}
	m := ExtensionMap(ParserSet(parsers))
	for file, want := range map[string]string{
		"Dockerfile":               "dockerfile",
		"deploy/Dockerfile.prod":   "dockerfile",
		"worker/api.Dockerfile":    "dockerfile",
		"Containerfile":            "dockerfile",
		"Makefile":                 "makefile",
		"scripts/release.mk":       "makefile",
		".github/workflows/ci.yml": "yaml",
		"pnpm-lock.yaml":           "",
		"web/yarn-lock.yaml":       "",
		"infra/main.tf":            "terraform",
		"README":                   "",
		"main.go":                  "go",
	} {
		got := ""
		if p := r.ForPath(file); p != nil {
			got = p.Language()
		}
		viaMap := ""
		if p := SelectParser(m, file); p != nil {
			viaMap = p.Language()
		}
		if got != want || viaMap != want {
			t.Errorf("%s: registry %q, FileMap %q; want %q", file, got, viaMap, want)
		}
	}
}

func TestFilenameValidation(t *testing.T) {
	base := Spec{Language: "x", DepthStyle: DepthNone, Symbols: []SpecRule{{Kind: KindFunc, Pattern: `(?P<name>x)`}}}
	cases := map[string]struct {
		mutate func(*Spec)
		want   string
	}{
		"nothing claimed": {func(s *Spec) {}, "claims no extensions or filenames"},
		"path in name":    {func(s *Spec) { s.Filenames = []string{"a/Makefile"} }, "base name"},
		"bad glob":        {func(s *Spec) { s.Filenames = []string{"Docker[file"} }, "pattern"},
		"bad graph":       {func(s *Spec) { s.Filenames = []string{"X"}; s.Graph = "never" }, "graph"},
	}
	for label, c := range cases {
		s := base
		c.mutate(&s)
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", label, err, c.want)
		}
	}
	s := base
	s.Filenames = []string{"Justfile", "*.just"}
	if err := s.Validate(); err != nil {
		t.Errorf("a filenames-only spec was rejected: %v", err)
	}
}

// TestInsideChildren: the keys directly under services:, not the keys of a
// service, and nothing under another top-level key.
func TestInsideChildren(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"yaml"}, PathGlobs: []string{"*.yml"},
		Rules: []DetectorRule{{Facet: FacetInfra, Requires: ":", Inside: `^services:\s*$`, InsideChildren: true,
			Pattern: `^\s+(?P<name>[\w.-]+):\s*$`, Detail: "service"}},
	})
	src := "services:\n" +
		"  web:\n" + // 2
		"    ports:\n" +
		"      - 80\n" +
		"\n" +
		"  db:\n" + // 6
		"    image: pg\n" +
		"volumes:\n" +
		"  data:\n"
	wantSummary(t, set.Detect(engineInput(t, "yaml", "compose.yml", src)), "infra web service @2", "infra db service @6")
}

func TestTerraformDirectoryModulesAndQuotedSymbols(t *testing.T) {
	src := "module \"net\" {\n  source = \"./modules/net\"\n}\n\nmodule \"eks\" {\n  source = \"terraform-aws-modules/eks/aws\"\n}\n\nresource \"aws_s3_bucket\" \"logs\" {}\n"
	symbols, imports := parseWith(t, "terraform", "infra/main.tf", src)
	if got, want := importPaths(imports), []string{"infra/modules/net", "terraform-aws-modules/eks/aws"}; !reflect.DeepEqual(got, want) {
		t.Errorf("imports = %q, want %q", got, want)
	}
	got := kindsByName(symbols)
	for name, kind := range map[string]SymbolKind{"net": KindType, "eks": KindType, "logs": KindType} {
		if got[name] != kind {
			t.Errorf("%s: kind %q, want %q", name, got[name], kind)
		}
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"infra/modules/net/main.tf": "", "main.tf": ""})
	if key, err := NewRegistry().ImportPath(root, "infra/modules/net/main.tf"); err != nil || key != "infra/modules/net" {
		t.Errorf("module key = %q, %v", key, err)
	}
	if _, err := NewRegistry().ImportPath(root, "main.tf"); err == nil {
		t.Error("the root module got a key")
	}
}
