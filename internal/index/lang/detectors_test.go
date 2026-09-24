package lang

import (
	"testing"
)

// detectIn runs the shipped detectors over source, using the real parser for
// the language so blanking matches what a scan would do.
func detectIn(t *testing.T, language, file, src string, imports ...string) []Detected {
	t.Helper()
	p := NewRegistry().ForLanguage(language)
	if p == nil {
		t.Fatalf("language %s is not registered", language)
	}
	bl, ok := p.(interface {
		BlankLines([]byte) []string
		BlankLinesKeepingStrings([]byte) []string
	})
	if !ok {
		t.Fatalf("%s exposes no blanked views", language)
	}
	in := DetectInput{
		FilePath: file,
		Language: language,
		Code:     bl.BlankLines([]byte(src)),
		Strings:  bl.BlankLinesKeepingStrings([]byte(src)),
	}
	for _, imp := range imports {
		in.Imports = append(in.Imports, Import{FilePath: file, ImportedPath: imp})
	}
	set, _ := EmbeddedDetectors()
	return set.Detect(in)
}

// named collects the names found for one facet and framework.
func named(found []Detected, framework, facet string) []string {
	var out []string
	for _, f := range found {
		if f.Framework == framework && f.Facet == facet {
			out = append(out, f.Name)
		}
	}
	return out
}

