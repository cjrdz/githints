package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
)

// reminderEvery bounds how often a reminder runs git: a burst of read calls
// shares one git status. A variable so a test can turn the window off.
var reminderEvery = 2 * time.Second

const (
	// reminderNames is how many file names one reminder line shows.
	reminderNames = 5
	// reminderGitTimeout keeps a slow git status from stalling a tool reply.
	reminderGitTimeout = 3 * time.Second
)

// editReminder notices files changed during the session that have no recorded
// change, and says so once per distinct set. Its sources are functions so
// tests can drive it without git or a clock.
type editReminder struct {
	dirty    func(ctx context.Context) ([]string, error)
	recorded func(since int64) (map[string]bool, error)
	now      func() time.Time

	mu        sync.Mutex
	since     int64
	baseline  map[string]bool
	checkedAt time.Time
	pending   []string
	lastShown string
}

// newEditReminder samples the working tree at session start: files dirty then
// are someone else's work in progress and never mentioned. It returns nil
// (no reminders) outside a git repository.
func newEditReminder(root string, st *store.Store) *editReminder {
	r := &editReminder{
		dirty: func(ctx context.Context) ([]string, error) { return gitutil.DirtyFiles(ctx, root) },
		recorded: func(since int64) (map[string]bool, error) {
			rows, err := st.QueryChanges(store.Query{Since: since, Limit: 500})
			if err != nil {
				return nil, err
			}
			files := make(map[string]bool, len(rows))
			for _, c := range rows {
				files[c.FilePath] = true
			}
			return files, nil
		},
		now: time.Now,
	}
	if !r.start(context.Background()) {
		return nil
	}
	return r
}

// start records the session start and the files already dirty then. It
// reports false when git cannot answer.
func (r *editReminder) start(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, reminderGitTimeout)
	defer cancel()
	files, err := r.dirty(ctx)
	if err != nil {
		return false
	}
	r.since = r.now().Unix()
	r.baseline = make(map[string]bool, len(files))
	for _, f := range files {
		r.baseline[f] = true
	}
	return true
}

// forget drops the cached answer, so the call after a record sees it.
func (r *editReminder) forget() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.checkedAt = time.Time{}
	r.mu.Unlock()
}

// line is the reminder to append to a tool result, or "" when every file
// changed this session is recorded, git cannot answer, or this same set was
// already reported.
func (r *editReminder) line(ctx context.Context) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); r.checkedAt.IsZero() || now.Sub(r.checkedAt) >= reminderEvery {
		r.checkedAt = now
		r.pending = r.unrecorded(ctx)
	}
	key := strings.Join(r.pending, "\x00")
	if key == r.lastShown {
		return ""
	}
	r.lastShown = key
	if len(r.pending) == 0 {
		return ""
	}
	return formatReminder(r.pending)
}

func (r *editReminder) unrecorded(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, reminderGitTimeout)
	defer cancel()
	files, err := r.dirty(ctx)
	if err != nil {
		return nil // no reminder is better than a wrong one
	}
	recorded, err := r.recorded(r.since)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range files {
		if r.baseline[f] || recorded[f] || f == ".githints" || strings.HasPrefix(f, ".githints/") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// formatReminder names the files through oneLine: a file name is untrusted
// text and may carry terminal escapes or line breaks.
func formatReminder(files []string) string {
	shown := files
	if len(shown) > reminderNames {
		shown = shown[:reminderNames]
	}
	names := make([]string, len(shown))
	for i, f := range shown {
		names[i] = oneLine(f)
	}
	list := strings.Join(names, ", ")
	if more := len(files) - len(shown); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	noun := "files"
	if len(files) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("[githints] %d %s changed this session without a record: %s. Call record_change.", len(files), noun, list)
}
