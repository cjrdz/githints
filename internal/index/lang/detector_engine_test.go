package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// engineInput blanks src with the real parser for language, as a scan would.
func engineInput(t *testing.T, language, file, src string) DetectInput {
	t.Helper()
	p := NewRegistry().ForLanguage(language)
	bl, ok := p.(interface {
		BlankLines([]byte) []string
		BlankLinesKeepingStrings([]byte) []string
	})
	if !ok {
		t.Fatalf("%s exposes no blanked views", language)
	}
	return DetectInput{
		FilePath: file,
		Language: language,
		Code:     bl.BlankLines([]byte(src)),
		Strings:  bl.BlankLinesKeepingStrings([]byte(src)),
	}
}

func mustSet(t *testing.T, ds ...Detector) *DetectorSet {
	t.Helper()
	set, err := NewDetectorSet(ds)
	if err != nil {
		t.Fatalf("NewDetectorSet: %v", err)
	}
	return set
}

// summary renders detections as "facet name detail @line", sorted.
func summary(found []Detected) []string {
	var out []string
	for _, f := range found {
		s := f.Facet + " " + f.Name
		if f.Detail != "" {
			s += " " + f.Detail
		}
		out = append(out, s+" @"+strconv.Itoa(f.Line))
	}
	sort.Strings(out)
	return out
}

func wantSummary(t *testing.T, got []Detected, want ...string) {
	t.Helper()
	s := summary(got)
	sort.Strings(want)
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got  %q\nwant %q", s, want)
	}
}

// TestMultiLineStatementsMatchAtTheirLine: a decorator split over lines is
// found, reported at the line it starts on, and every match inside a
// multi-line statement keeps its own line.
func TestMultiLineStatementsMatchAtTheirLine(t *testing.T) {
	set := mustSet(t, Detector{
		Framework:   "t",
		Languages:   []string{"python"},
		WhenImports: []string{"t"},
		Rules: []DetectorRule{
			{Facet: FacetRoute, Requires: "@", KeepStrings: true,
				Pattern: `@\w+\.(?P<detail>post)\(\s*"(?P<name>[^"]*)"`},
			{Facet: FacetModel, Requires: "&", Pattern: `&(?P<name>\w+)`},
		},
	})
	src := "" +
		"@app.post(\n" + // 1
		"    \"/items\",\n" +
		"    status_code=201,\n" +
		")\n" +
		"def create(): ...\n" + // 5
		"register(&User,\n" + // 6
		"         &Post)\n" // 7
	in := engineInput(t, "python", "a.py", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /items POST @1", "model User @6", "model Post @7")
}

// TestLineAnchoredRulesStillMatchInsideAStatement guards the reason statements
// are joined with newlines and compiled in multi-line mode: Vue's
// `^\s*name:` sits inside defineComponent({...}).
func TestLineAnchoredRulesStillMatchInsideAStatement(t *testing.T) {
	in := engineInput(t, "typescript", "c.ts", "export default defineComponent({\n  name: 'UserCard',\n})\n")
	in.Imports = []Import{{ImportedPath: "vue"}}
	set, _ := EmbeddedDetectors()
	if got := named(set.Detect(in), "vue", FacetComponent); !reflect.DeepEqual(got, []string{"UserCard"}) {
		t.Fatalf("vue components = %v, want [UserCard]", got)
	}
}

// TestGateIsCheckedOnTheMatchLine: inside a joined statement, a real call on
// one line must not license a route-shaped string on another.
func TestGateIsCheckedOnTheMatchLine(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"python"}, WhenImports: []string{"t"},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "route(", KeepStrings: true,
			Pattern: `route\(\s*"(?P<name>[^"]*)"`}},
	})
	src := "wrap(\n    route(\"/real\"),\n    doc(\"route('/fake')\"),\n)\n"
	in := engineInput(t, "python", "a.py", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /real @2")
}

