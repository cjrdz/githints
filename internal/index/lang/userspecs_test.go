package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const rubyishSpec = `{
  "language": "rubyish",
  "extensions": [".rbx"],
  "depth_style": "none",
  "comments": { "line": ["#"] },
  "strings": [{ "open": "\"", "close": "\"", "escape": "\\" }],
  "symbols": [{ "kind": "func", "requires": "def", "pattern": "^\\s*def\\s+(?P<name>\\w+)" }],
  "imports": [{ "requires": "require", "pattern": "^\\s*require\\s+\"(?P<path>[^\"]+)\"" }]
}`

func writeUserSpec(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(UserSpecDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadUserSpecsMissingDirIsNotAnError(t *testing.T) {
	parsers, errs := LoadUserSpecs(t.TempDir())
	if len(parsers) != 0 || len(errs) != 0 {
		t.Errorf("parsers=%d errs=%v, want both empty for a repo with no specs", len(parsers), errs)
	}
}

func TestRepoSpecAddsLanguage(t *testing.T) {
	root := t.TempDir()
	writeUserSpec(t, root, "rubyish.json", rubyishSpec)

	r := NewRegistryForRoot(root)
	p := r.ForLanguage("rubyish")
	if p == nil {
		t.Fatal("repository spec was not registered")
	}
	if r.ForPath("a.rbx") != p {
		t.Error("extension does not resolve to the repository language")
	}
	if got := r.Origin("rubyish"); got != OriginRepo {
		t.Errorf("origin = %q, want %q", got, OriginRepo)
	}
	if got := r.Origin("go"); got != OriginBuiltin {
		t.Errorf("go origin = %q, want %q", got, OriginBuiltin)
	}

	symbols, imports, err := p.Parse("a.rbx", []byte("require \"set\"\n\ndef run\nend\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "run" {
		t.Errorf("symbols = %v", symbols)
	}
	if len(imports) != 1 || imports[0].ImportedPath != "set" {
		t.Errorf("imports = %v", imports)
	}
}

// TestRepoSpecCannotDisplaceBuiltin is the containment rule: a repository may
// add languages but never redefine one the binary already provides, so `go`
// means the same thing in every checkout.
func TestRepoSpecCannotDisplaceBuiltin(t *testing.T) {
	root := t.TempDir()
	writeUserSpec(t, root, "evil.json", `{
		"language":"go","extensions":[".go"],
		"symbols":[{"kind":"func","pattern":"^(?P<name>.*)$"}]
	}`)

	r := NewRegistryForRoot(root)
	if got := r.ForLanguage("go").Language(); got != "go" {
		t.Fatalf("language go resolves to %q", got)
	}
	if _, isSpec := r.ForPath("x.go").(*SpecParser); isSpec {
		t.Error("a repository spec replaced the native Go parser")
	}
	if got := r.Origin("go"); got != OriginBuiltin {
		t.Errorf("go origin = %q, want it still built-in", got)
	}
}

func TestRepoSpecCannotClaimBuiltinExtension(t *testing.T) {
	root := t.TempDir()
	writeUserSpec(t, root, "grab.json", `{
		"language":"grabby","extensions":[".py"],
		"symbols":[{"kind":"func","pattern":"^(?P<name>\\w+)"}]
	}`)

	r := NewRegistryForRoot(root)
	if r.ForPath("a.py").Language() != "python" {
		t.Error(".py was taken from the built-in python spec")
	}
	if r.ForLanguage("grabby") != nil {
		t.Error("a spec that lost its extension clash was registered anyway")
	}
}

// TestLoadUserSpecsReportsBadSpecs checks that a broken spec is reported and
// skipped rather than failing the scan. This runs from the commit hook, where
// a repository file must never be able to fail someone's commit.
func TestLoadUserSpecsReportsBadSpecs(t *testing.T) {
	root := t.TempDir()
	writeUserSpec(t, root, "broken.json", `{"language":"broken"}`)
	writeUserSpec(t, root, "good.json", rubyishSpec)

	parsers, errs := LoadUserSpecs(root)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one", errs)
	}
	if !strings.Contains(errs[0].Error(), "broken.json") {
		t.Errorf("error should name the file, got %v", errs[0])
	}
	if len(parsers) != 1 || parsers[0].Language() != "rubyish" {
		t.Errorf("a valid spec must still load alongside a broken one, got %v", parsers)
	}
}

func TestLoadUserSpecsEnforcesSizeCap(t *testing.T) {
	root := t.TempDir()
	padding := strings.Repeat(" ", MaxUserSpecBytes+1)
	writeUserSpec(t, root, "big.json", rubyishSpec+padding)

	parsers, errs := LoadUserSpecs(root)
	if len(parsers) != 0 {
		t.Error("an oversized spec was loaded")
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "over the limit") {
		t.Errorf("errs = %v, want a size-limit error", errs)
	}
}

func TestLoadUserSpecsEnforcesFileCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= MaxUserSpecFiles; i++ {
		body := strings.Replace(rubyishSpec, `"rubyish"`, fmt.Sprintf(`"lang%02d"`, i), 1)
		body = strings.Replace(body, `".rbx"`, fmt.Sprintf(`".x%02d"`, i), 1)
		writeUserSpec(t, root, fmt.Sprintf("s%02d.json", i), body)
	}

	parsers, errs := LoadUserSpecs(root)
	if len(parsers) > MaxUserSpecFiles {
		t.Errorf("loaded %d specs, over the cap of %d", len(parsers), MaxUserSpecFiles)
	}
	if len(errs) == 0 {
		t.Error("exceeding the file cap should be reported, not silent")
	}
}

// TestLoadUserSpecsRejectsSymlink stops a repository choosing which file on
// the machine gets read. ValidateFilePath-style lexical checks cannot see a
// symlink, so this is checked on the opened entry.
func TestLoadUserSpecsRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs Developer Mode on Windows")
	}
	root := t.TempDir()
	writeUserSpec(t, root, "real.json", rubyishSpec)

	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(rubyishSpec), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, filepath.FromSlash(UserSpecDir), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	_, errs := LoadUserSpecs(root)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "not a regular file") {
			found = true
		}
	}
	if !found {
		t.Errorf("symlinked spec was not rejected: %v", errs)
	}
}

func TestLoadUserSpecsIgnoresNonJSON(t *testing.T) {
	root := t.TempDir()
	writeUserSpec(t, root, "notes.txt", "this is not a spec")
	writeUserSpec(t, root, "good.json", rubyishSpec)

	parsers, errs := LoadUserSpecs(root)
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none; non-JSON files are not specs", errs)
	}
	if len(parsers) != 1 {
		t.Errorf("parsers = %d, want 1", len(parsers))
	}
}
