package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParsePSR4(t *testing.T) {
	got := parsePSR4([]byte(`{
		"autoload": {"psr-4": {"App\\": "app/", "Lib\\": ["src/", "./lib"], "Up\\": "../outside/"}},
		"autoload-dev": {"psr-4": {"Tests\\": "tests"}}
	}`))
	want := []psr4Mapping{{"App", "app"}, {"Lib", "lib"}, {"Lib", "src"}, {"Tests", "tests"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := parsePSR4([]byte(`{"autoload":`)); got != nil {
		t.Errorf("broken composer.json = %+v, want nothing", got)
	}
}

func TestPSR4ResolveLongestDirectoryWins(t *testing.T) {
	m := []psr4Mapping{{"App", "app"}, {"App\\Domain", "app/Domain"}, {"", ""}}
	for rel, want := range map[string]string{
		"app/Models/User.php":   `App\Models\User`,
		"app/Domain/Order.php":  `App\Domain\Order`,
		"helpers/functions.php": `helpers\functions`, // the "" fallback
	} {
		if got, ok := psr4Resolve(m, rel); !ok || got != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, ok, want)
		}
	}
	if _, ok := psr4Resolve([]psr4Mapping{{"App", "app"}}, "database/seeders/X.php"); ok {
		t.Error("a file outside every mapping resolved")
	}
}

// TestPSR4KeyFindsTheMappingComposer: a nested package's composer.json that
// does not map a file gives way to the one above, and an edit to
// composer.json is seen by a long-running process.
func TestPSR4KeyFindsTheMappingComposer(t *testing.T) {
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
	write("composer.json", `{"autoload":{"psr-4":{"App\\":"app/"}}}`)
	write("packages/billing/composer.json", `{"autoload":{"psr-4":{"Acme\\Billing\\":"src/"}}}`)

	for file, want := range map[string]string{
		"app/Models/User.php":               `App\Models\User`,
		"packages/billing/src/Invoice.php":  `Acme\Billing\Invoice`,
		"packages/billing/src/Tax/Rate.php": `Acme\Billing\Tax\Rate`,
	} {
		if got, err := psr4Key(root, file); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	if _, err := psr4Key(root, "packages/billing/tests/InvoiceTest.php"); err == nil {
		t.Error("a file no composer.json maps got a key")
	}
	if _, err := psr4Key(root, "app/view.blade"); err == nil {
		t.Error("a non-.php file got a key")
	}

	// A longer value and a later modification time: the cache must notice.
	write("composer.json", `{"autoload":{"psr-4":{"Shop\\":"app/"}}}`)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "composer.json"), future, future); err != nil {
		t.Fatal(err)
	}
	if got, _ := psr4Key(root, "app/Models/User.php"); got != `Shop\Models\User` {
		t.Errorf("after editing composer.json: %q, want Shop\\Models\\User", got)
	}
}

func TestPSR4Members(t *testing.T) {
	got := psr4Members(`App\Models`, " User, Post as P,\\Tag ,, {bad}")
	want := []string{`App\Models\User`, `App\Models\Post`, `App\Models\Tag`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPHPImportsAndAttributes: grouped and aliased use statements are
// imports, a trait's use inside a class is not, "#[" is code while "#" is
// still a comment, and a PHP file resolves through composer.json.
func TestPHPImportsAndAttributes(t *testing.T) {
	src := "" +
		"<?php\n" +
		"namespace App\\Http\\Controllers;\n" +
		"use \\App\\Models\\User as U;\n" +
		"use App\\Http\\Requests\\{StoreRequest, UpdateRequest as Upd};\n" +
		"use function App\\Support\\helper;\n" +
		"# use App\\Commented\\Out;\n" +
		"#[Route('/x')]\n" +
		"class C {\n" +
		"    use HasTrait;\n" +
		"}\n"
	_, imports := parseWith(t, "php", "app/Http/Controllers/C.php", src)
	want := []string{`App\Models\User`, `App\Http\Requests\StoreRequest`, `App\Http\Requests\UpdateRequest`, `App\Support\helper`}
	if got := importPaths(imports); !reflect.DeepEqual(got, want) {
		t.Errorf("imports = %q, want %q", got, want)
	}
	p := NewRegistry().ForLanguage("php").(*SpecParser)
	code := p.BlankLines([]byte(src))
	if code[6] != "#[Route('')]" {
		t.Errorf("attribute line = %q, want it kept as code", code[6])
	}
	if code[5] != "" {
		t.Errorf("comment line = %q, want it blanked", code[5])
	}
}

func TestNotLineValidation(t *testing.T) {
	spec := Spec{Language: "x", Extensions: []string{".x"}, DepthStyle: DepthNone,
		Comments: SpecComments{Line: []string{"#"}, NotLine: []string{"//"}},
		Symbols:  []SpecRule{{Kind: KindFunc, Pattern: `(?P<name>x)`}}}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "not_line") {
		t.Errorf("a not_line entry that extends no line comment: %v", err)
	}
	spec.Comments.NotLine = []string{"#"}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "not_line") {
		t.Errorf("a not_line entry equal to the comment itself: %v", err)
	}
	spec.Comments.NotLine = []string{"#["}
	if err := spec.Validate(); err != nil {
		t.Errorf("#[ rejected: %v", err)
	}
}
