package risk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/store"
)

// fixture is a Go module with an index in which internal/core is imported by
// fanIn files and cmd/app.go is an HTTP route, plus an empty store.
func fixture(t *testing.T, fanIn int) Inputs {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := index.Open(filepath.Join(dir, ".githints", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var imps []lang.Import
	for i := range fanIn {
		imps = append(imps, lang.Import{FilePath: fmt.Sprintf("pkg%d/x.go", i), ImportedPath: "example.com/m/internal/core"})
	}
	if err := db.InsertImports(imps); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFacets([]lang.Detected{
		{FilePath: "cmd/app.go", Facet: "route", Framework: "nethttp", Name: "/users", Line: 3},
		{FilePath: "deploy.go", Facet: "infra", Framework: "docker", Name: "x", Line: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(lang.IndexMeta{LastIndexedAt: 1}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return Inputs{Store: st, Index: db, Root: dir}
}

func TestAssessLevels(t *testing.T) {
	in := fixture(t, FanInHigh)
	for name, tc := range map[string]struct {
		paths []string
		want  Level
	}{
		"readme":                  {[]string{"README.md"}, Low},
		"long docs":               {[]string{"docs/guide.md", "docs/more.rst"}, Low},
		"doc about auth":          {[]string{"docs/auth.md"}, Low},
		"test file":               {[]string{"pkg/x_test.go"}, Low},
		"plain code":              {[]string{"pkg/util.go"}, Medium},
		"one line of auth":        {[]string{"internal/auth/token.go"}, High},
		"auth test":               {[]string{"internal/auth/token_test.go"}, High},
		"tokenizer is not auth":   {[]string{"pkg/tokenizer.go"}, Medium},
		"workflow":                {[]string{".github/workflows/ci.yml"}, High},
		"dockerfile":              {[]string{"Dockerfile"}, High},
		"migration":               {[]string{"db/migrate/001_init.sql"}, High},
		"lockfile":                {[]string{"go.sum"}, High},
		"githints config":         {[]string{".githints/config.json"}, High},
		"route facet":             {[]string{"cmd/app.go"}, Medium},
		"infra facet":             {[]string{"deploy.go"}, High},
		"fan-in":                  {[]string{"internal/core/core.go"}, High},
		"readme with auth change": {[]string{"README.md", "internal/auth/token.go"}, High},
	} {
		t.Run(name, func(t *testing.T) {
			rep, err := Assess(in, tc.paths)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Level != tc.want {
				t.Errorf("level = %s, want %s\n%s", rep.Level, tc.want, rep.Format())
			}
		})
	}
}

func TestFanInBelowThresholdIsNotHigh(t *testing.T) {
	in := fixture(t, FanInHigh-1)
	rep, err := Assess(in, []string{"internal/core/core.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Level != Medium {
		t.Errorf("level = %s with %d importers, want medium", rep.Level, FanInHigh-1)
	}
}

// A file whose recent change failed is one level riskier than its path says.
func TestFailedHistoryRaisesOneLevel(t *testing.T) {
	in := fixture(t, 0)
	id, err := in.Store.Insert(store.Change{FilePath: "README.md", Source: "agent", Summary: "x", RecordedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Store.Insert(store.Change{Kind: store.KindOutcome, TargetID: id, OutcomeState: store.OutcomeReverted,
		Source: "agent", Summary: "broke the docs build", RecordedAt: 2}); err != nil {
		t.Fatal(err)
	}
	rep, err := Assess(in, []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Level != Medium || !strings.Contains(rep.Format(), "reverted") {
		t.Errorf("level = %s, want medium with the reverted change named:\n%s", rep.Level, rep.Format())
	}
}

func TestMissingCoChangePartnerIsReadFirst(t *testing.T) {
	in := fixture(t, 0)
	for i := range coChangeMin {
		hash := fmt.Sprintf("%040d", i+1)
		for _, f := range []string{"api/handler.go", "api/schema.go"} {
			if _, err := in.Store.Insert(store.Change{FilePath: f, CommitHash: hash, Source: "agent", Summary: "x", RecordedAt: int64(i + 1)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rep, err := Assess(in, []string{"api/handler.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ReadFirst) != 1 || rep.ReadFirst[0] != "api/schema.go" {
		t.Errorf("ReadFirst = %v, want [api/schema.go]", rep.ReadFirst)
	}
	rep, err = Assess(in, []string{"api/handler.go", "api/schema.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ReadFirst) != 0 {
		t.Errorf("a partner already in the change was listed: %v", rep.ReadFirst)
	}
}

// config.json arrives in clones: its rules may raise a level, never lower
// one, and a rule this binary does not understand is skipped, not fatal.
func TestRepositoryRulesOnlyRaise(t *testing.T) {
	in := fixture(t, 0)
	in.Config = config.Review{Rules: []config.ReviewRule{
		{Match: "billing/**", Class: "security"},
		{Match: "internal/auth/**", Class: "docs"},
		{Match: "pkg/**", Class: "docs"},
		{Match: "*.go", Class: "quantum"},
		{Match: "[", Class: "docs"},
	}}
	for p, want := range map[string]Level{
		"billing/invoice.go":     High,
		"internal/auth/token.go": High,
		"pkg/util.go":            Medium,
	} {
		rep, err := Assess(in, []string{p})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Level != want {
			t.Errorf("%s: level = %s, want %s", p, rep.Level, want)
		}
		if len(rep.Warnings) != 2 {
			t.Errorf("warnings = %v, want the unknown class and the bad pattern", rep.Warnings)
		}
	}
}

func TestTooManyPathsIsAtLeastMedium(t *testing.T) {
	in := fixture(t, 0)
	paths := make([]string, MaxPaths+1)
	for i := range paths {
		paths[i] = fmt.Sprintf("docs/p%d.md", i)
	}
	rep, err := Assess(in, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Truncated || rep.Level != Medium || rep.Files != MaxPaths {
		t.Errorf("truncated=%v level=%s files=%d, want true, medium, %d", rep.Truncated, rep.Level, rep.Files, MaxPaths)
	}
}

func TestWithoutIndexPathRulesStillApply(t *testing.T) {
	in := fixture(t, 0)
	in.Index = nil
	rep, err := Assess(in, []string{"internal/auth/token.go"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Level != High || !strings.Contains(rep.Format(), "index unavailable") {
		t.Errorf("level = %s, want high and a note that the index was not used:\n%s", rep.Level, rep.Format())
	}
}

// Every built-in rule must be a pattern Match can use, and name a class.
func TestBuiltinRulesAreValid(t *testing.T) {
	for _, r := range builtinRules {
		if err := CheckRule(config.ReviewRule{Match: r.match, Class: r.class.name}); err != nil {
			t.Errorf("built-in rule %q: %v", r.match, err)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"docs/**", "docs/a.md", true},
		{"docs/**", "pkg/docs/a.md", true},
		{"docs/**", "mydocs/a.md", false},
		{"*.md", "a/b/README.md", true},
		{"cmd/*.go", "cmd/app.go", true},
		{"cmd/*.go", "x/cmd/app.go", false},
		{"Dockerfile*", "build/Dockerfile.dev", true},
	} {
		if got := Match(tc.pattern, tc.path); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// Recorded paths reach the report; it must not print a control character.
func TestFormatNeutralizesPaths(t *testing.T) {
	in := fixture(t, 0)
	rep, err := Assess(in, []string{"auth/\x1b[2Jevil.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(rep.Format(), '\x1b') {
		t.Error("an escape sequence in a path reached the report")
	}
}
