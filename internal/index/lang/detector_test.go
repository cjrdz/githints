package lang

import (
	"strings"
	"testing"
)

func TestEmbeddedDetectorsAreValid(t *testing.T) {
	set, errs := EmbeddedDetectors()
	for _, err := range errs {
		t.Errorf("embedded detector failed to load: %v", err)
	}
	if set.Empty() {
		t.Fatal("no detectors loaded; the go:embed pattern may have stopped matching")
	}
}

// djangoInput builds the two blanked views a detector sees, using the real
// Python spec so the test exercises the same blanking a scan would.
func djangoInput(t *testing.T, src string, imports ...string) DetectInput {
	t.Helper()
	p := NewRegistry().ForLanguage("python")
	bl, ok := p.(interface {
		BlankLines([]byte) []string
		BlankLinesKeepingStrings([]byte) []string
	})
	if !ok {
		t.Fatal("python parser exposes no blanked views")
	}
	in := DetectInput{
		FilePath: "app/models.py",
		Language: "python",
		Code:     bl.BlankLines([]byte(src)),
		Strings:  bl.BlankLinesKeepingStrings([]byte(src)),
	}
	for _, imp := range imports {
		in.Imports = append(in.Imports, Import{FilePath: in.FilePath, ImportedPath: imp})
	}
	return in
}

func facetNames(found []Detected, facet string) []string {
	var out []string
	for _, f := range found {
		if f.Facet == facet {
			out = append(out, f.Name)
		}
	}
	return out
}

func TestDjangoDetectorFindsModelsAndRoutes(t *testing.T) {
	set, _ := EmbeddedDetectors()

	src := "" +
		"from django.db import models\n" +
		"from django.urls import path\n" +
		"\n" +
		"class Article(models.Model):\n" +
		"    title = models.CharField(max_length=100)\n" +
		"\n" +
		"class Plain:\n" +
		"    pass\n" +
		"\n" +
		"urlpatterns = [\n" +
		"    path(\"articles/\", views.index),\n" +
		"    path(\"articles/<int:pk>/\", views.detail),\n" +
		"]\n"

	found := set.Detect(djangoInput(t, src, "django.db", "django.urls"))

	if got := facetNames(found, FacetModel); len(got) != 1 || got[0] != "Article" {
		t.Errorf("models = %v, want [Article]; Plain is not a model", got)
	}
	routes := facetNames(found, FacetRoute)
	if len(routes) != 2 || routes[0] != "articles/" || routes[1] != "articles/<int:pk>/" {
		t.Errorf("routes = %v", routes)
	}
	for _, f := range found {
		if f.Framework != "django" {
			t.Errorf("framework = %q", f.Framework)
		}
		if f.Line <= 0 {
			t.Errorf("%s has no line number", f.Name)
		}
	}
	// The view is captured as detail, which is what makes a route listing
	// useful rather than just a list of URLs.
	for _, f := range found {
		if f.Facet == FacetRoute && f.Name == "articles/" && f.Detail != "views.index" {
			t.Errorf("detail = %q, want views.index", f.Detail)
		}
	}
}

// TestDetectorImportGate is the rule that keeps attribution honest. `class
// X(models.Model)` is the shape of every Python ORM; without the gate this
// detector would claim SQLAlchemy and Peewee code as Django.
func TestDetectorImportGate(t *testing.T) {
	set, _ := EmbeddedDetectors()

	src := "" +
		"from someother.orm import models\n" +
		"\n" +
		"class Article(models.Model):\n" +
		"    pass\n"

	if found := set.Detect(djangoInput(t, src, "someother.orm")); len(found) != 0 {
		t.Errorf("detected %v in a file that does not import django", found)
	}
}

// TestDetectorIgnoresCommentsAndDocstrings is why detection runs on blanked
// views. A model quoted in prose is not a model.
func TestDetectorIgnoresCommentsAndDocstrings(t *testing.T) {
	set, _ := EmbeddedDetectors()

	src := "" +
		"from django.db import models\n" +
		"\n" +
		"# class Ghost(models.Model):\n" +
		"\n" +
		"HELP = \"\"\"\n" +
		"class Phantom(models.Model):\n" +
		"\"\"\"\n" +
		"\n" +
		"class Real(models.Model):\n" +
		"    pass\n"

	found := set.Detect(djangoInput(t, src, "django.db"))
	if got := facetNames(found, FacetModel); len(got) != 1 || got[0] != "Real" {
		t.Errorf("models = %v, want only [Real]", got)
	}
}

func TestLoadDetectorRejects(t *testing.T) {
	for _, tc := range []struct{ name, json, want string }{
		{"noFramework", `{"when_imports":["x"],"rules":[{"facet":"route","pattern":"(?P<name>a)"}]}`, "no framework name"},
		{"noGate", `{"framework":"f","rules":[{"facet":"route","pattern":"(?P<name>a)"}]}`, "would claim every file"},
		{"noRules", `{"framework":"f","when_imports":["x"]}`, "no rules"},
		{"unknownFacet", `{"framework":"f","when_imports":["x"],"rules":[{"facet":"widget","pattern":"(?P<name>a)"}]}`, "unknown facet"},
		{"missingNameGroup", `{"framework":"f","when_imports":["x"],"rules":[{"facet":"route","pattern":"abc"}]}`, "must capture"},
		{"badPattern", `{"framework":"f","when_imports":["x"],"rules":[{"facet":"route","pattern":"(?P<name>["}]}`, "does not compile"},
		{"frameworkWithComma", `{"framework":"a,b","when_imports":["x"],"rules":[{"facet":"route","pattern":"(?P<name>a)"}]}`, "must not contain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadDetector([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestImportMatches(t *testing.T) {
	for _, tc := range []struct {
		imported, pattern string
		want              bool
	}{
		{"django.db", "django.db", true},
		{"django.db.models", "django.db", true},    // submodule
		{"django.urls", "django.*", true},          // prefix wildcard
		{"djangorestframework", "django.*", false}, // not a submodule
		{"github.com/go-chi/chi/v5", "github.com/go-chi/chi", true},
		{"other.db", "django.db", false},
		{"tokio::task", "tokio", true},                                          // Rust
		{"Illuminate\\Database\\Eloquent\\Model", "Illuminate\\Database", true}, // PHP
		{"tokiox::task", "tokio", false},
	} {
		if got := importMatches(tc.imported, tc.pattern); got != tc.want {
			t.Errorf("importMatches(%q, %q) = %v, want %v", tc.imported, tc.pattern, got, tc.want)
		}
	}
}

// TestKeepStringsRequiresAGate pins the rule that makes keep_strings safe.
// The gate is evaluated against the code view, so it is what confines such a
// rule to real code; without one it would match its own shape inside any
// string literal in the file.
func TestKeepStringsRequiresAGate(t *testing.T) {
	_, err := LoadDetector([]byte(`{
		"framework":"f","when_imports":["x"],
		"rules":[{"facet":"route","keep_strings":true,"pattern":"\"(?P<name>[^\"]+)\""}]
	}`))
	if err == nil {
		t.Fatal("a keep_strings rule with no requires should be rejected")
	}
	if !strings.Contains(err.Error(), "must set requires") {
		t.Errorf("error = %q", err)
	}
}
