package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cjrdz/githints/internal/store"
)

// AnchorVersion is the anchor format the post-commit hook writes today.
//
// Version 1 notes carry only "githints-root:", a Merkle root whose leaves
// include prev_hmac (so a salt rotation changes every root) and omit the row
// id and commit hash (so dropping or reassigning rows was invisible), with no
// domain separation between leaves and interior nodes.
//
// Version 2 fixes all three and adds a high-water mark over the whole log, so
// deleting rows anchored by an earlier commit is detected even when that
// commit's own rows are untouched.
const AnchorVersion = 2

// Anchor is the content of one refs/notes/githints note.
type Anchor struct {
	Version    int
	CommitRoot string // Merkle root of the rows stamped with this commit
	CommitRows int    // how many rows that was
	LogCount   int    // rows in the whole log with id <= LogLastID
	LogLastID  int64  // highest row id at anchoring time
	LogRoot    string // Merkle root of every row with id <= LogLastID
}

// String renders the note body. Keys are line-oriented so `git notes show`
// stays readable and an older binary still finds "githints-root:".
func (a Anchor) String() string {
	if a.Version < 2 {
		return "githints-root: " + a.CommitRoot
	}
	return fmt.Sprintf("githints-root: %s\ngithints-version: %d\ngithints-commit-rows: %d\ngithints-log-count: %d\ngithints-log-last-id: %d\ngithints-log-root: %s",
		a.CommitRoot, a.Version, a.CommitRows, a.LogCount, a.LogLastID, a.LogRoot)
}

// ParseAnchor reads a note body. A note without a version line is version 1.
func ParseAnchor(note string) (Anchor, error) {
	a := Anchor{Version: 1, CommitRows: -1}
	for _, line := range strings.Split(note, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		var err error
		switch key {
		case "githints-root":
			a.CommitRoot = val
		case "githints-version":
			a.Version, err = strconv.Atoi(val)
		case "githints-commit-rows":
			a.CommitRows, err = strconv.Atoi(val)
		case "githints-log-count":
			a.LogCount, err = strconv.Atoi(val)
		case "githints-log-last-id":
			a.LogLastID, err = strconv.ParseInt(val, 10, 64)
		case "githints-log-root":
			a.LogRoot = val
		}
		if err != nil {
			return Anchor{}, fmt.Errorf("parse %s: %w", key, err)
		}
	}
	if a.CommitRoot == "" {
		return Anchor{}, fmt.Errorf("note has no githints-root line")
	}
	if a.Version < 1 || a.Version > AnchorVersion {
		return Anchor{}, fmt.Errorf("unsupported anchor version %d", a.Version)
	}
	return a, nil
}

// BuildAnchor computes the current-format anchor for a commit. all is the
// whole log in id order; commitRows are the rows stamped with the commit.
func BuildAnchor(all, commitRows []store.Change) (Anchor, error) {
	commitRoot, err := merkleRootV2(commitRows, true)
	if err != nil {
		return Anchor{}, err
	}
	logRoot, err := merkleRootV2(all, false)
	if err != nil {
		return Anchor{}, err
	}
	a := Anchor{
		Version:    AnchorVersion,
		CommitRoot: commitRoot,
		CommitRows: len(commitRows),
		LogCount:   len(all),
		LogRoot:    logRoot,
	}
	if len(all) > 0 {
		a.LogLastID = all[len(all)-1].ID
	}
	return a, nil
}

// anchorLeaf is what a version 2 Merkle leaf commits to. prev_hmac and hmac
// are left out so a salt rotation, which re-signs every row, does not change
// any anchored root. The id is in, so deleting or reordering rows changes the
// root. The commit hash is in only for the per-commit root: the log root also
// covers rows still pending at anchoring time, which a later commit stamps.
type anchorLeaf struct {
	ID         int64  `json:"id"`
	CommitHash string `json:"commit_hash"`
	hmacPayload
}