// TestPathRules covers file-system conventions: a route named from its path
// with route groups and slots dropped, one facet per handler export, and a
// whole file as a component.
func TestPathRules(t *testing.T) {
	set := mustSet(t, Detector{
		Framework:        "next",
		Languages:        []string{"typescript"},
		WhenDependencies: []string{"next"},
		Rules: []DetectorRule{
			{Facet: FacetRoute, NameStyle: "route",
				PathPattern: `(?:^|/)app(?P<name>(?:/[^/]+)*)/page\.tsx$`},
			{Facet: FacetRoute, NameStyle: "route",
				PathPattern: `(?:^|/)app(?P<name>(?:/[^/]+)*)/route\.ts$`,
				Pattern:     `^export\s+async\s+function\s+(?P<detail>GET|POST)\b`},
		},
	})
	deps := []string{"next", "react"}

	page := engineInput(t, "typescript", "apps/web/app/(shop)/@modal/users/[id]/page.tsx", "export default function Page() {}\n")
	page.Dependencies = deps
	wantSummary(t, set.Detect(page), "route /users/[id] @1")

	root := engineInput(t, "typescript", "app/page.tsx", "export default function Home() {}\n")
	root.Dependencies = deps
	wantSummary(t, set.Detect(root), "route / @1")

	handler := engineInput(t, "typescript", "app/api/users/route.ts",
		"import x from 'y'\n\nexport async function GET() {}\nexport async function POST() {}\n")
	handler.Dependencies = deps
	wantSummary(t, set.Detect(handler), "route /api/users GET @3", "route /api/users POST @4")

	// The same file outside a project that depends on next is not Next.js.
	handler.Dependencies = []string{"react"}
	if got := set.Detect(handler); len(got) != 0 {
		t.Fatalf("detected without the dependency gate: %v", summary(got))
	}
}

func TestPrefixes(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"typescript"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{
			{Requires: "Router", KeepStrings: true, Pattern: `(?P<var>\w+)\s*=\s*Router\(\s*'(?P<prefix>[^']*)'`},
			{Requires: "@Controller", KeepStrings: true, Pattern: `@Controller\(\s*'(?P<prefix>[^']*)'`},
		},
		Rules: []DetectorRule{
			{Facet: FacetRoute, Requires: ".get(", KeepStrings: true, Pattern: `(?P<var>\w+)\.get\(\s*'(?P<name>[^']*)'`},
			{Facet: FacetRoute, Requires: "@Get", KeepStrings: true, Pattern: `@Get\(\s*(?:'(?P<name>[^']*)')?\s*\)`},
		},
	})
	// Nest's bare @Get() matches no name: it is the controller's own path.
	src := "" +
		"const api = Router('/api/')\n" + // 1
		"api.get('/users', h)\n" + // 2
		"other.get('/plain', h)\n" + // 3
		"@Controller('cats')\n" + // 4
		"class Cats {\n" +
		"  @Get(':id')\n" + // 6
		"  one() {}\n" +
		"  @Get()\n" + // 8
		"  all() {}\n" +
		"}\n"
	in := engineInput(t, "typescript", "a.ts", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in),
		"route /api/users @2", "route /plain @3", "route /cats/:id @6", "route /cats @8")
}

