package lang

import (
	"reflect"
	"sort"
	"testing"
)

// TestBlockAndIndentScopes: a block prefix reaches what its braces enclose
// (Ktor), an indent prefix what is indented under it (Rails), and nothing
// after either.
func TestBlockAndIndentScopes(t *testing.T) {
	block := mustSet(t, Detector{
		Framework: "t", Languages: []string{"kotlin"}, WhenImports: []string{"t"},
		Prefixes: []PrefixRule{{Requires: "route(", KeepStrings: true, Scope: "block",
			Pattern: `route\("(?P<prefix>[^"]*)"\)\s*\{`}},
		Rules: []DetectorRule{{Facet: FacetRoute, Requires: "get(", KeepStrings: true, Pattern: `get\("(?P<name>[^"]*)"\)`}},
	})
	src := "fun r() {\n" +
		"    route(\"/api\") {\n" +
		"        route(\"/v1\") {\n" +
		"            get(\"/a\") { }\n" + // 4
		"        }\n" +
		"        get(\"/b\") { }\n" + // 6
		"    }\n" +
		"    get(\"/c\") { }\n" + // 8
		"}\n"
	in := engineInput(t, "kotlin", "r.kt", src)
	in.Imports = []Import{{ImportedPath: "t"}}
	wantSummary(t, block.Detect(in), "route /api/v1/a @4", "route /api/b @6", "route /c @8")

	indent := mustSet(t, Detector{
		Framework: "t", Languages: []string{"ruby"}, PathGlobs: []string{"routes.rb"},
		Prefixes: []PrefixRule{{Requires: "namespace", Scope: "indent", Pattern: `namespace\s+:(?P<prefix>\w+)\s+do`}},
		Rules:    []DetectorRule{{Facet: FacetRoute, Requires: "get", KeepStrings: true, Pattern: `get\s+"(?P<name>[^"]*)"`}},
	})
	src = "draw do\n" +
		"  namespace :admin do\n" +
		"    get \"users\"\n" + // 3
		"\n" +
		"    get \"stats\"\n" + // 5
		"  end\n" +
		"  get \"home\"\n" + // 7
		"end\n"
	wantSummary(t, indent.Detect(engineInput(t, "ruby", "routes.rb", src)),
		"route /admin/users @3", "route /admin/stats @5", "route home @7")
}

func TestPathSegments(t *testing.T) {
	for in, want := range map[string]string{
		`"api", "v1"`: "api/v1",
		`"/hello"`:    "hello",
		`":id"`:       ":id",
		`plain`:       "plain",
		`""`:          "",
	} {
		if got := pathSegments(in); got != want {
			t.Errorf("pathSegments(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIgnoreNames: Dart's if (x) { has a function's shape and is not one.
func TestIgnoreNames(t *testing.T) {
	src := "void main() {\n}\n\nWidget build(BuildContext c) {\n  if (x) {\n  }\n}\n"
	symbols, _ := parseWith(t, "dart", "lib/a.dart", src)
	got := kindsByName(symbols)
	if _, ok := got["if"]; ok {
		t.Error("if indexed as a function")
	}
	for _, name := range []string{"main", "build"} {
		if got[name] != KindFunc {
			t.Errorf("%s: kind %q, want func", name, got[name])
		}
	}
}

func TestKotlinImportsNamePackages(t *testing.T) {
	src := "package com.x.ui\n\nimport com.x.data.User\nimport com.x.util.*\nimport androidx.compose.runtime.Composable as C\n\nclass A\n"
	_, imports := parseWith(t, "kotlin", "app/src/main/java/com/x/ui/A.kt", src)
	want := []string{"com.x.data", "com.x.util", "androidx.compose.runtime"}
	if got := importPaths(imports); !reflect.DeepEqual(got, want) {
		t.Errorf("imports = %q, want %q", got, want)
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/A.kt": src})
	if got, err := NewRegistry().ImportPath(root, "a/A.kt"); err != nil || got != "com.x.ui" {
		t.Errorf("key = %q, %v; want com.x.ui", got, err)
	}
}

func TestSPMModuleKey(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Package.swift":                   "",
		"Sources/Kit/Entry.swift":         "",
		"Sources/App/Views/List.swift":    "",
		"Tests/KitTests/EntryTests.swift": "",
		"Plugins/Gen/main.swift":          "",
		"ios/Notes/Scene.swift":           "",
	})
	for file, want := range map[string]string{
		"Sources/Kit/Entry.swift": "Kit", "Sources/App/Views/List.swift": "App", "Tests/KitTests/EntryTests.swift": "KitTests",
	} {
		if got, err := spmModuleKey(root, file); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	for _, file := range []string{"Plugins/Gen/main.swift", "ios/Notes/Scene.swift"} {
		if _, err := spmModuleKey(root, file); err == nil {
			t.Errorf("%s got a module key", file)
		}
	}
}

func TestResolveDartImport(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/pubspec.yaml": "name: shop_app\ndependencies:\n  flutter:\n    sdk: flutter\n",
	})
	from := "app/lib/screens/home.dart"
	for uri, want := range map[string]string{
		"package:shop_app/state/cart.dart": "app/lib/state/cart.dart",
		"../state/cart.dart":               "app/lib/state/cart.dart",
		"widgets/button.dart":              "app/lib/screens/widgets/button.dart",
		"package:flutter/material.dart":    "package:flutter/material.dart",
		"dart:async":                       "dart:async",
		"../../../../outside.dart":         "../../../../outside.dart",
	} {
		if got := resolveDartImport(root, from, uri); got != want {
			t.Errorf("resolve(%q) = %q, want %q", uri, got, want)
		}
	}
}

// TestRubyRelativeImports: require_relative joins the requiring file's
// directory, and lib/ is stripped as it is from a file's own key.
func TestRubyRelativeImports(t *testing.T) {
	src := "require \"json\"\nrequire_relative \"lib/greeter\"\nrequire_relative '../shared/util.rb'\n"
	_, imports := parseWith(t, "ruby", "app/main.rb", src)
	want := []string{"json", "greeter", "shared/util"}
	if got := importPaths(imports); !reflect.DeepEqual(got, want) {
		t.Errorf("imports = %q, want %q", got, want)
	}
}

func TestGemfileAndPubspecDeps(t *testing.T) {
	gems := gemfileDeps([]byte("source 'x'\ngem \"rails\", \"~> 7\"\n  gem 'pg'\n# gem 'nope'\n"))
	if want := []string{"rails", "pg"}; !reflect.DeepEqual(gems, want) {
		t.Errorf("gems = %q, want %q", gems, want)
	}
	deps := pubspecDeps([]byte("name: x\nenvironment:\n  sdk: ^3.0.0\ndependencies:\n  flutter:\n    sdk: flutter\n  go_router: ^14.0.0\n\ndev_dependencies:\n  flutter_test:\n    sdk: flutter\nflutter:\n  uses-material-design: true\n"))
	sort.Strings(deps)
	if want := []string{"flutter", "flutter_test", "go_router"}; !reflect.DeepEqual(deps, want) {
		t.Errorf("pubspec deps = %q, want %q", deps, want)
	}
	for _, p := range []string{"Gemfile", "app/pubspec.yaml"} {
		if !IsManifest(p) {
			t.Errorf("%s is not a manifest", p)
		}
	}
}