// merkleRootV2 is MerkleRoot with domain separation: leaves are hashed with a
// 0x00 prefix and interior nodes with 0x01 (as in RFC 6962), so a leaf can
// never be passed off as an interior node or the reverse.
func merkleRootV2(rows []store.Change, withCommit bool) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}
	hashes := make([][sha256.Size]byte, len(rows))
	for i, c := range rows {
		p := payloadFromChange(c)
		p.PrevHMAC = ""
		leaf := anchorLeaf{ID: c.ID, hmacPayload: p}
		if withCommit {
			leaf.CommitHash = c.CommitHash
		}
		data, err := json.Marshal(leaf)
		if err != nil {
			return "", fmt.Errorf("marshal row %d for merkle leaf: %w", c.ID, err)
		}
		hashes[i] = sha256.Sum256(append([]byte{0x00}, data...))
	}
	for len(hashes) > 1 {
		next := make([][sha256.Size]byte, 0, (len(hashes)+1)/2)
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				buf := make([]byte, 0, 1+2*sha256.Size)
				buf = append(buf, 0x01)
				buf = append(buf, hashes[i][:]...)
				buf = append(buf, hashes[i+1][:]...)
				next = append(next, sha256.Sum256(buf))
			} else {
				next = append(next, hashes[i])
			}
		}
		hashes = next
	}
	return hex.EncodeToString(hashes[0][:]), nil
}

// AnchorReport is the outcome of checking the log against its notes.
type AnchorReport struct {
	Notes        int      // notes found under the ref
	Checked      int      // notes that parsed and were compared
	Problems     []string // tamper evidence; any entry fails verify
	Unanchored   []string // commits with rows but no note (informational)
	Unanchorable int      // rows newer than the newest log anchor (not yet anchored)
}

// VerifyAnchors compares the log with the notes anchored in git. all is the
// whole log in id order; notes maps a commit hash to its note body.
//
// Every note's per-commit root is recomputed from the rows now stamped with
// that commit. The newest version 2 note also pins the whole log up to its
// high-water id, which catches rows deleted from any earlier commit. Rows
// recorded after the newest anchor cannot be checked: nothing has committed
// to them yet.
func VerifyAnchors(all []store.Change, notes map[string]string) AnchorReport {
	rep := AnchorReport{Notes: len(notes)}

	byCommit := make(map[string][]store.Change)
	for _, c := range all {
		if c.CommitHash != "" {
			byCommit[c.CommitHash] = append(byCommit[c.CommitHash], c)
		}
	}

	commits := make([]string, 0, len(notes))
	for h := range notes {
		commits = append(commits, h)
	}
	sort.Strings(commits)

	var newest *Anchor
	for _, h := range commits {
		a, err := ParseAnchor(notes[h])
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("commit %s: unreadable anchor note: %v", short(h), err))
			continue
		}
		rep.Checked++
		rows := byCommit[h]
		var got string
		if a.Version == 1 {
			got, err = MerkleRoot(rows)
		} else {
			got, err = merkleRootV2(rows, true)
		}
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("commit %s: %v", short(h), err))
			continue
		}
		switch {
		case len(rows) == 0:
			rep.Problems = append(rep.Problems, fmt.Sprintf("commit %s: anchored rows are missing from the log", short(h)))
		case got != a.CommitRoot:
			msg := fmt.Sprintf("commit %s: rows changed since they were anchored", short(h))
			if a.Version == 1 {
				msg += " (legacy v1 anchor; a salt rotation also changes these roots)"
			}
			rep.Problems = append(rep.Problems, msg)
		case a.CommitRows >= 0 && a.CommitRows != len(rows):
			rep.Problems = append(rep.Problems, fmt.Sprintf("commit %s: anchored %d row(s), log now has %d", short(h), a.CommitRows, len(rows)))
		}
		if a.Version >= 2 && (newest == nil || a.LogLastID > newest.LogLastID) {
			a := a
			newest = &a
		}
	}

	for h := range byCommit {
		if _, ok := notes[h]; !ok {
			rep.Unanchored = append(rep.Unanchored, h)
		}
	}
	sort.Strings(rep.Unanchored)

	if newest != nil {
		var prefix []store.Change
		for _, c := range all {
			if c.ID <= newest.LogLastID {
				prefix = append(prefix, c)
			} else {
				rep.Unanchorable++
			}
		}
		if len(prefix) != newest.LogCount {
			rep.Problems = append(rep.Problems, fmt.Sprintf("log anchor: %d row(s) up to id %d were anchored, %d remain", newest.LogCount, newest.LogLastID, len(prefix)))
		} else if got, err := merkleRootV2(prefix, false); err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("log anchor: %v", err))
		} else if got != newest.LogRoot {
			rep.Problems = append(rep.Problems, fmt.Sprintf("log anchor: rows up to id %d changed since they were anchored", newest.LogLastID))
		}
	}

	return rep
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
