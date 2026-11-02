package recorder

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

func TestHints(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		file, summary, reason, prev string
		want                        []string
	}{
		{"good record", "a.go", "Replaced the linear scan in FindUser with a map lookup", "profiled at 10k users", "", nil},
		{"no reason", "a.go", "Retry the token refresh once on a 401", "", "", []string{"no reason"}},
		{"blank reason", "a.go", "Retry the token refresh once on a 401", "  \n", "", []string{"no reason"}},
		{"generic verb, two words", "a.go", "Updated function", "x", "", []string{"generic summary"}},
		{"generic verb alone", "a.go", "fix.", "x", "", []string{"generic summary"}},
		{"file name", "internal/a.go", "a.go", "x", "", []string{"generic summary"}},
		{"full path", "internal/a.go", "internal/a.go", "x", "", []string{"generic summary"}},
		{"specific summary opening with a generic verb", "a.go", "Fix the race between refresh and logout", "x", "", nil},
		{"repeat, any case", "a.go", "Added retry", "x", "added retry ", []string{"same summary"}},
		{"all three", "a.go", "update", "", "update", []string{"no reason", "generic summary", "same summary"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Hints(tc.file, tc.summary, tc.reason, tc.prev)
			if len(got) != len(tc.want) {
				t.Fatalf("Hints = %q, want %d hint(s) %q", got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w) {
					t.Errorf("hint %d = %q, want it to start with %q", i, got[i], w)
				}
			}
		})
	}
}

func TestPreviousSummary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := PreviousSummary(st, "a.go"); got != "" {
		t.Errorf("empty log: %q", got)
	}
	for _, s := range []string{"first", "second"} {
		if _, err := st.Insert(store.Change{FilePath: "a.go", Source: "agent", Summary: s}); err != nil {
			t.Fatal(err)
		}
	}
	if got := PreviousSummary(st, "a.go"); got != "second" {
		t.Errorf("PreviousSummary = %q, want the latest", got)
	}
}
