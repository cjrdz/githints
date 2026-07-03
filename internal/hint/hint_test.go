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
		Branch:     "feature/rotate-keys",
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
	for _, want := range []string{"# auth/token.go", "rotate signing key", "security rotation", "deadbeef", "+10 -5", "feature/rotate-keys"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered file missing %q:\n%s", want, body)
		}
	}
}

func TestRenderEscapesMarkdownInjection(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath: "xss.go",
		Source:   "agent",
		Summary:  "<script>alert('xss')</script> **bold** `code`",
		Reason:   "[click](http://evil) ![](http://evil/img.png)",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := RenderFile(st, root, "xss.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}

	got, err := os.ReadFile(FilePath(root, "xss.go"))
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	body := string(got)

	// HTML tags must be escaped.
	if strings.Contains(body, "<script>") {
		t.Errorf("rendered file contains unescaped <script>:\n%s", body)
	}
	// Markdown syntax must be escaped so it is not interpreted.
	for _, dangerous := range []string{"**bold**", "`code`", "[click](http://evil)", "![](http://evil/img.png)"} {
		if strings.Contains(body, dangerous) {
			t.Errorf("rendered file contains unescaped markdown token %q:\n%s", dangerous, body)
		}
	}
	// But the human-readable text should still be present (escaped).
	for _, present := range []string{"&lt;script&gt;", "alert", "bold", "code", "click", "http://evil"} {
		if !strings.Contains(body, present) {
			t.Errorf("escaped text missing %q:\n%s", present, body)
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
		Source:     "fallback",
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
		{FilePath: "c.go", CommitHash: "222", Source: "fallback", Summary: "commit 2 c"},
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

func TestVerifyRenderedDetectsTampering(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath:   "a.go",
		CommitHash: "abc123",
		Source:     "agent",
		Summary:    "first change",
		RecordedAt: 100,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := RenderFile(st, root, "a.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if err := RenderChangelog(st, root, 10); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	diverged, err := VerifyRendered(st, root, 10, 10)
	if err != nil {
		t.Fatalf("VerifyRendered: %v", err)
	}
	if len(diverged) != 0 {
		t.Fatalf("expected consistent render, got diverged: %v", diverged)
	}

	path := FilePath(root, "a.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(path, append(content, []byte("\nattacker edit\n")...), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	diverged, err = VerifyRendered(st, root, 10, 10)
	if err != nil {
		t.Fatalf("VerifyRendered after tamper: %v", err)
	}
	if len(diverged) != 1 {
		t.Fatalf("expected 1 diverged file, got %d: %v", len(diverged), diverged)
	}
	if diverged[0] != path {
		t.Errorf("diverged path = %q, want %q", diverged[0], path)
	}
}

func TestRenderShowsClockTamperWarning(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath:   "a.go",
		Source:     "agent",
		Summary:    "first",
		RecordedAt: 100,
	}); err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	if _, err := st.Insert(store.Change{
		FilePath:   "a.go",
		Source:     "agent",
		Summary:    "second",
		RecordedAt: 10,
	}); err != nil {
		t.Fatalf("Insert second: %v", err)
	}

	if err := RenderFile(st, root, "a.go", 10); err != nil {
		t.Fatalf("RenderFile: %v", err)
	}
	if err := RenderChangelog(st, root, 10); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	perFile, err := os.ReadFile(FilePath(root, "a.go"))
	if err != nil {
		t.Fatalf("read per-file: %v", err)
	}
	if !strings.Contains(string(perFile), "CLOCK TAMPER WARNING") {
		t.Errorf("per-file hint missing clock tamper warning:\n%s", perFile)
	}

	changelog, err := os.ReadFile(filepath.Join(root, ".githints", "CHANGES.md"))
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	if !strings.Contains(string(changelog), "CLOCK TAMPER WARNING") {
		t.Errorf("changelog missing clock tamper warning:\n%s", changelog)
	}
}
