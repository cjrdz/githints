package hint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/store"
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

// CHANGES.md is the file shared mode commits and clients render in a webview,
// but it was the one renderer that interpolated agent text raw.
func TestRenderChangelogEscapesAgentText(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	if _, err := st.Insert(store.Change{
		FilePath: "xss.go",
		Source:   "agent",
		Summary:  "<script>alert('xss')</script> **bold** [click](http://evil)",
		AgentID:  "agent-<img src=x onerror=alert(1)>",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := RenderChangelog(st, root, 10); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, dirName, "CHANGES.md"))
	if err != nil {
		t.Fatalf("read CHANGES.md: %v", err)
	}
	body := string(got)

	for _, dangerous := range []string{"<script>", "**bold**", "[click](http://evil)", "<img src=x"} {
		if strings.Contains(body, dangerous) {
			t.Errorf("CHANGES.md contains unescaped %q:\n%s", dangerous, body)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("escaped text missing from CHANGES.md:\n%s", body)
	}
}

// A backtick in a value must not break out of the `code span` that wraps it.
func TestCodeSpanStripsDelimiters(t *testing.T) {
	if got := codeSpan("abc`def\nghi"); got != "abcdefghi" {
		t.Errorf("codeSpan = %q, want %q", got, "abcdefghi")
	}
}

// linkDir points link at target. os.Symlink needs Developer Mode or elevation
// on Windows, but a junction (mklink /J) does not — which makes the Windows
// case the more exposed one, so it is worth testing rather than skipping.
func linkDir(t *testing.T, target, link string) error {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J: %v: %s", err, out)
	}
	return nil
}

// Path validation is lexical, so it cannot see a symlink or junction. os.Root
// resolves every component against the repo root and refuses to traverse out.
func TestRenderFileCannotEscapeRootViaSymlink(t *testing.T) {
	st, root, cleanup := setup(t)
	defer cleanup()

	outside := t.TempDir()
	link := filepath.Join(root, dirName, "escape")
	if err := os.MkdirAll(filepath.Join(root, dirName), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := linkDir(t, outside, link); err != nil {
		t.Skipf("cannot create a directory link on this platform: %v", err)
	}

	if _, err := st.Insert(store.Change{
		FilePath: "escape/pwned.go",
		Source:   "agent",
		Summary:  "should never land outside the repo",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Either the write is refused, or it lands inside the root — never in the
	// symlink target.
	_ = RenderFile(st, root, "escape/pwned.go", 10)

	if _, err := os.Lstat(filepath.Join(outside, "pwned.go.md")); err == nil {
		t.Fatal("render escaped the repo root through a symlink")
	}
}

func TestFilePath(t *testing.T) {
	tests := []struct {
		root string
		src  string
	}{
		{"/repo", "main.go"},
		{"/repo", "cmd/api/main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			got := FilePath(tt.root, tt.src)
			want := filepath.Join(tt.root, dirName, tt.src+".md")
			if got != want {
				t.Errorf("got %q, want %q", got, want)
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

	// The tamper flag is decided before the row is built so it can be covered
	// by the row's HMAC, so the caller passes it in rather than Insert
	// inferring it. Mirror what recorder.prepare does.
	insert := func(summary string, recordedAt int64) {
		t.Helper()
		c := store.Change{
			FilePath:   "a.go",
			Source:     "agent",
			Summary:    summary,
			RecordedAt: recordedAt,
		}
		c.ClockTamperWarning = st.CheckClockTamper(recordedAt)
		if _, err := st.Insert(c); err != nil {
			t.Fatalf("Insert %s: %v", summary, err)
		}
	}
	insert("first", 100)
	insert("second", 10)

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
