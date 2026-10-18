package lang

import (
	"reflect"
	"testing"
)

// TestCSSSpec: selectors by their first class or id, a selector list split
// over lines, custom properties, @property and @keyframes; and not a value,
// a comment, or a nested declaration that looks like one.
func TestCSSSpec(t *testing.T) {
	src := "" +
		"@import url(\"base.css\");\n" + // 1
		"/* .commented { } */\n" +
		":root {\n" +
		"  --brand: #0af;\n" +
		"  margin:\n" + // 5
		"    .5em 1em;\n" +
		"}\n" +
		".btn-primary:hover, .btn-primary:focus {\n" +
		"  color: red;\n" +
		"}\n" + // 10
		".a,\n" +
		".b {\n" +
		"}\n" +
		"#app > main { display: grid; }\n" +
		"@property --angle {\n" + // 15
		"  syntax: '<angle>';\n" +
		"}\n" +
		"@keyframes spin {\n" +
		"  to { transform: rotate(1turn); }\n" +
		"}\n" // 20
	symbols, imports := parseWith(t, "css", "src/app.css", src)
	want := []string{"var --brand 4-4", "type .btn-primary 8-10", "type .a 11-11", "type .b 12-13", "type #app 14-14", "var --angle 15-17", "const spin 18-20"}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
	if got := importPaths(imports); !reflect.DeepEqual(got, []string{"base.css"}) {
		t.Errorf("imports %q", got)
	}
}

func TestSCSSSpec(t *testing.T) {
	src := "" +
		"@use \"sass:math\";\n" + // 1
		"@forward \"src/list\" hide list-reset;\n" +
		"$gutter: 1rem !default;\n" +
		"// $commented: 1;\n" +
		"@mixin respond($bp) {\n" + // 5
		"  $local: 1;\n" +
		"  @media (min-width: $bp) { @content; }\n" +
		"}\n" +
		"@function rem($px) {\n" +
		"  @return math.div($px, 16px) * 1rem;\n" + // 10
		"}\n" +
		"%hidden { position: absolute; }\n" +
		".card {\n" +
		"  &.is-active { outline: 0; }\n" +
		"  &__title { margin: 0; }\n" + // 15
		"}\n"
	symbols, imports := parseWith(t, "scss", "src/_card.scss", src)
	want := []string{"var $gutter 3-3", "func respond 5-8", "func rem 9-11", "type %hidden 12-12", "type .card 13-16", "type .is-active 14-14"}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
	if got := importPaths(imports); !reflect.DeepEqual(got, []string{"sass:math", "src/list"}) {
		t.Errorf("imports %q", got)
	}
}

func TestLessSpec(t *testing.T) {
	src := "" +
		"@import (reference) \"mixins\";\n" + // 1
		"@brand: #337ab7;\n" +
		"@media (min-width: 768px) {\n" +
		"  .nav { float: left; }\n" +
		"}\n" + // 5
		".rounded(@r: 4px) {\n" +
		"  border-radius: @r;\n" +
		"}\n" +
		".box { .rounded(2px); }\n"
	symbols, imports := parseWith(t, "less", "less/theme.less", src)
	want := []string{"var @brand 2-2", "type .nav 4-4", "func .rounded 6-8", "type .box 9-9"}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
	if got := importPaths(imports); !reflect.DeepEqual(got, []string{"mixins"}) {
		t.Errorf("imports %q", got)
	}
}

// TestResolveStylesheet: Sass's lookup order (as written, with an extension,
// a partial, a directory's index), a relative path upward, packages and
// built-ins kept as written without webpack's ~, and URLs dropped.
func TestResolveStylesheet(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/scss/abstracts/_variables.scss": "",
		"src/scss/components/_index.scss":    "",
		"src/scss/base.scss":                 "",
		"src/scss/vendor/reset.css":          "",
		"src/styles/fonts.css":               "",
		"less/mixins.less":                   "",
	})
	defer beginSpecScan(root)()
	scss := []string{".scss"}
	for _, c := range []struct {
		file, spec string
		exts       []string
		want       string
	}{
		{"src/scss/main.scss", "abstracts/variables", scss, "src/scss/abstracts/_variables.scss"},
		{"src/scss/main.scss", "components", scss, "src/scss/components/_index.scss"},
		{"src/scss/main.scss", "base", scss, "src/scss/base.scss"},
		{"src/scss/main.scss", "vendor/reset", scss, "src/scss/vendor/reset.css"},
		{"src/scss/components/_card.scss", "../abstracts/variables", scss, "src/scss/abstracts/_variables.scss"},
		{"src/app.css", "./styles/fonts.css", []string{".css"}, "src/styles/fonts.css"},
		{"less/theme.less", "mixins", []string{".less"}, "less/mixins.less"},
		{"src/scss/main.scss", "~bootstrap/scss/bootstrap", scss, "bootstrap/scss/bootstrap"},
		{"src/scss/main.scss", "sass:math", scss, "sass:math"},
		{"src/app.css", "tailwindcss", []string{".css"}, "tailwindcss"},
		{"src/app.css", "../../../etc/passwd", []string{".css"}, "../../../etc/passwd"},
		{"src/app.css", "https://fonts.googleapis.com/css2?family=Inter", []string{".css"}, ""},
		{"src/app.css", "//cdn.example.com/x.css", []string{".css"}, ""},
	} {
		if got := resolveStylesheet(root, c.file, c.spec, c.exts); got != c.want {
			t.Errorf("%s imports %q = %q, want %q", c.file, c.spec, got, c.want)
		}
	}
}
