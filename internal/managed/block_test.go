package managed

import (
	"os"
	"path/filepath"
	"testing"
)

// A cloned repository can commit CLAUDE.md -> a file outside the repo; init
// must not append its managed block at the far end of the link.
func TestEnsureBlockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "bashrc")
	if err := os.WriteFile(outside, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a file symlink on this platform: %v", err)
	}

	if err := EnsureBlock(link, "<!-- s -->", "<!-- e -->", []string{"x"}); err == nil {
		t.Fatal("EnsureBlock wrote through a symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# mine\n" {
		t.Fatalf("link target was modified: %q", got)
	}
}

// Only the block decides its state. Whitespace around it, which EnsureBlock
// would normalize, used to make a current block read as stale.
func TestBlockStateLooksOnlyAtTheBlock(t *testing.T) {
	dir := t.TempDir()
	b := Block{Path: filepath.Join(dir, "AGENTS.md"), Start: MarkdownStart, End: MarkdownEnd, Body: []string{"rule one", "rule two"}}
	if err := b.Ensure(); err != nil {
		t.Fatal(err)
	}
	block := MarkdownStart + "\nrule one\nrule two\n" + MarkdownEnd
	for name, content := range map[string]string{
		"as written":           "",
		"trailing blank lines": "# Notes\n\n" + block + "\n\n\n\n",
		"no final newline":     "# Notes\n\n" + block,
		"text after the block": block + "\n\nMore of the project's own rules.\n",
	} {
		if content != "" {
			if err := os.WriteFile(b.Path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := b.State(); err != nil || got != BlockCurrent {
			t.Errorf("%s: State = %v, %v; want current", name, got, err)
		}
	}

	if err := os.WriteFile(b.Path, []byte(MarkdownStart+"\nrule one\n"+MarkdownEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.State(); got != BlockStale {
		t.Errorf("a changed block: State = %v, want stale", got)
	}
	if err := os.WriteFile(b.Path, []byte("# Only the project's text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.State(); got != BlockMissing {
		t.Errorf("no block: State = %v, want missing", got)
	}
}
