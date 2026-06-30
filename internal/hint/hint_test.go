package hint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"githints/internal/store"
)

func setup(t *testing.T) (*store.Store, string, func()) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	return st, root, func() { st.Close() }
}

func TestFilePath(t *testing.T) {
	tests := []struct {
		root string
		src  string
		want string
	}{
		{"/repo", "main.go", "/repo/.githints/main.go.md"},
		{"/repo", "cmd/api/main.go", "/repo/.githints/cmd/api/main.go.md"},
	}

	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			got := FilePath(tt.root, tt.src)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderFileWithNoChanges(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if err := RenderFile(st, root, "foo.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}

	got, err := os.ReadFile(FilePath(root, "foo.go"))
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	if !strings.Contains(string(got), "_no recorded changes yet_") {
		t.Errorf("rendered file missing placeholder:\n%s", got)
	}
}

func TestRenderFileWithChanges(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath:   "auth/token.go",
		CommitHash: "deadbeef",
		Source:     "agent",
		Summary:    "rotate signing key",
		Reason:     "security rotation",
		DiffStat:   "+10 -5",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := RenderFile(st, root, "auth/token.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}

	got, err := os.ReadFile(FilePath(root, "auth/token.go"))
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	body := string(got)
	for _, want := range []string{"# auth/token.go", "rotate signing key", "security rotation", "deadbeef", "+10 -5"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered file missing %q:\n%s", want, body)
		}
	}
}

func TestRenderFileShowsUncommitted(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath: "auth/token.go",
		Source:   "agent",
		Summary:  "pending edit",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := RenderFile(st, root, "auth/token.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}

	got, err := os.ReadFile(FilePath(root, "auth/token.go"))
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	if !strings.Contains(string(got), "uncommitted") {
		t.Errorf("expected 'uncommitted' marker for pending row:\n%s", got)
	}
}

func TestRenderChangelog(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath:   "a.go",
		CommitHash: "abc123",
		Source:     "agent",
		Summary:    "first change",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := st.Insert(store.Change{
		FilePath:   "b.go",
		CommitHash: "abc123",
		Source:     "hook",
		Summary:    "second change",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := RenderChangelog(st, root, 10); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, ".githints", "CHANGES.md"))
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	body := string(got)
	if !strings.HasPrefix(body, "# Changes\n") {
		t.Errorf("changelog missing header:\n%s", body)
	}
	if !strings.Contains(body, "first change") || !strings.Contains(body, "second change") {
		t.Errorf("changelog missing entries:\n%s", body)
	}
}

func TestRenderChangelogGroupsByCommit(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	for _, c := range []store.Change{
		{FilePath: "a.go", CommitHash: "111", Source: "agent", Summary: "commit 1 a"},
		{FilePath: "b.go", CommitHash: "222", Source: "agent", Summary: "commit 2 b"},
		{FilePath: "c.go", CommitHash: "222", Source: "hook", Summary: "commit 2 c"},
	} {
		if _, err := st.Insert(c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	if err := RenderChangelog(st, root, 10); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, ".githints", "CHANGES.md"))
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	body := string(got)
	if strings.Count(body, "## ") != 2 {
		t.Errorf("expected 2 commit headings, got:\n%s", body)
	}
}