func TestJoinRouteAndRouteFromPath(t *testing.T) {
	for _, c := range [][3]string{
		{"/items", "/{id}", "/items/{id}"},
		{"items", "{id}", "/items/{id}"},
		{"/items/", "/", "/items"},
		{"", "", "/"},
		{"", "x", "/x"},
	} {
		if got := joinRoute(c[0], c[1]); got != c[2] {
			t.Errorf("joinRoute(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	for in, want := range map[string]string{
		"":                      "/",
		"/(marketing)/about":    "/about",
		"/@team/settings":       "/settings",
		"/blog/index":           "/blog",
		"/users/[id]/[...slug]": "/users/[id]/[...slug]",
		"pages/../x":            "/pages/../x",
	} {
		if got := routeFromPath(in); got != want {
			t.Errorf("routeFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchesAnyGlob(t *testing.T) {
	cases := []struct {
		path, glob string
		want       bool
	}{
		{"schema.prisma", "*.prisma", true},
		{"db/schema.prisma", "*.prisma", true},
		{"app/users/page.tsx", "app/**/page.tsx", true},
		{"app/page.tsx", "app/**/page.tsx", true},
		{"apps/web/app/x/page.tsx", "app/**/page.tsx", true},
		{"apps/web/myapp/x/page.tsx", "app/**/page.tsx", false},
		{"pages/a.vue", "pages/**/*.{vue,tsx}", true},
		{"pages/a.ts", "pages/**/*.{vue,tsx}", false},
		{"database/migrations/2024_x.php", "**/migrations/*.php", true},
		{"database/migrations/sub/x.php", "migrations/*.php", false},
	}
	for _, c := range cases {
		if got := matchesAnyGlob(c.path, []string{c.glob}); got != c.want {
			t.Errorf("matchesAnyGlob(%q, %q) = %v, want %v", c.path, c.glob, got, c.want)
		}
	}
}

func TestDetectorValidation(t *testing.T) {
	ok := DetectorRule{Facet: FacetRoute, Pattern: `(?P<name>x)`}
	cases := map[string]struct {
		d    Detector
		want string
	}{
		"no gate": {Detector{Framework: "a", Rules: []DetectorRule{ok}}, "when_dependencies"},
		"new facet": {Detector{Framework: "a", PathGlobs: []string{"*"},
			Rules: []DetectorRule{{Facet: "widget", Pattern: `(?P<name>x)`}}}, "controller"},
		"prefix without group": {Detector{Framework: "a", PathGlobs: []string{"*"}, Rules: []DetectorRule{ok},
			Prefixes: []PrefixRule{{Pattern: `x`}}}, "prefix"},
		"prefix keep_strings without requires": {Detector{Framework: "a", PathGlobs: []string{"*"}, Rules: []DetectorRule{ok},
			Prefixes: []PrefixRule{{Pattern: `(?P<prefix>x)`, KeepStrings: true}}}, "requires"},
		"nothing names it": {Detector{Framework: "a", PathGlobs: []string{"*"},
			Rules: []DetectorRule{{Facet: FacetRoute, Pattern: `x`, PathPattern: `y`}}}, "name"},
		"bad name_style": {Detector{Framework: "a", PathGlobs: []string{"*"},
			Rules: []DetectorRule{{Facet: FacetRoute, Pattern: `(?P<name>x)`, NameStyle: "camel"}}}, "name_style"},
		"no pattern at all": {Detector{Framework: "a", PathGlobs: []string{"*"},
			Rules: []DetectorRule{{Facet: FacetRoute}}}, "path_pattern"},
	}
	for name, c := range cases {
		err := c.d.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
	}
	for _, f := range []string{FacetController, FacetMiddleware, FacetService, FacetSchema} {
		d := Detector{Framework: "a", PathGlobs: []string{"*"}, Rules: []DetectorRule{{Facet: f, Pattern: `(?P<name>x)`}}}
		if err := d.Validate(); err != nil {
			t.Errorf("facet %s rejected: %v", f, err)
		}
	}
}

func TestParseManifests(t *testing.T) {
	cases := map[string]struct {
		name, data string
		want       []string
	}{
		"package.json": {"package.json",
			`{"name":"x","dependencies":{"next":"15"},"devDependencies":{"@nestjs/core":"1"},"peerDependencies":{"react":"*"}}`,
			[]string{"@nestjs/core", "next", "react"}},
		"composer.json": {"composer.json",
			`{"require":{"php":"^8.2","laravel/framework":"^11"},"require-dev":{"phpunit/phpunit":"^10"}}`,
			[]string{"laravel/framework", "php", "phpunit/phpunit"}},
		"pyproject": {"pyproject.toml", `
[project]
name = "x"  # not a dependency
dependencies = [
    "FastAPI[standard]>=0.115",  # the ] in an extra must not end the list
    'SQLAlchemy[asyncio] ; python_version > "3.8"',
]
description = "not = a dep"

[project.optional-dependencies]
dev = ["pytest>=8", "ruff"]

[dependency-groups]
lint = ["mypy"]

[tool.poetry.dependencies]
python = "^3.12"
django_ninja = "^1"

[tool.ruff]
line-length = 100
`, []string{"django-ninja", "fastapi", "mypy", "pytest", "ruff", "sqlalchemy"}},
		"requirements": {"requirements-dev.txt", "# c\n-r base.txt\nFlask==3.0\nflask_sqlalchemy>=3 ; extra\n\n-e .\n",
			[]string{"flask", "flask-sqlalchemy"}},
		"go.mod": {"go.mod", "module m\n\nrequire github.com/go-chi/chi/v5 v5.0.0\n\nrequire (\n\tgorm.io/gorm v1.25.0 // indirect\n)\n",
			[]string{"github.com/go-chi/chi/v5", "gorm.io/gorm"}},
		"Cargo.toml": {"Cargo.toml", "[package]\nname = \"x\"\n\n[dependencies]\ntokio = { version = \"1\" }\naxum = \"0.7\"\n\n[dev-dependencies]\ninsta = \"1\"\n",
			[]string{"axum", "insta", "tokio"}},
		"vcpkg.json": {"vcpkg.json", `{"name":"x","dependencies":["crow",{"name":"fmt","features":["x"]}, 3]}`,
			[]string{"crow", "fmt"}},
		"conanfile.txt": {"conanfile.txt", "[requires]\nfmt/10.2.1\n# zlib/1.3\n\n[generators]\nCMakeDeps\n\n[tool_requires]\ncmake/3.27.0\n",
			[]string{"cmake", "fmt"}},
		"conanfile.py": {"conanfile.py", "class App(ConanFile):\n    requires = \"fmt/10.2.1\", \"zlib/1.3@user/stable\"\n    name = \"not/a-dep\"\n    def requirements(self):\n        self.requires(\"drogon/1.9.1\")\n",
			[]string{"drogon", "fmt", "zlib"}},
		"CMakeLists.txt": {"CMakeLists.txt", "find_package(Qt6 REQUIRED COMPONENTS Widgets)\nFetchContent_Declare(\n  Crow\n  GIT_REPOSITORY x)\nCPMAddPackage(NAME fmt GIT_TAG 10.0.0)\nCPMAddPackage(\"gh:yhirose/cpp-httplib@0.15.3\")\nmessage(\"find_package(Nope)\")\n",
			[]string{"Crow", "Qt6", "cpp-httplib", "fmt"}},
		"broken json": {"package.json", `{"dependencies":`, nil},
	}
	for name, c := range cases {
		got := parseManifest(c.name, []byte(c.data))
		sort.Strings(got)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// TestDependenciesForUnionsAncestors: a monorepo package sees its own
// manifest and every one above it, and a manifest elsewhere is invisible.
func TestDependenciesForUnionsAncestors(t *testing.T) {
	root := t.TempDir()
	write := func(rel, data string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"devDependencies":{"typescript":"5"}}`)
	write("apps/web/package.json", `{"dependencies":{"next":"15"}}`)
	write("apps/api/package.json", `{"dependencies":{"@nestjs/core":"10"}}`)
	write("apps/api/requirements_test.txt", "pytest\n")

	// Closed before TempDir's cleanup: the listing root is an open directory
	// handle, and Windows will not delete an open directory.
	m := NewManifests(root)
	defer m.Close()
	if got, want := m.DependenciesFor("apps/web/app/page.tsx"), []string{"next", "typescript"}; !reflect.DeepEqual(got, want) {
		t.Errorf("web deps = %v, want %v", got, want)
	}
	if got, want := m.DependenciesFor("apps/api/src/main.ts"), []string{"@nestjs/core", "pytest", "typescript"}; !reflect.DeepEqual(got, want) {
		t.Errorf("api deps = %v, want %v", got, want)
	}
	if got, want := m.DependenciesFor("README.md"), []string{"typescript"}; !reflect.DeepEqual(got, want) {
		t.Errorf("root deps = %v, want %v", got, want)
	}
	var nilManifests *Manifests
	if got := nilManifests.DependenciesFor("x"); got != nil {
		t.Errorf("nil Manifests = %v, want nil", got)
	}
}

// TestManifestLinkIsNotRead: a manifest is repository input, and a link to a
// file outside the repository must contribute nothing.
func TestManifestLinkIsNotRead(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(outside, []byte(`{"dependencies":{"secret":"1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "package.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m := NewManifests(root)
	defer m.Close() // an open handle would keep Windows from removing root
	if got := m.DependenciesFor("a.ts"); len(got) != 0 {
		t.Fatalf("read a linked manifest: %v", got)
	}
}

func TestIsManifest(t *testing.T) {
	for p, want := range map[string]bool{
		"package.json": true, "a/b/composer.json": true, "requirements.txt": true,
		"requirements-dev.txt": true, "requirements_test.txt": true, "go.mod": true,
		"Cargo.toml": true, "pyproject.toml": true,
		"package-lock.json": false, "requirementsx.txt": false, "go.sum": false,
	} {
		if got := IsManifest(p); got != want {
			t.Errorf("IsManifest(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestEveryPackFileIsEmbedded fails when a pack gains a directory the embed
// pattern does not cover, which would ship a detector nobody runs.
func TestEveryPackFileIsEmbedded(t *testing.T) {
	specs, err := packFiles(true)
	if err != nil {
		t.Fatal(err)
	}
	detectors, err := packFiles(false)
	if err != nil {
		t.Fatal(err)
	}
	embedded := map[string]bool{}
	for _, p := range append(specs, detectors...) {
		embedded[p] = true
	}
	err = filepath.WalkDir("packs", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if !d.IsDir() && filepath.Ext(p) == ".json" && !embedded[filepath.ToSlash(p)] {
			t.Errorf("%s is not embedded; extend the go:embed pattern in packs.go", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestLookaheadNamesTheDecoratedClass: the decorator closes before the class
// starts, blank lines and further decorators may sit between them, and the
// lookahead stops short of a class too far away to be the one decorated.
func TestLookaheadNamesTheDecoratedClass(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"typescript"}, WhenImports: []string{"t"},
		Rules: []DetectorRule{{Facet: FacetService, Requires: "@Injectable", Lookahead: 3,
			Pattern: `@Injectable\((?s:.)*?\)\s*(?:@\w+(?:\((?s:.)*?\))?\s*)*export\s+class\s+(?P<name>\w+)`}},
	})
	src := "" +
		"@Injectable({\n" + // 1
		"  providedIn: 'root',\n" +
		"})\n" +
		"\n" +
		"@Other()\n" +
		"export class Kept {}\n" + // 6
		"@Injectable()\n" + // 7
		"const a = 1\n" +
		"const b = 2\n" +
		"const c = 3\n" +
		"export class TooFar {}\n"
	in := engineInput(t, "typescript", "a.ts", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "service Kept @1")
}

func TestWithinAndRequiresAny(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"typescript"}, WhenImports: []string{"t"},
		Rules: []DetectorRule{
			{Facet: FacetRoute, Requires: ":", KeepStrings: true, Within: "serve(",
				Pattern: `^\s*"(?P<name>/[^"]*)"\s*:`},
			{Facet: FacetRoute, RequiresAny: []string{".get(", ".post("}, KeepStrings: true,
				Pattern: `\.(?P<detail>get|post|put)\(\s*"(?P<name>/[^"]*)"`},
		},
	})
	src := "" +
		"const cfg = {\n" +
		"  \"/not-in-serve\": 1,\n" +
		"}\n" +
		"serve({\n" +
		"  \"/in-serve\": h,\n" + // 5
		"})\n" +
		"app.get(\"/a\", h)\n" + // 7
		"app.put(\"/b\", h)\n" + // not in requires_any
		"doc('app.post(\"/c\")')\n" // inside a string: only the gate rejects it
	in := engineInput(t, "typescript", "a.ts", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /in-serve @5", "route /a GET @7")
}

// TestGateSpansTheMatch: a gate on a token at the end of a multi-line match
// (an arrow component's =>) passes, because the gate is read over every line
// the match covers.
func TestGateSpansTheMatch(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"typescript"}, WhenImports: []string{"t"},
		Rules: []DetectorRule{{Facet: FacetComponent, Requires: "=>",
			Pattern: `^\s*export\s+const\s+(?P<name>[A-Z]\w*)\s*=\s*\([^)]*\)\s*=>`}},
	})
	in := engineInput(t, "typescript", "a.tsx", "export const Button = ({\n  label,\n}) => null\n")
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "component Button @1")
}

func TestPathExclude(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"vue"}, PathGlobs: []string{"*.vue"},
		Rules: []DetectorRule{{Facet: FacetComponent, PathPattern: `(?P<name>[^/]+)\.vue$`, PathExclude: `(?:^|/)pages/`}},
	})
	for path, want := range map[string]int{"components/Card.vue": 1, "app/pages/index.vue": 0} {
		in := engineInput(t, "vue", path, "<template />\n")
		if got := set.Detect(in); len(got) != want {
			t.Errorf("%s: %v, want %d facets", path, summary(got), want)
		}
	}
}

func TestRouteMethodsAreUpperCase(t *testing.T) {
	for in, want := range map[string]string{
		"get": "GET", "Delete": "DELETE", "all": "ALL", "page": "page", "views.index": "views.index", "": "",
	} {
		if got := normalizeMethod(in); got != want {
			t.Errorf("normalizeMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRuleOptionValidation(t *testing.T) {
	base := func(r DetectorRule) Detector {
		return Detector{Framework: "a", PathGlobs: []string{"*"}, Rules: []DetectorRule{r}}
	}
	name := `(?P<name>x)`
	cases := map[string]struct {
		r    DetectorRule
		want string
	}{
		"lookahead too far":    {DetectorRule{Facet: FacetRoute, Pattern: name, Lookahead: maxLookahead + 1}, "lookahead"},
		"negative lookahead":   {DetectorRule{Facet: FacetRoute, Pattern: name, Lookahead: -1}, "lookahead"},
		"empty requires_any":   {DetectorRule{Facet: FacetRoute, Pattern: name, RequiresAny: []string{"a", ""}}, "requires_any"},
		"bad path_exclude":     {DetectorRule{Facet: FacetRoute, Pattern: name, PathExclude: "("}, "path_exclude"},
		"keep_strings no gate": {DetectorRule{Facet: FacetRoute, Pattern: name, KeepStrings: true}, "requires"},
	}
	for label, c := range cases {
		err := base(c.r).Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", label, err, c.want)
		}
	}
	ok := DetectorRule{Facet: FacetRoute, Pattern: name, KeepStrings: true, RequiresAny: []string{".get("}}
	if err := base(ok).Validate(); err != nil {
		t.Errorf("keep_strings gated by requires_any rejected: %v", err)
	}
}

// TestStatementScopedPrefixes: a group's prefix reaches the routes inside its
// call and no further, and nested groups join outermost first.
func TestStatementScopedPrefixes(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"php"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{{Requires: "group(", KeepStrings: true, Scope: "statement",
			Pattern: `group\(\s*'(?P<prefix>[^']*)'`}},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "get(", KeepStrings: true,
			Pattern: `get\(\s*'(?P<name>[^']*)'`}},
	})
	src := "" +
		"<?php\n" +
		"get('/before');\n" + // 2
		"group('admin', function () {\n" +
		"    get('/users');\n" + // 4
		"    group('posts', function () {\n" +
		"        get('/{id}');\n" + // 6
		"    });\n" +
		"    get('/stats');\n" + // 8
		"});\n" +
		"get('/after');\n" // 10
	in := engineInput(t, "php", "routes.php", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in),
		"route /before @2", "route /admin/users @4", "route /admin/posts/{id} @6",
		"route /admin/stats @8", "route /after @10")
}

// TestPrefixLookahead: a class-level attribute is a prefix only when a class
// follows it; the same attribute on a method is a route.
func TestPrefixLookahead(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"php"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{{Requires: "#[Route", KeepStrings: true, Lookahead: 2,
			Pattern: `#\[Route\('(?P<prefix>[^']*)'\)\]\s*class\b`}},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "#[Route", KeepStrings: true, Lookahead: 2,
			Pattern: `#\[Route\('(?P<name>[^']*)'\)\]\s*public\s+function\b`}},
	})
	src := "<?php\n#[Route('/blog')]\nclass Blog {\n    #[Route('/{slug}')]\n    public function show() {}\n}\n"
	in := engineInput(t, "php", "Blog.php", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /blog/{slug} @4")
}

