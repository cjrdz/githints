package integrity

import (
	"encoding/json"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

// goldenRows is a fixed log. Nothing here may change: the point of the file is
// that rows signed and anchored by an older binary keep verifying.
func goldenRows() []store.Change {
	return []store.Change{
		{
			ID: 1, FilePath: "auth/token.go", CommitHash: "aaaa1111", Branch: "main",
			Source: "agent", Summary: "rotate key", Reason: "security", DiffStat: "+5 -2",
			DiffHash: "d1", AgentID: "session-1", RecordedAt: 1234567890,
		},
		{
			ID: 2, FilePath: "b.go", CommitHash: "aaaa1111", Branch: "main",
			Source: "fallback", Summary: "Change detected by git hook", RecordedAt: 1234567891,
			ClockTamperWarning: true,
		},
		{
			ID: 3, FilePath: "c.go", Branch: "main", Source: "llm", Summary: "pending row",
			RecordedAt: 1234567892,
		},
	}
}

// The signed payload is serialized with encoding/json in struct order, so the
// field set, names and order are an on-disk format. Adding a field without
// omitempty changes every existing row's bytes and breaks every stored HMAC
// and every anchored Merkle root at once. If this test fails, the change is
// not backward compatible: version the payload instead of editing it.
func TestHMACPayloadBytesAreFrozen(t *testing.T) {
	c := goldenRows()[0]
	c.PrevHMAC = "prev"
	got, err := json.Marshal(payloadFromChange(c))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"prev_hmac":"prev","file_path":"auth/token.go","branch":"main","source":"agent","summary":"rotate key","reason":"security","diff_stat":"+5 -2","diff_hash":"d1","agent_id":"session-1","recorded_at":1234567890,"clock_tamper_warning":false}`
	if string(got) != want {
		t.Fatalf("payload bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestComputeHMACChainIsFrozen(t *testing.T) {
	key := DeriveKey([]byte("s"+string(make([]byte, 31))), "u@example.com")
	want := []string{
		"b5ed9f1ff723ec9325f18857e0edf06ca7cead5c22050e1bbde8e17945d25c32",
		"56f9d989b02a9948b05acaabf43572bdb0f7dd822d5ae2cfe9e575b046ce3a02",
		"8b7941106e6f967b09611dbc95fdb32cc409ea1d161ce006be391f4fcb58813c",
	}
	prev := ""
	for i, c := range goldenRows() {
		c.PrevHMAC = prev
		h := mustHMAC(t, key, c)
		if h != want[i] {
			t.Errorf("row %d hmac = %s, want %s", c.ID, h, want[i])
		}
		prev = h
	}
}

func TestAnchorRootsAreFrozen(t *testing.T) {
	rows := goldenRows()
	a, err := BuildAnchor(rows, rows[:2])
	if err != nil {
		t.Fatal(err)
	}
	if a.CommitRoot != "5032738b2e1803efad12e5fa65474d1d885316bca1330f3c10d6d4abc3a3b7e9" {
		t.Errorf("commit root = %s", a.CommitRoot)
	}
	if a.LogRoot != "b7e19fe9a92f631d5303de62898bca6dc21a5681adc1acadfbdf63ba66c5bd6e" {
		t.Errorf("log root = %s", a.LogRoot)
	}
}
