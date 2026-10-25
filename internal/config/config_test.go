package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Index.Enabled {
		t.Fatal("the index should be enabled when config file is absent")
	}
}

// TestDefaultIndexLanguagesIsAuto pins the default. It was ["go"], and a
// Python repository with no config.json indexed nothing.
func TestDefaultIndexLanguagesIsAuto(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"auto"}; !equalStrings(cfg.Index.Languages, want) {
		t.Errorf("languages = %v, want %v", cfg.Index.Languages, want)
	}
}

// The Ollama integration was removed. A config.json that still configures it
// (it travels in clones, so an older one will keep arriving) must load as
// before, with the block ignored.
func TestLoadIgnoresRemovedOllamaBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"ollama":{"enabled":true,"endpoint":"http://192.168.1.1:11434"},"index":{"max_file_size":2048}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Index.MaxFileSize != 2048 {
		t.Errorf("max_file_size = %d, want 2048 from the same file", cfg.Index.MaxFileSize)
	}
}

// writeIndexConfig writes a config.json selecting the given languages.
func writeIndexConfig(t *testing.T, langs string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data := []byte(`{"index":{"enabled":true,"languages":[` + langs + `]}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIndexLanguagesEnvOverridesJSON(t *testing.T) {
	dir := writeIndexConfig(t, `"go"`)

	t.Setenv("GITHINTS_INDEX_LANGUAGES", "typescript,svelte")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"typescript", "svelte"}; !equalStrings(cfg.Index.Languages, want) {
		t.Errorf("languages = %v, want %v", cfg.Index.Languages, want)
	}
}

func TestIndexLanguagesEnvNormalizesInput(t *testing.T) {
	dir := writeIndexConfig(t, `"go"`)

	// Spacing, case, duplicates and empty entries are all things a user
	// writing this by hand in a shell will produce.
	t.Setenv("GITHINTS_INDEX_LANGUAGES", " Go , TypeScript ,,go,")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"go", "typescript"}
	if !equalStrings(cfg.Index.Languages, want) {
		t.Errorf("languages = %v, want %v", cfg.Index.Languages, want)
	}
}

// TestIndexLanguagesEnvIgnoresEmptyValue pins that a value parsing to nothing
// leaves the configured list alone. Blanking it would fail validation with
// "index.languages is empty", pointing the user at config.json when the
// problem is in their environment.
func TestIndexLanguagesEnvIgnoresEmptyValue(t *testing.T) {
	dir := writeIndexConfig(t, `"typescript"`)

	t.Setenv("GITHINTS_INDEX_LANGUAGES", " , ,")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"typescript"}; !equalStrings(cfg.Index.Languages, want) {
		t.Errorf("languages = %v, want %v", cfg.Index.Languages, want)
	}
}

func TestIndexLanguagesUnsetKeepsJSON(t *testing.T) {
	dir := writeIndexConfig(t, `"svelte","astro"`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"svelte", "astro"}; !equalStrings(cfg.Index.Languages, want) {
		t.Errorf("languages = %v, want %v", cfg.Index.Languages, want)
	}
}

// config.json travels in clones, so the index limits have ceilings as well
// as floors: a repository must not be able to make the post-commit hook read
// multi-gigabyte files or wait an hour on a parser.
func TestLoadRejectsOversizedIndexLimits(t *testing.T) {
	for _, body := range []string{
		`{"index":{"enabled":true,"languages":["go"],"max_file_size":1073741824}}`,
		`{"index":{"enabled":true,"languages":["go"],"max_bytes":2000000000}}`,
		`{"index":{"enabled":true,"languages":["go"],"parse_timeout_ms":3600000}}`,
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("Load accepted %s", body)
		}
	}
}

// A config.json that links outside the repository is refused rather than
// followed.
func TestLoadRefusesConfigLinkedOutside(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(outside, []byte(`{"index":{"enabled":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".githints", "config.json")); err != nil {
		t.Skipf("cannot create a file symlink on this platform: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load followed config.json out of the repository")
	}
}
