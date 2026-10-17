package integrity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

func outcomeChain(t *testing.T) ([]byte, []store.Change) {
	t.Helper()
	key := DeriveKey(make([]byte, saltSize), "test@example.com")
	rows := []store.Change{
		{ID: 1, FilePath: "a.go", Source: "agent", Summary: "added retry", DiffHash: "dh1", RecordedAt: 10},
		{ID: 2, FilePath: "b.go", Source: "agent", Summary: "unrelated", DiffHash: "dh2", RecordedAt: 20},
		{ID: 3, FilePath: "a.go", Source: "agent", Summary: "retry deadlocks", RecordedAt: 30,
			Kind: store.KindOutcome, TargetID: 1, TargetDiffHash: "dh1", OutcomeState: store.OutcomeFailed},
	}
	resign(t, key, rows)
	return key, rows
}

func resign(t *testing.T, key []byte, rows []store.Change) {
	t.Helper()
	for i := range rows {
		rows[i].PrevHMAC = ""
		if i > 0 {
			rows[i].PrevHMAC = rows[i-1].HMAC
		}
		rows[i].HMAC = mustHMAC(t, key, rows[i])
	}
}

func problemsFor(errs []IntegrityError, id int64) []string {
	var out []string
	for _, e := range errs {
		if e.ID == id {
			out = append(out, e.Problem)
		}
	}
	return out
}

func TestVerifyChainAcceptsOutcomes(t *testing.T) {
	key, rows := outcomeChain(t)
	if errs := VerifyChain(key, rows); len(errs) != 0 {
		t.Fatalf("valid chain with an outcome reported %+v", errs)
	}
}

// The outcome fields are signed: rewriting what became of a change, or what
// kind of row something is, breaks that row's HMAC.
func TestOutcomeFieldsAreSigned(t *testing.T) {
	for name, mutate := range map[string]func(*store.Change){
		"state":  func(c *store.Change) { c.OutcomeState = store.OutcomeWorked },
		"target": func(c *store.Change) { c.TargetID = 2 },
		"hash":   func(c *store.Change) { c.TargetDiffHash = "other" },
		"kind":   func(c *store.Change) { c.Kind = store.KindChange },
	} {
		t.Run(name, func(t *testing.T) {
			key, rows := outcomeChain(t)
			mutate(&rows[2])
			found := false
			for _, p := range problemsFor(VerifyChain(key, rows), 3) {
				if p == ProblemHMACMismatch {
					found = true
				}
			}
			if !found {
				t.Errorf("editing the outcome's %s was not detected", name)
			}
		})
	}
}

// Turning an ordinary change into an outcome, or giving it a target, must also
// show: the fields are omitted from its payload only while they are empty.
func TestOutcomeFieldsOnAChangeBreakItsHMAC(t *testing.T) {
	key, rows := outcomeChain(t)
	rows[0].Kind = store.KindOutcome
	rows[1].TargetID = 1
	errs := VerifyChain(key, rows)
	for _, id := range []int64{1, 2} {
		ok := false
		for _, p := range problemsFor(errs, id) {
			ok = ok || p == ProblemHMACMismatch
		}
		if !ok {
			t.Errorf("row %d: forged outcome fields were not detected", id)
		}
	}
}

func TestVerifyOutcomesChecksTheTarget(t *testing.T) {
	cases := map[string]struct {
		edit func([]store.Change)
		want string
	}{
		"missing target": {func(r []store.Change) { r[2].TargetID = 99 }, ProblemOutcomeTarget},
		"forward target": {func(r []store.Change) { r[2].TargetID = 3 }, ProblemOutcomeTarget},
		"outcome target": {func(r []store.Change) {
			r[1] = store.Change{ID: 2, FilePath: "a.go", Source: "agent", RecordedAt: 20,
				Kind: store.KindOutcome, TargetID: 1, TargetDiffHash: "dh1", OutcomeState: store.OutcomeWorked}
			r[2].TargetID = 2
		}, ProblemOutcomeTarget},
		"diff changed":  {func(r []store.Change) { r[2].TargetDiffHash = "stale" }, "different diff"},
		"unknown state": {func(r []store.Change) { r[2].OutcomeState = "great" }, "unknown state"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			key, rows := outcomeChain(t)
			tc.edit(rows)
			resign(t, key, rows) // a valid signature: only the reference check can catch it
			var got []string
			for _, e := range VerifyChain(key, rows) {
				got = append(got, e.Problem)
			}
			if !strings.Contains(strings.Join(got, "|"), tc.want) {
				t.Errorf("problems = %q, want one containing %q", got, tc.want)
			}
		})
	}
}

// An outcome row's payload is as frozen as a change's: the next change to the
// layout must not silently re-sign history. This pins one and its HMAC.
func TestOutcomePayloadBytesAreFrozen(t *testing.T) {
	c := store.Change{
		FilePath: "a.go", Branch: "main", Source: "agent", Summary: "retry deadlocks", AgentID: "s1",
		RecordedAt: 1234567890, PrevHMAC: "prev",
		Kind: store.KindOutcome, TargetID: 7, TargetDiffHash: "dh", OutcomeState: store.OutcomeFailed,
	}
	got, err := json.Marshal(payloadFromChange(c))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"prev_hmac":"prev","file_path":"a.go","branch":"main","source":"agent","summary":"retry deadlocks","reason":"","diff_stat":"","diff_hash":"","agent_id":"s1","recorded_at":1234567890,"clock_tamper_warning":false,"kind":"outcome","target_id":7,"target_diff_hash":"dh","outcome":"failed"}`
	if string(got) != want {
		t.Fatalf("outcome payload changed:\n got %s\nwant %s", got, want)
	}
	key := DeriveKey([]byte("s"+string(make([]byte, 31))), "u@example.com")
	if h := mustHMAC(t, key, c); h != "a429f4c7130a663c3d8d184695f27bd6d7058cbef1d2548b065414015e1f71c9" {
		t.Errorf("outcome hmac = %s", h)
	}
}

// Anchors written before outcomes existed commit to rows without the new
// fields; appending an outcome later must not change those roots.
func TestOutcomesDoNotDisturbEarlierAnchors(t *testing.T) {
	rows := goldenRows()
	before, err := BuildAnchor(rows[:2], rows[:2])
	if err != nil {
		t.Fatal(err)
	}
	withOutcome := append(append([]store.Change(nil), rows[:2]...), store.Change{
		ID: 3, FilePath: "b.go", Source: "agent", Summary: "later", RecordedAt: 1234567893,
		Kind: store.KindOutcome, TargetID: 2, OutcomeState: store.OutcomeWorked,
	})
	after, err := BuildAnchor(withOutcome, rows[:2])
	if err != nil {
		t.Fatal(err)
	}
	if before.CommitRoot != after.CommitRoot {
		t.Errorf("per-commit root changed when an outcome was appended: %s -> %s", before.CommitRoot, after.CommitRoot)
	}
}