func wantNames(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestChiDetector(t *testing.T) {
	src := "" +
		"package api\n\n" +
		"import \"github.com/go-chi/chi/v5\"\n\n" +
		"func Routes(r chi.Router) {\n" +
		"\tr.Get(\"/users\", listUsers)\n" +
		"\tr.Post(\"/users\", createUser)\n" +
		"\tr.Route(\"/admin\", adminRoutes)\n" +
		"\t// r.Delete(\"/ghost\", nope)\n" +
		"\ts := \"r.Put(\\\"/fake\\\", x)\"\n" +
		"}\n"

	found := detectIn(t, "go", "api/routes.go", src, "github.com/go-chi/chi/v5")
	wantNames(t, named(found, "chi", FacetRoute), []string{"/users", "/users", "/admin"})

	for _, f := range found {
		if f.Name == "/users" && f.Detail == "" {
			t.Error("the HTTP method should be captured as detail")
		}
	}
}

func TestGormDetector(t *testing.T) {
	src := "" +
		"package models\n\n" +
		"import \"gorm.io/gorm\"\n\n" +
		"type User struct{ ID uint }\n\n" +
		"func (u *User) TableName() string { return \"users\" }\n\n" +
		"func Migrate(db *gorm.DB) error {\n" +
		"\treturn db.AutoMigrate(&User{}, &Post{})\n" +
		"}\n"

	found := detectIn(t, "go", "models/user.go", src, "gorm.io/gorm")
	// AutoMigrate names both models on one line, which is why the matcher
	// takes every match per line rather than the first. User is named twice --
	// by TableName and by AutoMigrate -- and reported once.
	wantNames(t, named(found, "gorm", FacetModel), []string{"User", "Post"})
}

func TestBunDetector(t *testing.T) {
	src := "package models\n\n" +
		"import \"github.com/uptrace/bun\"\n\n" +
		"type User struct {\n" +
		"\tbun.BaseModel `bun:\"table:users,alias:u\"`\n" +
		"\tID int64\n" +
		"}\n"

	found := detectIn(t, "go", "models/user.go", src, "github.com/uptrace/bun")
	wantNames(t, named(found, "bun", FacetModel), []string{"users"})
}

func TestFlaskDetector(t *testing.T) {
	src := "" +
		"from flask import Flask\n\n" +
		"app = Flask(__name__)\n\n" +
		"@app.route(\"/health\")\n" +
		"def health():\n" +
		"    return \"ok\"\n\n" +
		"@app.post(\"/items\")\n" +
		"def create():\n" +
		"    pass\n"

	found := detectIn(t, "python", "app.py", src, "flask")
	wantNames(t, named(found, "flask", FacetRoute), []string{"/health", "/items"})
}

func TestFastAPIDetector(t *testing.T) {
	src := "" +
		"from fastapi import FastAPI\n\n" +
		"app = FastAPI()\n\n" +
		"@app.get(\"/users/{uid}\")\n" +
		"async def read_user(uid: int):\n" +
		"    pass\n\n" +
		"@router.delete(\"/users/{uid}\")\n" +
		"async def delete_user(uid: int):\n" +
		"    pass\n"

	found := detectIn(t, "python", "main.py", src, "fastapi")
	wantNames(t, named(found, "fastapi", FacetRoute), []string{"/users/{uid}", "/users/{uid}"})
	for _, f := range found {
		if f.Detail != "get" && f.Detail != "delete" {
			t.Errorf("method not captured: %+v", f)
		}
	}
}

// TestFlaskAndFastAPIAreToldApartByImports is the point of the gate: the two
// frameworks share a decorator shape, so only the import distinguishes them.
func TestFlaskAndFastAPIAreToldApartByImports(t *testing.T) {
	src := "@app.get(\"/x\")\ndef handler():\n    pass\n"

	flask := detectIn(t, "python", "a.py", src, "flask")
	if got := named(flask, "flask", FacetRoute); len(got) != 1 {
		t.Errorf("flask routes = %v", got)
	}
	if got := named(flask, "fastapi", FacetRoute); len(got) != 0 {
		t.Errorf("fastapi claimed a flask file: %v", got)
	}

	fast := detectIn(t, "python", "a.py", src, "fastapi")
	if got := named(fast, "fastapi", FacetRoute); len(got) != 1 {
		t.Errorf("fastapi routes = %v", got)
	}
	if got := named(fast, "flask", FacetRoute); len(got) != 0 {
		t.Errorf("flask claimed a fastapi file: %v", got)
	}
}

func TestSQLAlchemyDetector(t *testing.T) {
	src := "" +
		"from sqlalchemy.orm import DeclarativeBase\n\n" +
		"class Base(DeclarativeBase):\n" +
		"    pass\n\n" +
		"class User(Base):\n" +
		"    __tablename__ = \"users\"\n\n" +
		"class NotAModel:\n" +
		"    pass\n"

	found := detectIn(t, "python", "models.py", src, "sqlalchemy.orm")
	got := named(found, "sqlalchemy", FacetModel)
	// User matches the base-class rule and the __tablename__ rule; NotAModel
	// matches neither.
	if len(got) != 2 {
		t.Fatalf("models = %v, want User and its table name", got)
	}
	for _, name := range got {
		if name == "NotAModel" {
			t.Errorf("a plain class was reported as a model: %v", got)
		}
	}
}

func TestReactDetector(t *testing.T) {
	src := "" +
		"import React from \"react\";\n\n" +
		"export function Button(props) {\n" +
		"  return <button />;\n" +
		"}\n\n" +
		"export const Card = ({ title }) => <div>{title}</div>;\n\n" +
		"function helper() { return 1; }\n\n" +
		"const value = 3;\n"

	found := detectIn(t, "typescript", "ui.tsx", src, "react")
	// Capitalisation is the only signal React itself gives, so helper and
	// value must not be reported.
	wantNames(t, named(found, "react", FacetComponent), []string{"Button", "Card"})
}

func TestVueDetector(t *testing.T) {
	src := "" +
		"import { defineComponent } from \"vue\";\n\n" +
		"export const Widget = defineComponent({\n" +
		"  name: \"Widget\",\n" +
		"});\n"

	found := detectIn(t, "typescript", "widget.ts", src, "vue")
	got := named(found, "vue", FacetComponent)
	if len(got) == 0 {
		t.Fatalf("no Vue component detected")
	}
	for _, name := range got {
		if name != "Widget" {
			t.Errorf("component = %q, want Widget", name)
		}
	}
}

// TestDetectorsIgnoreCommentsAndStrings checks the blanking guarantee holds
// for every shipped detector, not just the one it was written against.
func TestDetectorsIgnoreCommentsAndStrings(t *testing.T) {
	goSrc := "package api\n\n" +
		"import \"github.com/go-chi/chi/v5\"\n\n" +
		"// r.Get(\"/commented\", h)\n" +
		"var doc = `r.Get(\"/in-raw-string\", h)`\n"
	for _, f := range detectIn(t, "go", "a.go", goSrc, "github.com/go-chi/chi/v5") {
		t.Errorf("detected %q from a comment or string literal", f.Name)
	}

	pySrc := "from flask import Flask\n\n" +
		"# @app.route(\"/commented\")\n" +
		"DOC = \"\"\"\n@app.route(\"/in-docstring\")\n\"\"\"\n"
	for _, f := range detectIn(t, "python", "a.py", pySrc, "flask") {
		t.Errorf("detected %q from a comment or docstring", f.Name)
	}
}

// TestDetectorDeduplicatesWithinAFile covers two rules describing the same
// thing. A GORM model is named both by its TableName method and by its
// AutoMigrate registration, and a listing should show it once.
func TestDetectorDeduplicatesWithinAFile(t *testing.T) {
	src := "package models\n\n" +
		"import \"gorm.io/gorm\"\n\n" +
		"func (u *User) TableName() string { return \"users\" }\n\n" +
		"func Migrate(db *gorm.DB) error { return db.AutoMigrate(&User{}) }\n"

	found := detectIn(t, "go", "models/user.go", src, "gorm.io/gorm")
	wantNames(t, named(found, "gorm", FacetModel), []string{"User"})
}

// TestDetectorKeepsDistinctDetails is the other half: two routes sharing a
// path but differing in method are different routes, so detail is part of the
// deduplication key.
func TestDetectorKeepsDistinctDetails(t *testing.T) {
	src := "package api\n\n" +
		"import \"github.com/go-chi/chi/v5\"\n\n" +
		"func Mount(r chi.Router) {\n" +
		"\tr.Get(\"/users\", list)\n" +
		"\tr.Post(\"/users\", create)\n" +
		"}\n"

	found := detectIn(t, "go", "api/r.go", src, "github.com/go-chi/chi/v5")
	if got := named(found, "chi", FacetRoute); len(got) != 2 {
		t.Errorf("routes = %v, want GET and POST kept separate", got)
	}
}

func TestSpringDetector(t *testing.T) {
	src := "" +
		"package com.example;\n\n" +
		"import org.springframework.web.bind.annotation.GetMapping;\n\n" +
		"@RestController\n" +
		"@RequestMapping(\"/api\")\n" +
		"public class UserController {\n" +
		"    @GetMapping(\"/users\")\n" +
		"    public List<User> list() { return null; }\n" +
		"}\n"

	found := detectIn(t, "java", "src/main/java/com/example/UserController.java", src,
		"org.springframework.web.bind.annotation.GetMapping")
	// Ordered by line: @RequestMapping on the class precedes @GetMapping on
	// the method.
	wantNames(t, named(found, "spring", FacetRoute), []string{"/api", "/users"})
}

func TestEloquentDetector(t *testing.T) {
	src := "" +
		"<?php\n\n" +
		"use Illuminate\\Database\\Eloquent\\Model;\n" +
		"use Illuminate\\Support\\Facades\\Route;\n\n" +
		"class User extends Model {}\n\n" +
		"Route::get('/users', [UserController::class, 'index']);\n" +
		"Route::post('/users', [UserController::class, 'store']);\n"

	found := detectIn(t, "php", "app/Models/User.php", src,
		`Illuminate\Database\Eloquent\Model`, `Illuminate\Support\Facades\Route`)
	wantNames(t, named(found, "eloquent", FacetModel), []string{"User"})
	wantNames(t, named(found, "eloquent", FacetRoute), []string{"/users", "/users"})
}

func TestEntityFrameworkDetector(t *testing.T) {
	src := "" +
		"using Microsoft.EntityFrameworkCore;\n\n" +
		"public class AppContext : DbContext\n" +
		"{\n" +
		"    public DbSet<User> Users { get; set; }\n" +
		"    public DbSet<Order> Orders { get; set; }\n" +
		"}\n"

	found := detectIn(t, "csharp", "Data/AppContext.cs", src, "Microsoft.EntityFrameworkCore")
	wantNames(t, named(found, "entityframework", FacetModel), []string{"User", "Order"})
}

func TestTokioDetector(t *testing.T) {
	src := "" +
		"use tokio::task;\n\n" +
		"#[tokio::main]\n" +
		"async fn main() {\n" +
		"    tokio::spawn(async { work().await });\n" +
		"}\n"

	found := detectIn(t, "rust", "src/main.rs", src, "tokio::task")
	got := named(found, "tokio", FacetJob)
	if len(got) != 2 {
		t.Errorf("tokio jobs = %v, want the entry point and the spawn", got)
	}
}

// TestEveryDetectorHasAWorkingLanguage guards the sequencing problem that held
// these four back: detection needs a parser to produce blanked views and
// imports, so a detector naming a language githints cannot read is inert.
func TestEveryDetectorHasAWorkingLanguage(t *testing.T) {
	set, _ := EmbeddedDetectors()
	r := NewRegistry()
	for _, d := range set.detectors {
		for language := range d.languages {
			p := r.ForLanguage(language)
			if p == nil {
				t.Errorf("detector %s targets %q, which is not a registered language", d.framework, language)
				continue
			}
			if _, ok := p.(interface {
				BlankLines([]byte) []string
				BlankLinesKeepingStrings([]byte) []string
			}); !ok {
				t.Errorf("detector %s targets %s, which exposes no blanked views, so it can never match",
					d.framework, language)
			}
		}
	}
}
