package recorder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

func outcomeFixture(t *testing.T) (*store.Store, string, store.Change) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := RecordChange(st, dir, testKey, Input{FilePath: "a.go", Summary: "added retry loop", Source: "agent"})
	if err != nil {
		t.Fatalf("RecordChange: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("RecordChange did not return the row id")
	}
	return st, dir, c
}

func TestRecordOutcomeLinksAndSigns(t *testing.T) {
	st, dir, c := outcomeFixture(t)

	o, err := RecordOutcome(st, dir, testKey, OutcomeInput{
		ChangeID: c.ID, State: store.OutcomeFailed, Note: "retry loop deadlocks under load", AgentID: "s1",
	})
	if err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if o.Kind != store.KindOutcome || o.TargetID != c.ID || o.FilePath != "a.go" || o.TargetDiffHash != c.DiffHash {
		t.Errorf("outcome row = %+v", o)
	}

	all, _ := st.AllChanges()
	if len(all) != 2 {
		t.Fatalf("%d rows, want change + outcome", len(all))
	}
	if errs := integrity.VerifyChain(testKey, all); len(errs) != 0 {
		t.Fatalf("chain with an outcome does not verify: %+v", errs)
	}

	hist, _ := st.FileHistory("a.go", 10)
	if len(hist) != 1 || hist[0].LatestOutcome == nil || hist[0].LatestOutcome.OutcomeState != store.OutcomeFailed {
		t.Fatalf("history = %+v", hist)
	}

	md, err := os.ReadFile(filepath.Join(dir, ".githints", "a.go.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "outcome: failed") || !strings.Contains(string(md), "deadlocks") {
		t.Errorf("hint does not show the outcome:\n%s", md)
	}
}

func TestRecordOutcomeValidation(t *testing.T) {
	st, dir, c := outcomeFixture(t)
	o, err := RecordOutcome(st, dir, testKey, OutcomeInput{ChangeID: c.ID, State: store.OutcomeWorked})
	if err != nil {
		t.Fatalf("a worked outcome needs no note: %v", err)
	}

	cases := map[string]OutcomeInput{
		"no id":              {State: store.OutcomeWorked},
		"unknown id":         {ChangeID: 999, State: store.OutcomeWorked},
		"bad state":          {ChangeID: c.ID, State: "great"},
		"empty state":        {ChangeID: c.ID},
		"failed without why": {ChangeID: c.ID, State: store.OutcomeFailed},
		"partial no why":     {ChangeID: c.ID, State: store.OutcomePartial},
		"reverted no why":    {ChangeID: c.ID, State: store.OutcomeReverted},
		"note too long":      {ChangeID: c.ID, State: store.OutcomeFailed, Note: strings.Repeat("x", MaxOutcomeNoteLen+1)},
		"agent id too long":  {ChangeID: c.ID, State: store.OutcomeWorked, AgentID: strings.Repeat("x", MaxAgentIDLen+1)},
		"on an outcome":      {ChangeID: o.ID, State: store.OutcomeWorked},
		"secret in note":     {ChangeID: c.ID, State: store.OutcomeFailed, Note: "used key AKIAIOSFODNN7EXAMPLE"},
		// A zero-width space used to hide a credential from the scan.
		"hidden secret": {ChangeID: c.ID, State: store.OutcomeFailed, Note: "key AKIA\u200bIOSFODNN7EXAMPLE"},
	}
	before, _ := st.AllChanges()
	for name, in := range cases {
		if _, err := RecordOutcome(st, dir, testKey, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if after, _ := st.AllChanges(); len(after) != len(before) {
		t.Errorf("rejected outcomes still wrote %d rows", len(after)-len(before))
	}
}

func TestRecordOutcomeStripsControlCharacters(t *testing.T) {
	st, dir, c := outcomeFixture(t)
	o, err := RecordOutcome(st, dir, testKey, OutcomeInput{
		ChangeID: c.ID, State: store.OutcomeFailed, Note: "bad\x1b[31m red\u202e", AgentID: "a\x1b]0;x\x07gent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(o.Summary+o.AgentID, "\x1b\x07\u202e") {
		t.Errorf("control characters stored: %q %q", o.Summary, o.AgentID)
	}
}

// The target's row and signature are untouched by what is said about it.
func TestRecordOutcomeNeverEditsTheChange(t *testing.T) {
	st, dir, c := outcomeFixture(t)
	for _, state := range []string{store.OutcomeWorked, store.OutcomeReverted} {
		if _, err := RecordOutcome(st, dir, testKey, OutcomeInput{ChangeID: c.ID, State: state, Note: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _, _ := st.GetChange(c.ID)
	if got.HMAC != c.HMAC || got.Summary != c.Summary || got.OutcomeState != "" {
		t.Errorf("change was modified: %+v", got)
	}
	hist, _ := st.FileHistory("a.go", 10)
	if hist[0].LatestOutcome.OutcomeState != store.OutcomeReverted {
		t.Errorf("newest outcome did not win: %+v", hist[0].LatestOutcome)
	}
}

// Outcomes and changes interleave across processes like any other writes.
func TestConcurrentOutcomesKeepOneChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".githints", "store.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	c, err := RecordChange(first, dir, testKey, Input{FilePath: "a.go", Summary: "seed", Source: "agent"})
	if err != nil {
		t.Fatal(err)
	}

	const writers, each = 4, 10
	errs := make(chan error, writers*each)
	done := make(chan struct{})
	for w := 0; w < writers; w++ {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		go func() {
			defer func() { done <- struct{}{} }()
			for n := 0; n < each; n++ {
				if _, err := RecordOutcome(st, dir, testKey, OutcomeInput{ChangeID: c.ID, State: store.OutcomeUnknown}); err != nil {
					errs <- err
				}
			}
		}()
	}
	for w := 0; w < writers; w++ {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Errorf("outcome: %v", err)
	}
	all, _ := first.AllChanges()
	if len(all) != 1+writers*each {
		t.Fatalf("%d rows, want %d", len(all), 1+writers*each)
	}
	if errs := integrity.VerifyChain(testKey, all); len(errs) != 0 {
		t.Fatalf("chain broken: %+v", errs[0])
	}
}
