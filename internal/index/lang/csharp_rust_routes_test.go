package lang

import (
	"reflect"
	"sort"
	"testing"
)

func TestUnlessImportsVetoes(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"go"}, WhenImports: []string{"net/http"},
		UnlessImports: []string{"github.com/go-chi/chi"},
		Rules:         []DetectorRule{{Facet: FacetRoute, Requires: "Handle", KeepStrings: true, Pattern: `Handle\("(?P<name>[^"]*)"`}},
	})
	src := "package x\n\nfunc f() { mux.Handle(\"/a\", h) }\n"
	in := engineInput(t, "go", "x.go", src)
	in.Imports = []Import{{ImportedPath: "net/http"}}
	if got := set.Detect(in); len(got) != 1 {
		t.Fatalf("without the veto: %v", summary(got))
	}
	in.Imports = append(in.Imports, Import{ImportedPath: "github.com/go-chi/chi/v5"})
	if got := set.Detect(in); len(got) != 0 {
		t.Fatalf("vetoed detector still ran: %v", summary(got))
	}
}

// TestParentPrefixesChain: v1 := api.Group("/v1") under api := r.Group("/api").
func TestParentPrefixesChain(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"go"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{{Requires: ".Group(", KeepStrings: true,
			Pattern: `(?P<var>\w+)\s*:=\s*(?P<parent>\w+)\.Group\("(?P<prefix>[^"]*)"`}},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: ".GET(", KeepStrings: true,
			Pattern: `(?P<var>\w+)\.GET\("(?P<name>[^"]*)"`}},
	})
	src := "package x\n\nfunc f() {\n\tapi := r.Group(\"/api\")\n\tv1 := api.Group(\"/v1\")\n\tv1.GET(\"/users\", h)\n\tapi.GET(\"/health\", h)\n}\n"
	in := engineInput(t, "go", "x.go", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /api/v1/users @6", "route /api/health @7")
}

// TestStatementScopeReachesTheEnclosingStatement: Actix's web::scope("/api")
// closes on its own line; the routes chained after it are inside the
// enclosing .service( call, and nothing after that call is.
func TestStatementScopeReachesTheEnclosingStatement(t *testing.T) {
	set := mustSet(t, Detector{
		Framework: "t", Languages: []string{"rust"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{{Requires: "scope(", KeepStrings: true, Scope: "statement",
			Pattern: `scope\("(?P<prefix>[^"]*)"\)`}},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "route(", KeepStrings: true,
			Pattern: `route\("(?P<name>[^"]*)"`}},
	})
	src := "fn c() {\n" +
		"    cfg.service(\n" +
		"        web::scope(\"/api\")\n" +
		"            .route(\"/users\", h),\n" + // 4
		"    );\n" +
		"    cfg.route(\"/outside\", h);\n" + // 6
		"}\n"
	in := engineInput(t, "rust", "a.rs", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, set.Detect(in), "route /api/users @4", "route /outside @6")
}

func TestFlatRoute(t *testing.T) {
	for in, want := range map[string]string{
		"_index":          "/",
		"users.$id.edit":  "/users/:id/edit",
		"_auth.login":     "/login",
		"($lang).about":   "/:lang?/about",
		"files.$":         "/files/*",
		"sitemap[.]xml":   "/sitemap.xml",
		"concerts_.mine":  "/concerts/mine",
		"dashboard.route": "/dashboard/route",
	} {
		if got := flatRoute(in); got != want {
			t.Errorf("flatRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCsprojDeps(t *testing.T) {
	got := csprojDeps([]byte(`<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Microsoft.EntityFrameworkCore" Version="9.0.0" />
    <PackageReference Version="1" Include="Serilog.AspNetCore"/>
  </ItemGroup>
</Project>`))
	sort.Strings(got)
	want := []string{"Microsoft.EntityFrameworkCore", "Microsoft.NET.Sdk.Web", "Serilog.AspNetCore"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if !IsManifest("src/Api/Api.csproj") || IsManifest(".csproj") {
		t.Error("IsManifest disagrees about .csproj files")
	}
}

// TestCSharpDeclaredNamespace: the key is the namespace a file declares,
// file-scoped or block, never one inside a comment.
func TestCSharpDeclaredNamespace(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/Models.cs":  "// namespace Wrong.One;\nnamespace App.Models;\npublic class A {}\n",
		"b/Service.cs": "using App.Models;\n\nnamespace App.Services\n{\n    class S {}\n}\n",
		"Program.cs":   "var x = 1;\n",
	})
	r := NewRegistry()
	for file, want := range map[string]string{"a/Models.cs": "App.Models", "b/Service.cs": "App.Services"} {
		if got, err := r.ImportPath(root, file); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	if _, err := r.ImportPath(root, "Program.cs"); err == nil {
		t.Error("a file declaring no namespace got a key")
	}
}

func TestRustModules(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Cargo.toml":            "[package]\nname = \"todo-api\"\n",
		"src/main.rs":           "",
		"src/models.rs":         "",
		"src/handlers/mod.rs":   "",
		"src/handlers/todos.rs": "",
		"src/bin/tool.rs":       "",
		"tests/it.rs":           "",
		"ws/Cargo.toml":         "[workspace]\nmembers = [\"x\"]\n",
	})
	for file, want := range map[string]string{
		"src/main.rs":           "todo_api",
		"src/models.rs":         "todo_api::models",
		"src/handlers/mod.rs":   "todo_api::handlers",
		"src/handlers/todos.rs": "todo_api::handlers::todos",
	} {
		if got, err := rustModuleKey(root, file); err != nil || got != want {
			t.Errorf("key(%s) = %q, %v; want %q", file, got, err, want)
		}
	}
	for _, file := range []string{"src/bin/tool.rs", "tests/it.rs"} {
		if _, err := rustModuleKey(root, file); err == nil {
			t.Errorf("%s got a module key", file)
		}
	}
	from := "src/handlers/todos.rs"
	for use, want := range map[string]string{
		"crate::models::Todo":        "todo_api::models",
		"todo_api::handlers::todos":  "todo_api::handlers::todos",
		"super::super::models::Todo": "todo_api::models",
		"self::Thing":                "todo_api::handlers::todos",
		"super::super::super::x":     "super::super::super::x", // above the crate
		"serde::Serialize":           "serde::Serialize",
		"crate::missing::Thing":      "todo_api",
	} {
		if got := resolveRustUse(root, from, use); got != want {
			t.Errorf("resolve(%q) = %q, want %q", use, got, want)
		}
	}
	if got, want := rustUseMembers("crate::", " models::Todo, self, db as d, {x}"), []string{"crate::models::Todo", "crate::", "crate::db"}; !reflect.DeepEqual(got, want) {
		t.Errorf("members = %q, want %q", got, want)
	}
}
