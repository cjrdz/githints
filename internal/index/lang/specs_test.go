package lang

import (
	"testing"
)

// parseWith runs a shipped language spec over source and returns what it found.
func parseWith(t *testing.T, language, file, src string) ([]Symbol, []Import) {
	t.Helper()
	p := NewRegistry().ForLanguage(language)
	if p == nil {
		t.Fatalf("language %s is not registered", language)
	}
	symbols, imports, err := p.Parse(file, []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return symbols, imports
}

func kindsByName(symbols []Symbol) map[string]SymbolKind {
	out := make(map[string]SymbolKind, len(symbols))
	for _, s := range symbols {
		out[s.Name] = s.Kind
	}
	return out
}

func importPaths(imports []Import) []string {
	out := make([]string, 0, len(imports))
	for _, i := range imports {
		out = append(out, i.ImportedPath)
	}
	return out
}

func TestRustSpec(t *testing.T) {
	src := "" +
		"use std::collections::HashMap;\n" +
		"pub use crate::thing::Thing;\n" +
		"\n" +
		"pub const MAX: usize = 10;\n" +
		"static mut COUNTER: u32 = 0;\n" +
		"\n" +
		"pub struct Config<'a> { name: &'a str }\n" +
		"pub enum State { On, Off }\n" +
		"pub trait Runner { fn run(&self); }\n" +
		"type Alias = HashMap<String, u32>;\n" +
		"\n" +
		"impl Config<'_> {\n" +
		"    pub async fn load(path: &str) -> Result<Self, Error> {\n" +
		"        fn helper() {}\n" +
		"        todo!()\n" +
		"    }\n" +
		"}\n"

	symbols, imports := parseWith(t, "rust", "src/config.rs", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"MAX":     KindConst,
		"COUNTER": KindVar,
		"Config":  KindType,
		"State":   KindType,
		"Runner":  KindType,
		"Alias":   KindType,
		"load":    KindFunc,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	// A fn inside a fn body is a local helper.
	if _, found := got["helper"]; found {
		t.Error("a function-local fn was indexed")
	}
	// The lifetime in `&'a str` must not have opened a string and swallowed
	// the rest of the line.
	if _, found := got["Config"]; !found {
		t.Error("a lifetime quote blanked the struct declaration")
	}

	if want := []string{"std::collections::HashMap", "crate::thing::Thing"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

// TestRustLifetimeDoesNotBlankLine is the specific hazard the spec omits char
// literals for: a lifetime opens a quote that never closes.
func TestRustLifetimeDoesNotBlankLine(t *testing.T) {
	symbols, _ := parseWith(t, "rust", "a.rs",
		"pub fn borrow<'a>(x: &'a str) -> &'a str { x }\npub struct After;\n")
	got := kindsByName(symbols)
	if got["borrow"] != KindFunc {
		t.Errorf("borrow not indexed: %v", got)
	}
	if got["After"] != KindType {
		t.Errorf("the declaration after a lifetime was lost: %v", got)
	}
}

func TestSQLSpec(t *testing.T) {
	src := "" +
		"-- a comment mentioning CREATE TABLE ghosts\n" +
		"CREATE TABLE users (\n" +
		"  id SERIAL PRIMARY KEY,\n" +
		"  name TEXT NOT NULL DEFAULT 'CREATE TABLE in a string'\n" +
		");\n" +
		"\n" +
		"create table if not exists sessions (id int);\n" +
		"CREATE UNIQUE INDEX idx_users_name ON users(name);\n" +
		"CREATE OR REPLACE VIEW active_users AS SELECT * FROM users;\n" +
		"CREATE FUNCTION bump(n int) RETURNS int AS $$ SELECT n + 1 $$ LANGUAGE sql;\n"

	symbols, _ := parseWith(t, "sql", "schema.sql", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"users":          KindType,
		"sessions":       KindType, // lower case: patterns are case-insensitive
		"idx_users_name": KindVar,
		"active_users":   KindType,
		"bump":           KindFunc,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	if _, found := got["ghosts"]; found {
		t.Error("a table named in a comment was indexed")
	}
	if len(got) != 5 {
		t.Errorf("symbols = %v, want exactly the five real objects", got)
	}
}

func TestJavaSpec(t *testing.T) {
	src := "" +
		"package com.example.app;\n" +
		"\n" +
		"import java.util.List;\n" +
		"import static org.junit.Assert.assertEquals;\n" +
		"\n" +
		"public class UserService implements Service {\n" +
		"    private static final String NAME = \"svc\";\n" +
		"\n" +
		"    public List<User> findAll(int limit) {\n" +
		"        helperCall(limit);\n" +
		"        return null;\n" +
		"    }\n" +
		"\n" +
		"    @Override\n" +
		"    protected void close() throws Exception {\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"interface Service {}\n" +
		"enum Status { ON, OFF }\n" +
		"record Point(int x, int y) {}\n"

	symbols, imports := parseWith(t, "java", "src/main/java/com/example/app/UserService.java", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"UserService": KindType,
		"findAll":     KindMethod,
		"close":       KindMethod,
		"Service":     KindType,
		"Status":      KindType,
		"Point":       KindType,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	// A bare call inside a method body must not look like a declaration.
	if _, found := got["helperCall"]; found {
		t.Error("a method call was indexed as a method")
	}

	if want := []string{"java.util.List", "org.junit.Assert.assertEquals"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

// TestJavaImportPathStripsSourceRoot pins the inverse relationship. A class is
// imported by its fully qualified name, which is its path below src/main/java;
// without stripping that root the key would match nothing.
func TestJavaImportPathStripsSourceRoot(t *testing.T) {
	p := NewRegistry().ForLanguage("java")
	resolver, ok := p.(ImportPathResolver)
	if !ok {
		t.Fatal("java does not resolve import paths")
	}
	for _, tc := range []struct{ file, want string }{
		{"src/main/java/com/example/app/UserService.java", "com.example.app.UserService"},
		{"src/test/java/com/example/AppTest.java", "com.example.AppTest"},
		{"com/example/Bare.java", "com.example.Bare"},
	} {
		got, err := resolver.ImportPath("/repo", tc.file)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s -> %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestCSharpSpec(t *testing.T) {
	src := "" +
		"using System;\n" +
		"using System.Collections.Generic;\n" +
		"\n" +
		"namespace App.Services;\n" +
		"\n" +
		"public sealed class UserService : IUserService\n" +
		"{\n" +
		"    public async Task<List<User>> FindAllAsync(int limit)\n" +
		"    {\n" +
		"        DoSomething(limit);\n" +
		"        return null;\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"public interface IUserService { }\n" +
		"public record Point(int X, int Y);\n" +
		"public enum Status { On, Off }\n" +
		"internal struct Pair { }\n"

	symbols, imports := parseWith(t, "csharp", "App/Services/UserService.cs", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"UserService":  KindType,
		"FindAllAsync": KindMethod,
		"IUserService": KindType,
		"Point":        KindType,
		"Status":       KindType,
		"Pair":         KindType,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	if _, found := got["DoSomething"]; found {
		t.Error("a method call was indexed as a method")
	}

	if want := []string{"System", "System.Collections.Generic"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

func TestPHPSpec(t *testing.T) {
	src := "" +
		"<?php\n" +
		"\n" +
		"namespace App\\Models;\n" +
		"\n" +
		"use Illuminate\\Database\\Eloquent\\Model;\n" +
		"use App\\Contracts\\Repo;\n" +
		"\n" +
		"# a hash comment mentioning class Ghost\n" +
		"// a slash comment mentioning function ghost\n" +
		"\n" +
		"final class User extends Model\n" +
		"{\n" +
		"    const TABLE = 'users';\n" +
		"\n" +
		"    public function scopeActive($query)\n" +
		"    {\n" +
		"        return $query->where('active', 1);\n" +
		"    }\n" +
		"}\n" +
		"\n" +
		"interface Repo {}\n" +
		"trait Timestamps {}\n" +
		"enum Status: string { case On = 'on'; }\n"

	symbols, imports := parseWith(t, "php", "app/Models/User.php", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"User":        KindType,
		"TABLE":       KindConst,
		"scopeActive": KindFunc,
		"Repo":        KindType,
		"Timestamps":  KindType,
		"Status":      KindType,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	for _, ghost := range []string{"Ghost", "ghost"} {
		if _, found := got[ghost]; found {
			t.Errorf("%s was indexed from a comment", ghost)
		}
	}

	if want := []string{`Illuminate\Database\Eloquent\Model`, `App\Contracts\Repo`}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

func TestVueParser(t *testing.T) {
	src := "" +
		"<template>\n" +
		"  <div class=\"card\">{{ title }}</div>\n" +
		"</template>\n" +
		"\n" +
		"<script setup lang=\"ts\">\n" +
		"import { ref } from \"vue\";\n" +
		"import Helper from \"./Helper.vue\";\n" +
		"\n" +
		"export function useCard() {\n" +
		"  return ref(1);\n" +
		"}\n" +
		"</script>\n" +
		"\n" +
		"<style scoped>\n" +
		".card { color: red; }\n" +
		"</style>\n"

	symbols, imports := parseWith(t, "vue", "ui/Card.vue", src)
	got := kindsByName(symbols)
	if got["useCard"] != KindFunc {
		t.Errorf("useCard not indexed: %v", got)
	}
	// Line numbers must be host-file coordinates, not script-block ones.
	for _, s := range symbols {
		if s.Name == "useCard" && s.LineStart != 9 {
			t.Errorf("useCard LineStart = %d, want 9 (host-file line)", s.LineStart)
		}
	}
	if want := []string{"vue", "ui/Helper"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

// TestVueJoinsTheTypeScriptImportKeyspace checks a .vue file can be found by
// the files that import it, the way .svelte and .astro already are.
func TestVueJoinsTheTypeScriptImportKeyspace(t *testing.T) {
	r := NewRegistry()
	resolver, ok := r.ForLanguage("vue").(ImportPathResolver)
	if !ok {
		t.Fatal("vue does not resolve import paths")
	}
	got, err := resolver.ImportPath("/repo", "ui/Card.vue")
	if err != nil {
		t.Fatalf("ImportPath: %v", err)
	}

	// A TypeScript file importing "./Card.vue" must resolve to the same key.
	_, imports, err := r.ForLanguage("typescript").Parse("ui/app.ts", []byte("import Card from \"./Card.vue\";\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(imports) != 1 {
		t.Fatalf("imports = %v", imports)
	}
	if imports[0].ImportedPath != got {
		t.Errorf("importer sees %q but ImportPath yields %q; the graph would never connect",
			imports[0].ImportedPath, got)
	}
}

func TestPrismaSpec(t *testing.T) {
	src := "" +
		"// a comment mentioning model Ghost {\n" +
		"datasource db {\n" +
		"  provider = \"postgresql\"\n" +
		"}\n" +
		"\n" +
		"generator client {\n" +
		"  provider = \"prisma-client-js\"\n" +
		"}\n" +
		"\n" +
		"model User {\n" +
		"  id    Int    @id @default(autoincrement())\n" +
		"  posts Post[]\n" +
		"}\n" +
		"\n" +
		"enum Role {\n" +
		"  USER\n" +
		"  ADMIN\n" +
		"}\n"

	symbols, _ := parseWith(t, "prisma", "prisma/schema.prisma", src)
	got := kindsByName(symbols)

	for name, want := range map[string]SymbolKind{
		"User":   KindType,
		"Role":   KindType,
		"db":     KindVar,
		"client": KindVar,
	} {
		if got[name] != want {
			t.Errorf("%s: kind %q, want %q", name, got[name], want)
		}
	}
	if _, found := got["Ghost"]; found {
		t.Error("a model named in a comment was indexed")
	}
}

// TestPrismaDetectorUsesPathGlob covers the gate for a language that has no
// imports at all: the file path is the only signal a detector can use.
func TestPrismaDetectorUsesPathGlob(t *testing.T) {
	src := "model User {\n  id Int @id\n}\n\nview ActiveUser {\n  id Int\n}\n"
	found := detectIn(t, "prisma", "prisma/schema.prisma", src)
	wantNames(t, named(found, "prisma", FacetModel), []string{"User", "ActiveUser"})

	// A file that is not a schema must not be claimed.
	if other := detectIn(t, "prisma", "notes.txt", src); len(other) != 0 {
		t.Errorf("detected %v in a file the glob should not match", other)
	}
}

// TestSQLForeignKeysAreEdges covers the half of the import graph a schema can
// actually supply. A SQL file has no inbound key -- nothing imports it -- but
// a foreign key is a dependency, and recording it answers "what references
// users" even though the edge resolves to a table rather than a file.
func TestSQLForeignKeysAreEdges(t *testing.T) {
	src := "" +
		"CREATE TABLE orders (\n" +
		"  id SERIAL PRIMARY KEY,\n" +
		"  user_id INT REFERENCES users(id),\n" +
		"  FOREIGN KEY (shop_id) references shops (id)\n" +
		");\n" +
		"-- REFERENCES ghosts(id)\n"

	symbols, imports := parseWith(t, "sql", "schema.sql", src)
	if got := kindsByName(symbols)["orders"]; got != KindType {
		t.Errorf("orders not indexed: %v", symbols)
	}
	if want := []string{"users", "shops"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}

func TestPrismaRelationsAreEdges(t *testing.T) {
	src := "" +
		"model User {\n" +
		"  id    Int    @id\n" +
		"  name  String\n" +
		"  posts Post[]\n" +
		"}\n" +
		"\n" +
		"model Post {\n" +
		"  id       Int  @id\n" +
		"  author   User @relation(fields: [authorId], references: [id])\n" +
		"  authorId Int\n" +
		"}\n"

	symbols, imports := parseWith(t, "prisma", "schema.prisma", src)
	got := kindsByName(symbols)
	if got["User"] != KindType || got["Post"] != KindType {
		t.Errorf("models = %v", got)
	}
	// Scalar fields must not be mistaken for relations.
	if want := []string{"Post", "User"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v; a scalar field was read as a relation", importPaths(imports), want)
	}
}

// TestSQLRepeatedForeignKeyIsOneEdge pins the deduplication. Two foreign keys
// to the same table are one dependency, and counting both would overstate that
// table's place in the hub ranking.
func TestSQLRepeatedForeignKeyIsOneEdge(t *testing.T) {
	src := "" +
		"CREATE TABLE payments (\n" +
		"  order_id INT REFERENCES orders(id),\n" +
		"  payer_id INT REFERENCES users(id),\n" +
		"  payee_id INT REFERENCES users(id)\n" +
		");\n"

	_, imports := parseWith(t, "sql", "schema.sql", src)
	if want := []string{"orders", "users"}; !equalStringSlices(importPaths(imports), want) {
		t.Errorf("imports = %v, want %v", importPaths(imports), want)
	}
}
