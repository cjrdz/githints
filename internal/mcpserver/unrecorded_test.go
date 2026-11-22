package mcpserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/store"
)

// fakeTree is a working tree and a log the reminder can be pointed at.
type fakeTree struct {
	dirty     []string
	recorded  map[string]bool
	gitErr    error
	gitCalls  int
	clock     time.Time
	reminders *editReminder
}

func newFakeTree(t *testing.T, dirtyAtStart ...string) *fakeTree {
	t.Helper()
	f := &fakeTree{dirty: dirtyAtStart, recorded: map[string]bool{}, clock: time.Unix(1_000_000, 0)}
	f.reminders = &editReminder{
		dirty: func(context.Context) ([]string, error) {
			f.gitCalls++
			return append([]string(nil), f.dirty...), f.gitErr
		},
		recorded: func(int64) (map[string]bool, error) { return f.recorded, nil },
		now:      func() time.Time { return f.clock },
	}
	if !f.reminders.start(context.Background()) {
		t.Fatal("start failed")
	}
	return f
}

// next advances past the debounce window and asks for a line.
func (f *fakeTree) next() string {
	f.clock = f.clock.Add(reminderEvery)
	return f.reminders.line(context.Background())
}

func TestEditReminder(t *testing.T) {
	t.Run("clean tree", func(t *testing.T) {
		if got := newFakeTree(t).next(); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("files dirty at session start are not mentioned", func(t *testing.T) {
		f := newFakeTree(t, "wip.go")
		f.dirty = []string{"wip.go"}
		if got := f.next(); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("edited, then recorded, then edited again", func(t *testing.T) {
		f := newFakeTree(t, "wip.go")
		f.dirty = []string{"a.go", "wip.go"}
		if got := f.next(); got != "[githints] 1 file changed this session without a record: a.go. Call record_change." {
			t.Errorf("edited: %q", got)
		}
		if got := f.next(); got != "" {
			t.Errorf("same set repeated: %q", got)
		}
		f.recorded["a.go"] = true
		if got := f.next(); got != "" {
			t.Errorf("after recording: %q", got)
		}
		f.dirty = []string{"a.go", "b.go", "wip.go"}
		if got := f.next(); !strings.Contains(got, "1 file") || !strings.Contains(got, "b.go") || strings.Contains(got, "a.go") {
			t.Errorf("new edit after recording: %q", got)
		}
	})

	t.Run("the githints directory is never mentioned", func(t *testing.T) {
		f := newFakeTree(t)
		f.dirty = []string{".githints", ".githints/notes/x.md", ".githintsignore"}
		if got := f.next(); !strings.Contains(got, "1 file") || !strings.Contains(got, ".githintsignore") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("at most five names", func(t *testing.T) {
		f := newFakeTree(t)
		f.dirty = []string{"1.go", "2.go", "3.go", "4.go", "5.go", "6.go", "7.go"}
		got := f.next()
		if !strings.Contains(got, "7 files") || !strings.Contains(got, "5.go and 2 more.") || strings.Contains(got, "6.go") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("file names are flattened to one safe line", func(t *testing.T) {
		f := newFakeTree(t)
		f.dirty = []string{"evil\n\x1b[2Jname.go"}
		got := f.next()
		if strings.ContainsAny(got, "\n\x1b") || !strings.Contains(got, "evil  [2Jname.go") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("git calls are debounced", func(t *testing.T) {
		f := newFakeTree(t)
		f.dirty = []string{"a.go"}
		before := f.gitCalls
		f.next()
		f.clock = f.clock.Add(time.Second)
		f.reminders.line(context.Background())
		f.reminders.line(context.Background())
		if calls := f.gitCalls - before; calls != 1 {
			t.Errorf("%d git calls inside the debounce window, want 1", calls)
		}
		f.reminders.forget()
		f.reminders.line(context.Background())
		if calls := f.gitCalls - before; calls != 2 {
			t.Errorf("forget did not force a fresh check: %d calls", calls)
		}
	})

	t.Run("no line when git fails", func(t *testing.T) {
		f := newFakeTree(t)
		f.dirty = []string{"a.go"}
		f.gitErr = errors.New("boom")
		if got := f.next(); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("nil reminder is off", func(t *testing.T) {
		var r *editReminder
		r.forget()
		if got := r.line(context.Background()); got != "" {
			t.Errorf("got %q", got)
		}
	})
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// The reminder through the real server and real git: it rides on a read
// tool's result as its own content block, disappears once the file is
// recorded, and is gone entirely when switched off.
func TestUnrecordedReminderThroughTheServer(t *testing.T) {
	saved := reminderEvery
	reminderEvery = 0
	t.Cleanup(func() { reminderEvery = saved })

	for _, on := range []bool{true, false} {
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q")
		gitIn(t, dir, "config", "user.email", "test@example.com")
		gitIn(t, dir, "config", "user.name", "Test User")
		if err := os.WriteFile(filepath.Join(dir, "wip.go"), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.MCP.UnrecordedReminder = on
		srv, closeDBs := newServer(dir, st, cfg, "test")

		call := func(name string, args map[string]any) []mcp.Content {
			t.Helper()
			req := mcp.CallToolRequest{}
			req.Params.Name = name
			req.Params.Arguments = args
			res, err := srv.ListTools()[name].Handler(context.Background(), req)
			if err != nil || res.IsError {
				t.Fatalf("%s: %v %+v", name, err, res)
			}
			return res.Content
		}
		reminderIn := func(c []mcp.Content) string {
			for _, block := range c {
				if text, ok := block.(mcp.TextContent); ok && strings.HasPrefix(text.Text, "[githints]") {
					return text.Text
				}
			}
			return ""
		}

		if got := reminderIn(call("query_changes", nil)); got != "" {
			t.Errorf("on=%v: reminder before any edit: %q", on, got)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		content := call("get_dependency_graph", nil)
		got := reminderIn(content)
		if on && (!strings.Contains(got, "a.go") || strings.Contains(got, "wip.go") || len(content) < 2) {
			t.Errorf("after an edit: %q in %d blocks", got, len(content))
		}
		if !on && got != "" {
			t.Errorf("reminder while switched off: %q", got)
		}

		call("record_change", map[string]any{"file": "a.go", "summary": "Added package a", "reason": "test"})
		if got := reminderIn(call("query_changes", nil)); got != "" {
			t.Errorf("on=%v: reminder after recording: %q", on, got)
		}
		closeDBs()
		st.Close()
	}
}
