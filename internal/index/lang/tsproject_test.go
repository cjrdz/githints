package lang

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A TypeScript monorepo: per-app tsconfigs (one extending a shared base, one
// with its own paths), a nested tsconfig without paths, and workspace
// packages imported by name. Only the root tsconfig used to be read, and
// package names were never resolved.
func TestTypeScriptMonorepoResolution(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"tsconfig.base.json": `{ "compilerOptions": { "baseUrl": ".", "paths": { "@shared/*": ["packages/shared/src/*"] } } }`,
		"apps/web/tsconfig.json": `{
			// JSONC, extending the base
			"extends": "../../tsconfig.base.json",
			"compilerOptions": { "strict": true, },
		}`,
		"apps/web/test/tsconfig.json":        `{ "compilerOptions": { "types": ["vitest"] } }`,
		"apps/admin/tsconfig.json":           `{ "compilerOptions": { "paths": { "@/*": ["./src/*"] } } }`,
		"packages/ui/package.json":           `{ "name": "@acme/ui", "main": "dist/index.js", "exports": { ".": "./dist/index.js", "./theme": { "types": "./lib/theme.d.ts", "import": "./lib/theme.ts" } } }`,
		"packages/ui/src/index.ts":           "export const ui = 1;\n",
		"packages/ui/src/button.tsx":         "export const Button = 1;\n",
		"packages/ui/lib/theme.ts":           "export const theme = 1;\n",
		"packages/shared/package.json":       `{ "name": "shared-utils", "source": "src/main.ts" }`,
		"packages/shared/src/main.ts":        "export const x = 1;\n",
		"node_modules/@acme/ui/package.json": `{ "name": "@acme/ui", "main": "evil.js" }`,
	})

	p := TypeScriptParser{}
	defer p.BeginScan(root)()

	imports := func(file, src string) []string {
		t.Helper()
		_, imps, err := p.Parse(file, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(imps))
		for i, imp := range imps {
			out[i] = imp.ImportedPath
		}
		sort.Strings(out)
		return out
	}
	check := func(name string, got []string, want ...string) {
		t.Helper()
		sort.Strings(want)
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got %v, want %v", name, got, want)
				return
			}
		}
	}

	check("extends inherits base paths",
		imports("apps/web/src/a.ts", `import { u } from "@shared/util";`),
		"packages/shared/src/util")
	check("a nested tsconfig without paths does not hide the app's",
		imports("apps/web/test/a.test.ts", `import { u } from "@shared/util";`),
		"packages/shared/src/util")
	check("paths without baseUrl resolve from their own tsconfig",
		imports("apps/admin/src/page.ts", `import { x } from "@/lib/x";`),
		"apps/admin/src/lib/x")
	check("@ alias does not leak into another app",
		imports("apps/web/src/b.ts", `import { x } from "@/lib/x";`),
		"@/lib/x")
	check("workspace packages",
		imports("apps/web/src/c.ts", `
import { ui } from "@acme/ui";
import { Button } from "@acme/ui/button";
import { theme } from "@acme/ui/theme";
import { x } from "shared-utils";
import React from "react";`),
		"packages/ui/src", "packages/ui/src/button", "packages/ui/lib/theme", "packages/shared/src/main", "react")
}

func TestReadTSConfigChainRefusesEscapesAndCycles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/tsconfig.json": `{ "extends": "../b/tsconfig.json", "compilerOptions": { "paths": { "@a/*": ["./*"] } } }`,
		"b/tsconfig.json": `{ "extends": "../a/tsconfig.json" }`,
		"c/tsconfig.json": `{ "extends": "../../outside.json" }`,
	})
	if c := loadTSConfigIn(root, "a"); c == nil {
		t.Error("a cycle in extends lost the config's own paths")
	}
	if c := loadTSConfigIn(root, "c"); c != nil {
		t.Errorf("extends outside the repository was followed: %+v", c)
	}
}

// TypeScript, Vue, Svelte and Astro each begin a scan; they must share one
// project (one walk of the repository) and it must last until the last ends.
func TestTSScanStateIsShared(t *testing.T) {
	root := t.TempDir()
	endTS := TypeScriptParser{}.BeginScan(root)
	endVue := VueParser{}.BeginScan(root)
	_, first := activeTSState()
	_, second := activeTSState()
	if first == nil || first != second {
		t.Fatal("parsers did not share one project")
	}
	endTS()
	if _, p := activeTSState(); p != first {
		t.Fatal("project torn down while another parser's scan was still running")
	}
	endVue()
	if _, p := activeTSState(); p != nil {
		t.Fatal("project outlived every scan")
	}
}

// A Python monorepo: each project is named from its own root (the nearest
// pyproject.toml / setup.py / setup.cfg, and its src/), and relative imports
// resolve against the importing module. Both used to be repo-root dotted
// paths, which no `import` statement in such a repo ever matches.
func TestPythonMonorepoImportPaths(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"services/api/pyproject.toml":         "[project]\nname = \"api\"\n",
		"services/api/src/app/__init__.py":    "",
		"services/api/src/app/models.py":      "",
		"services/api/src/app/routes/user.py": "",
		"libs/common/setup.py":                "",
		"libs/common/common/log.py":           "",
		"scripts/tool.py":                     "",
	})
	p := NewRegistry().ForLanguage("python").(*SpecParser)
	for file, want := range map[string]string{
		"services/api/src/app/models.py":      "app.models",
		"services/api/src/app/__init__.py":    "app",
		"services/api/src/app/routes/user.py": "app.routes.user",
		"libs/common/common/log.py":           "common.log",
		"scripts/tool.py":                     "scripts.tool", // no project: repo-relative, as before
	} {
		if got, err := p.ImportPath(root, file); err != nil || got != want {
			t.Errorf("ImportPath(%s) = %q, %v; want %q", file, got, err, want)
		}
	}

	defer p.BeginScan(root)()
	_, imps, err := p.Parse("services/api/src/app/routes/user.py", []byte(
		"from ..models import User\nfrom . import helpers\nfrom .... import nowhere\nimport common.log\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, i := range imps {
		got = append(got, i.ImportedPath)
	}
	sort.Strings(got)
	want := []string{"....", "app.models", "app.routes", "common.log"}
	if len(got) != len(want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("imports = %v, want %v", got, want)
		}
	}
}

// Java's source root is stripped inside every module of a multi-module
// build, not only at the repository root.
func TestJavaMultiModuleImportPath(t *testing.T) {
	p := NewRegistry().ForLanguage("java").(ImportPathResolver)
	for file, want := range map[string]string{
		"src/main/java/com/acme/App.java":              "com.acme.App",
		"billing/src/main/java/com/acme/bill/Inv.java": "com.acme.bill.Inv",
		"core/src/test/java/com/acme/CoreTest.java":    "com.acme.CoreTest",
	} {
		if got, err := p.ImportPath(t.TempDir(), file); err != nil || got != want {
			t.Errorf("ImportPath(%s) = %q, %v; want %q", file, got, err, want)
		}
	}
}
