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
