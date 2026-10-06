package integrity

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

func TestLoadOrCreateSalt(t *testing.T) {
	// Keep salt files out of the user's real config directory during tests.
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())

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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
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

// mustHMAC / mustMerkle keep the tests readable now that both return an error
// (they used to panic and silently ignore, respectively).
func mustHMAC(t *testing.T, key []byte, c store.Change) string {
	t.Helper()
	h, err := ComputeHMAC(key, c)
	if err != nil {
		t.Fatalf("ComputeHMAC: %v", err)
	}
	return h
}

func mustMerkle(t *testing.T, rows []store.Change) string {
	t.Helper()
	r, err := MerkleRoot(rows)
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	return r
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

	h1 := mustHMAC(t, key, c)
	h2 := mustHMAC(t, key, c)
	if h1 != h2 {
		t.Fatal("HMAC not stable for identical input")
	}

	c.AgentID = "session-2"
	h3 := mustHMAC(t, key, c)
	if h1 == h3 {
		t.Fatal("HMAC did not change when row changed")
	}

	otherKey := DeriveKey([]byte("different-salt-0123456789abcdef"), "u@example.com")
	h4 := mustHMAC(t, otherKey, c)
	if h3 == h4 {
		t.Fatal("HMAC did not change with different key")
	}

	// The clock-tamper flag must be covered by the signature: flipping it in
	// the database used to be invisible to VerifyChain.
	c.ClockTamperWarning = true
	if flagged := mustHMAC(t, key, c); flagged == h3 {
		t.Fatal("HMAC did not change when clock_tamper_warning was flipped")
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
		rows[i].HMAC = mustHMAC(t, key, rows[i])
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
		rows[i].HMAC = mustHMAC(t, key, rows[i])
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
		rows[i].HMAC = mustHMAC(t, key, rows[i])
	}

	r1 := mustMerkle(t, rows)
	r2 := mustMerkle(t, rows)
	if r1 != r2 {
		t.Fatal("Merkle root not stable")
	}
	if r1 == "" {
		t.Fatal("Merkle root empty for non-empty log")
	}

	rows[1].Summary = "changed"
	rows[1].HMAC = mustHMAC(t, key, rows[1])
	r3 := mustMerkle(t, rows)
	if r1 == r3 {
		t.Fatal("Merkle root did not change when row changed")
	}
}

func TestSaltPathUsesLegacyWhenPresent(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, ".githints", ".salt")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("existing-salt-0123456789abcdef"), 0o600); err != nil {
		t.Fatalf("write legacy salt: %v", err)
	}
	if got := SaltPath(root); got != legacy {
		t.Errorf("SaltPath = %q, want legacy %q", got, legacy)
	}
}

func TestSaltPathUsesConfigDir(t *testing.T) {
	saltDir := t.TempDir()
	t.Setenv("GITHINTS_SALT_DIR", saltDir)

	root := t.TempDir()
	got := SaltPath(root)
	if !filepath.IsAbs(got) {
		t.Fatalf("SaltPath = %q, want absolute path", got)
	}
	if filepath.Dir(got) != saltDir {
		t.Errorf("SaltPath dir = %q, want %q", filepath.Dir(got), saltDir)
	}
	if ext := filepath.Ext(got); ext != ".salt" {
		t.Errorf("SaltPath extension = %q, want .salt", ext)
	}
}

func TestRotateSalt(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())

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
		c.HMAC = mustHMAC(t, key, c)
		if _, err := st.Insert(c); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	mustInsert(store.Change{FilePath: "a.go", Source: "agent", Summary: "first", RecordedAt: 10})
	rows, _ := st.AllChanges()
	second := store.Change{FilePath: "b.go", Source: "agent", Summary: "second", RecordedAt: 20, PrevHMAC: rows[0].HMAC}
	second.HMAC = mustHMAC(t, key, second)
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

// A legacy in-repo salt that git tracks came from a commit: anyone with the
// repository has the key. It must be refused, not silently used.
func TestLoadOrCreateSaltRefusesTrackedLegacySalt(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".githints"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".githints", ".salt"), make([]byte, saltSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateSalt(root); err != nil {
		t.Fatalf("untracked legacy salt should load: %v", err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "-f", ".githints/.salt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if _, err := LoadOrCreateSalt(root); err == nil {
		t.Fatal("LoadOrCreateSalt used a salt that git tracks")
	}
}

// The salt used to be keyed on the absolute path, so renaming or moving the
// checkout silently produced a new salt and every row failed verify.
func TestSaltSurvivesMovingTheRepository(t *testing.T) {
	t.Setenv(saltDirEnv, t.TempDir())
	base := t.TempDir()
	before := filepath.Join(base, "before")
	if err := os.MkdirAll(before, 0o755); err != nil {
		t.Fatal(err)
	}
	s1, err := LoadOrCreateSalt(before)
	if err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(base, "after")
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadOrCreateSalt(after)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1) != string(s2) {
		t.Fatal("moving the repository produced a different salt")
	}
}

// A salt created before repo ids (keyed on the path) is adopted under a repo
// id the first time it is loaded, so the following move keeps it.
func TestPathKeyedSaltIsAdopted(t *testing.T) {
	t.Setenv(saltDirEnv, t.TempDir())
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".githints"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := make([]byte, saltSize)
	old[0] = 7
	if err := os.WriteFile(pathKeyedSaltPath(root), old, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreateSalt(root)
	if err != nil || string(got) != string(old) {
		t.Fatalf("legacy salt not used: %v", err)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	got, err = LoadOrCreateSalt(moved)
	if err != nil || string(got) != string(old) {
		t.Fatalf("adopted salt lost after a move: %v", err)
	}
}

// With signed rows in the log and no salt, a new salt would make every row
// fail verify. LoadOrCreateSalt refuses and explains; export/import recovers.
func TestMissingSaltWithRowsIsAnErrorAndImportRecovers(t *testing.T) {
	t.Setenv(saltDirEnv, t.TempDir())
	root := t.TempDir()
	salt, err := LoadOrCreateSalt(root)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := ExportSalt(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := store.Change{FilePath: "a.go", Source: "agent", Summary: "x"}
	c.HMAC = mustHMAC(t, DeriveKey(salt, ""), c)
	if _, err := st.Insert(c); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := os.Remove(SaltPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateSalt(root); !errors.Is(err, ErrSaltMissing) {
		t.Fatalf("want ErrSaltMissing, got %v", err)
	}
	if _, err := ImportSalt(root, exported, false); err != nil {
		t.Fatalf("ImportSalt: %v", err)
	}
	got, err := LoadOrCreateSalt(root)
	if err != nil || string(got) != string(salt) {
		t.Fatalf("imported salt not used: %v", err)
	}
	if _, err := ImportSalt(root, exported, false); err == nil {
		t.Fatal("ImportSalt replaced an existing salt without -force")
	}
	if _, err := ImportSalt(root, "nothex", true); err == nil {
		t.Fatal("ImportSalt accepted garbage")
	}
}