func TestNameExclude(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"php"}, PathGlobs: []string{"routes.php"},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "$route[", KeepStrings: true,
			NameExclude: `^default_controller$`, Pattern: `\$route\['(?P<name>[^']+)'\]`}},
	})
	in := engineInput(t, "php", "routes.php", "<?php\n$route['default_controller'] = 'w';\n$route['blog'] = 'b';\n")
	wantSummary(t, set.Detect(in), "route blog @3")
}

func TestPrefixValidation(t *testing.T) {
	base := func(p PrefixRule) Detector {
		return Detector{Framework: "a", PathGlobs: []string{"*"}, Prefixes: []PrefixRule{p},
			Rules: []DetectorRule{{Facet: FacetRoute, Pattern: `(?P<name>x)`}}}
	}
	for label, c := range map[string]struct {
		p    PrefixRule
		want string
	}{
		"unknown scope":      {PrefixRule{Pattern: `(?P<prefix>x)`, Scope: "file"}, "scope"},
		"statement with var": {PrefixRule{Pattern: `(?P<var>v)(?P<prefix>x)`, Scope: "statement"}, "variable"},
		"lookahead too far":  {PrefixRule{Pattern: `(?P<prefix>x)`, Lookahead: maxLookahead + 1}, "lookahead"},
	} {
		if err := base(c.p).Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", label, err, c.want)
		}
	}
}
