package integrity

import (
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

func anchorRows() []store.Change {
	return []store.Change{
		{ID: 1, FilePath: "a.go", Summary: "one", CommitHash: "aaaa1111", HMAC: "h1"},
		{ID: 2, FilePath: "b.go", Summary: "two", CommitHash: "aaaa1111", HMAC: "h2", PrevHMAC: "h1"},
		{ID: 3, FilePath: "c.go", Summary: "three", CommitHash: "bbbb2222", HMAC: "h3", PrevHMAC: "h2"},
	}
}

// anchorAll simulates the post-commit hook having anchored each commit in
// turn, with the log as it stood at that moment.
func anchorAll(t *testing.T, rows []store.Change) map[string]string {
	t.Helper()
	notes := map[string]string{}
	for i, c := range rows {
		if _, done := notes[c.CommitHash]; done || c.CommitHash == "" {
			continue
		}
		var commitRows []store.Change
		for _, r := range rows {
			if r.CommitHash == c.CommitHash {
				commitRows = append(commitRows, r)
			}
		}
		last := i
		for last+1 < len(rows) && rows[last+1].CommitHash == c.CommitHash {
			last++
		}
		a, err := BuildAnchor(rows[:last+1], commitRows)
		if err != nil {
			t.Fatalf("BuildAnchor: %v", err)
		}
		notes[c.CommitHash] = a.String()
	}
	return notes
}

func clone(rows []store.Change) []store.Change {
	return append([]store.Change(nil), rows...)
}

func TestVerifyAnchorsClean(t *testing.T) {
	rows := anchorRows()
	rep := VerifyAnchors(rows, anchorAll(t, rows))
	if len(rep.Problems) != 0 || rep.Checked != 2 || len(rep.Unanchored) != 0 {
		t.Fatalf("clean log: %+v", rep)
	}
}

func TestVerifyAnchorsDetectsTampering(t *testing.T) {
	rows := anchorRows()
	notes := anchorAll(t, rows)

	cases := map[string]func([]store.Change) []store.Change{
		"edited summary":                 func(r []store.Change) []store.Change { r[0].Summary = "forged"; return r },
		"deleted tail of a commit":       func(r []store.Change) []store.Change { return r[:2] },
		"deleted a whole earlier commit": func(r []store.Change) []store.Change { return r[2:] },
		"moved row to another commit":    func(r []store.Change) []store.Change { r[1].CommitHash = "bbbb2222"; return r },
		"cleared clock tamper flag": func(r []store.Change) []store.Change {
			r[2].ClockTamperWarning = true // anchored as false; flipping either way must show
			return r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rep := VerifyAnchors(mutate(clone(rows)), notes)
			if len(rep.Problems) == 0 {
				t.Fatalf("tampering not detected: %+v", rep)
			}
		})
	}
}

// Deleting a row from an earlier commit and also deleting that commit's note
// leaves no per-commit evidence; the newest note's log root still catches it.
func TestVerifyAnchorsLogRootCatchesDeletedNote(t *testing.T) {
	rows := anchorRows()
	notes := anchorAll(t, rows)
	delete(notes, "aaaa1111")
	rep := VerifyAnchors(rows[1:], notes)
	if len(rep.Problems) == 0 {
		t.Fatalf("deletion under a removed note was not caught by the log root: %+v", rep)
	}
}

// A salt rotation re-signs every row (new hmac and prev_hmac) and a later
// commit stamps rows that were pending when an earlier anchor was taken.
// Neither is tampering.
func TestVerifyAnchorsStableAcrossRotationAndStamping(t *testing.T) {
	rows := anchorRows()
	rows = append(rows, store.Change{ID: 4, FilePath: "d.go", Summary: "pending", HMAC: "h4", PrevHMAC: "h3"})
	notes := anchorAll(t, rows) // row 4 is covered by bbbb2222's log root while still pending

	later := clone(rows)
	for i := range later {
		later[i].HMAC = "rotated-" + later[i].HMAC
		later[i].PrevHMAC = "rotated-" + later[i].PrevHMAC
	}
	later[3].CommitHash = "cccc3333"
	a, err := BuildAnchor(later, later[3:])
	if err != nil {
		t.Fatal(err)
	}
	notes["cccc3333"] = a.String()

	if rep := VerifyAnchors(later, notes); len(rep.Problems) != 0 {
		t.Fatalf("rotation or stamping reported as tampering: %v", rep.Problems)
	}
}

// Notes written before version 2 carry only githints-root and must keep
// verifying with the original algorithm.
func TestVerifyAnchorsLegacyV1(t *testing.T) {
	rows := anchorRows()
	root, err := MerkleRoot(rows[:2])
	if err != nil {
		t.Fatal(err)
	}
	notes := map[string]string{"aaaa1111": "githints-root: " + root}
	if rep := VerifyAnchors(rows, notes); len(rep.Problems) != 0 {
		t.Fatalf("legacy note failed: %v", rep.Problems)
	}
	tampered := clone(rows)
	tampered[0].Summary = "forged"
	if rep := VerifyAnchors(tampered, notes); len(rep.Problems) == 0 {
		t.Fatal("legacy note did not detect tampering")
	}
}

func TestParseAnchorRoundTrip(t *testing.T) {
	a, err := BuildAnchor(anchorRows(), anchorRows()[:2])
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAnchor(a.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != a {
		t.Fatalf("round trip: got %+v, want %+v", got, a)
	}
	for _, bad := range []string{"", "githints-version: 2", "githints-root: x\ngithints-version: 99", "githints-root: x\ngithints-log-count: nope"} {
		if _, err := ParseAnchor(bad); err == nil {
			t.Errorf("ParseAnchor(%q) accepted", bad)
		}
	}
}

// Leaves and interior nodes are hashed with distinct prefixes, so the v2
// root of two rows differs from the v1 root of the same rows.
func TestMerkleV2DomainSeparated(t *testing.T) {
	rows := anchorRows()[:2]
	v1, _ := MerkleRoot(rows)
	v2, _ := merkleRootV2(rows, true)
	if v1 == v2 || !strings.HasPrefix(buildAnchorMust(t, rows).String(), "githints-root: "+v2) {
		t.Fatalf("v1 %s, v2 %s", v1, v2)
	}
}

func buildAnchorMust(t *testing.T, rows []store.Change) Anchor {
	t.Helper()
	a, err := BuildAnchor(rows, rows)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
