package integrity

import (
	"os"
	"path/filepath"
	"testing"

	"githints/internal/store"
)

func TestLoadOrCreateSalt(t *testing.T) {
	root := t.TempDir()
	salt1, err := LoadOrCreateSalt(root)
	if err != nil {
		t.Fatalf("LoadOrCreateSalt first: %v", err)
	}
	if len(salt1) != saltSize {
		t.Fatalf("salt size = %d, want %d", len(salt1), saltSize)
	}

	info, err := os.Stat(SaltPath(root))
	if err != nil {
		t.Fatalf("Stat salt: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("salt file mode = %o, want 0600", info.Mode().Perm())
	}

	salt2, err := LoadOrCreateSalt(root)
	if err != nil {
		t.Fatalf("LoadOrCreateSalt second: %v", err)
	}
	if string(salt1) != string(salt2) {
		t.Fatal("salt changed between calls")
	}
}

func TestDeriveKeyStable(t *testing.T) {
	salt := []byte("0123456789abcdef0123456789abcdef")
	k1 := DeriveKey(salt, "alice@example.com")
	k2 := DeriveKey(salt, "alice@example.com")
	k3 := DeriveKey(salt, "bob@example.com")
	if string(k1) != string(k2) {
		t.Fatal("same inputs produced different keys")
	}
	if string(k1) == string(k3) {
		t.Fatal("different emails produced same key")
	}
	if len(k1) != 32 {
		t.Errorf("key length = %d, want 32", len(k1))
	}
}

func TestComputeHMACStableAndSensitive(t *testing.T) {
	key := DeriveKey([]byte("s"+string(make([]byte, 31))), "u@example.com")
	c := store.Change{
		FilePath:   "auth/token.go",
		Branch:     "main",
		Source:     "agent",
		Summary:    "rotate key",
		Reason:     "security",
		DiffStat:   "+5 -2",
		AgentID:    "session-1",
		RecordedAt: 1234567890,
		PrevHMAC:   "",
	}

	h1 := ComputeHMAC(key, c)
	h2 := ComputeHMAC(key, c)
	if h1 != h2 {
		t.Fatal("HMAC not stable for identical input")
	}

	c.AgentID = "session-2"
	h3 := ComputeHMAC(key, c)
	if h1 == h3 {
		t.Fatal("HMAC did not change when row changed")
	}

	otherKey := DeriveKey([]byte("different-salt-0123456789abcdef"), "u@example.com")
	h4 := ComputeHMAC(otherKey, c)
	if h3 == h4 {
		t.Fatal("HMAC did not change with different key")
	}
}

func TestVerifyChain(t *testing.T) {
	key := DeriveKey(make([]byte, saltSize), "test@example.com")

	rows := []store.Change{
		{ID: 1, FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 10},
		{ID: 2, FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 20},
	}
	for i := range rows {
		if i > 0 {
			rows[i].PrevHMAC = rows[i-1].HMAC
		}
		rows[i].HMAC = ComputeHMAC(key, rows[i])
	}

	errs := VerifyChain(key, rows)
	if len(errs) != 0 {
		t.Fatalf("expected valid chain, got %d errors: %+v", len(errs), errs)
	}

	rows[1].Summary = "tampered"
	errs = VerifyChain(key, rows)
	if len(errs) == 0 {
		t.Fatal("expected tampered row to fail verification")
	}
}

func TestVerifyChainDetectsBackwardClock(t *testing.T) {
	key := DeriveKey(make([]byte, saltSize), "test@example.com")

	rows := []store.Change{
		{ID: 1, FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 20},
		{ID: 2, FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 10},
	}
	for i := range rows {
		if i > 0 {
			rows[i].PrevHMAC = rows[i-1].HMAC
		}
		rows[i].HMAC = ComputeHMAC(key, rows[i])
	}

	errs := VerifyChain(key, rows)
	found := false
	for _, e := range errs {
		if e.Problem == "recorded_at went backwards (possible clock tampering)" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected backward-clock error, got %+v", errs)
	}
}

func TestMerkleRoot(t *testing.T) {
	key := DeriveKey(make([]byte, saltSize), "test@example.com")
	rows := []store.Change{
		{ID: 1, FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 10},
		{ID: 2, FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 20},
	}
	for i := range rows {
		if i > 0 {
			rows[i].PrevHMAC = rows[i-1].HMAC
		}
		rows[i].HMAC = ComputeHMAC(key, rows[i])
	}

	r1 := MerkleRoot(rows)
	r2 := MerkleRoot(rows)
	if r1 != r2 {
		t.Fatal("Merkle root not stable")
	}
	if r1 == "" {
		t.Fatal("Merkle root empty for non-empty log")
	}

	rows[1].Summary = "changed"
	rows[1].HMAC = ComputeHMAC(key, rows[1])
	r3 := MerkleRoot(rows)
	if r1 == r3 {
		t.Fatal("Merkle root did not change when row changed")
	}
}

func TestSaltPathInsideGithints(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, ".githints", ".salt")
	if got := SaltPath(root); got != want {
		t.Errorf("SaltPath = %q, want %q", got, want)
	}
}

func TestRotateSalt(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	key, err := KeyFromRepo(root)
	if err != nil {
		t.Fatalf("KeyFromRepo: %v", err)
	}

	mustInsert := func(c store.Change) {
		t.Helper()
		c.HMAC = ComputeHMAC(key, c)
		if _, err := st.Insert(c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	mustInsert(store.Change{FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 10})
	rows, _ := st.AllChanges()
	second := store.Change{FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 20, PrevHMAC: rows[0].HMAC}
	second.HMAC = ComputeHMAC(key, second)
	mustInsert(second)
	st.Close()

	oldSalt, err := os.ReadFile(SaltPath(root))
	if err != nil {
		t.Fatalf("read old salt: %v", err)
	}

	if err := RotateSalt(root, false); err != nil {
		t.Fatalf("RotateSalt: %v", err)
	}

	newSalt, err := os.ReadFile(SaltPath(root))
	if err != nil {
		t.Fatalf("read new salt: %v", err)
	}
	if string(oldSalt) == string(newSalt) {
		t.Fatal("salt did not change after rotation")
	}

	st, err = store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open after rotate: %v", err)
	}
	defer st.Close()

	newKey, err := KeyFromRepo(root)
	if err != nil {
		t.Fatalf("KeyFromRepo after rotate: %v", err)
	}
	if string(key) == string(newKey) {
		t.Fatal("key did not change after rotation")
	}

	rows, err = st.AllChanges()
	if err != nil {
		t.Fatalf("AllChanges after rotate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	errs := VerifyChain(newKey, rows)
	if len(errs) != 0 {
		t.Fatalf("chain invalid after rotation: %+v", errs)
	}
}
