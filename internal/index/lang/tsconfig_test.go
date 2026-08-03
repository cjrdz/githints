package lang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	src := []byte(`{
	// line comment
	"compilerOptions": {
		"paths": {
			"@core/*": ["./src/core/*"], // trailing comment
			"@shared/*": ["./src/shared/*"],
		},
	}, /* block comment */
	"url": "https://example.com//not-a-comment",
}`)
	out := stripJSONC(src)
	var v struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped JSONC did not parse: %v\n%s", err, out)
	}
	if len(v.CompilerOptions.Paths) != 2 {
		t.Errorf("paths = %v", v.CompilerOptions.Paths)
	}
	if v.URL != "https://example.com//not-a-comment" {
		t.Errorf("string contents mangled: %q", v.URL)
	}
}

func TestTSPathsConfigResolve(t *testing.T) {
	cfg := &TSPathsConfig{
		baseDir: "",
		paths: map[string][]string{
			"@core/*":    {"./src/core/*"},
			"@shared/*":  {"./src/shared/*"},
			"@core/di/*": {"./src/vendor/di/*"}, // longer prefix wins over @core/*
			"exact":      {"./src/exact.ts"},
		},
	}

	for _, tc := range []struct {
		spec string
		want string
		ok   bool
	}{
		{"@core/bff/proxy", "src/core/bff/proxy", true},
		{"@core/di/container", "src/vendor/di/container", true},
		{"@shared/utils/format", "src/shared/utils/format", true},
		{"exact", "src/exact.ts", true},
		{"svelte", "", false},
		{"@other/x", "", false},
		{"@core", "", false}, // prefix requires the rest to exist beyond "/"
	} {
		got, ok := cfg.Resolve(tc.spec)
		if ok != tc.ok {
			t.Errorf("Resolve(%q) ok = %v, want %v", tc.spec, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}

func TestTSPathsConfigResolveBaseURL(t *testing.T) {
	cfg := &TSPathsConfig{
		baseDir: "src",
		paths:   map[string][]string{"@/*": {"./*"}},
	}
	got, ok := cfg.Resolve("@/lib/util")
	if !ok || got != "src/lib/util" {
		t.Errorf("Resolve(@/lib/util) = %q, %v; want src/lib/util, true", got, ok)
	}
}

func TestLoadTSPathsConfig(t *testing.T) {
	dir := t.TempDir()
	if cfg := LoadTSPathsConfig(dir); cfg != nil {
		t.Errorf("expected nil for missing tsconfig, got %v", cfg)
	}

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tsconfig.json", `{
	// JSONC is fine
	"compilerOptions": {
		"paths": {
			"@features/*": ["./src/features/*"],
		},
	},
}`)
	cfg := LoadTSPathsConfig(dir)
	if cfg == nil {
		t.Fatal("expected config from tsconfig.json")
	}
	got, ok := cfg.Resolve("@features/catalog/api")
	if !ok || got != "src/features/catalog/api" {
		t.Errorf("Resolve = %q, %v; want src/features/catalog/api, true", got, ok)
	}

	// jsconfig.json is the fallback when tsconfig.json is absent.
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "jsconfig.json"), []byte(`{"compilerOptions":{"paths":{"@/*":["./app/*"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = LoadTSPathsConfig(dir2)
	if cfg == nil {
		t.Fatal("expected config from jsconfig.json")
	}
	if got, ok := cfg.Resolve("@/x"); !ok || got != "app/x" {
		t.Errorf("Resolve = %q, %v; want app/x, true", got, ok)
	}
}

func TestTypeScriptImportAliasResolution(t *testing.T) {
	SetActiveTSPathsConfig(&TSPathsConfig{
		paths: map[string][]string{
			"@core/*":   {"./src/core/*"},
			"@shared/*": {"./src/shared/*"},
		},
	})
	defer SetActiveTSPathsConfig(nil)

	src := []byte(`import { proxy } from "@core/bff/proxy";
import { format } from "@shared/utils/format";
import { onMount } from "svelte";
import { local } from "./local";
`)
	p := TypeScriptParser{}
	_, imports, err := p.Parse("src/features/catalog/page.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := sortedImportPaths(imports)
	want := []string{
		"src/core/bff/proxy",         // alias resolved via tsconfig paths
		"src/features/catalog/local", // relative still works
		"src/shared/utils/format",    // alias resolved
		"svelte",                     // true package stays raw
	}
	if len(got) != len(want) {
		t.Fatalf("imports = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("imports = %v\nwant %v", got, want)
		}
	}
}

func TestTypeScriptImportAliasRawWithoutConfig(t *testing.T) {
	SetActiveTSPathsConfig(nil) // no tsconfig paths: aliases stay raw
	src := []byte(`import { proxy } from "@core/bff/proxy";`)
	p := TypeScriptParser{}
	_, imports, err := p.Parse("src/page.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(imports) != 1 || imports[0].ImportedPath != "@core/bff/proxy" {
		t.Errorf("imports = %v, want raw @core/bff/proxy", imports)
	}
}
