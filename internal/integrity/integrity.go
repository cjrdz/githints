// Package integrity provides the tamper-evident machinery for githints:
// key derivation from a local salt, HMAC chaining of change rows, chain
// verification, and a Merkle root over the whole log.
//
// Threat model: the chain detects an attacker who can write to store.db but
// does not have the integrity key. It also detects accidental corruption.
// The salt is machine-local and stored outside the repo tree by default so
// that sharing rendered markdown from .githints/ does not leak it; however,
// because the salt is readable by the same OS user that runs the agent,
// a determined same-user attacker who finds the salt can recompute valid
// HMACs. For that actor the external check is the Merkle root anchored in
// refs/notes/githints (which travels with the repo) and periodic
// `githints verify`.
package integrity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
)

const saltFileName = ".salt"
const saltSize = 32
const saltDirEnv = "GITHINTS_SALT_DIR"

// SaltPath returns the path to the salt file. Legacy repos keep using
// .githints/.salt if it exists. New installs store the salt outside the
// repo tree (under os.UserConfigDir()/githints/salts, or GITHINTS_SALT_DIR
// if set) so it cannot be committed accidentally when sharing rendered
// markdown from .githints/.
func SaltPath(root string) string {
	legacy := filepath.Join(root, ".githints", saltFileName)
	if _, err := os.Stat(legacy); err == nil || !os.IsNotExist(err) {
		return legacy
	}

	dir := saltDir(root)
	key := repoSaltKey(root)
	return filepath.Join(dir, fmt.Sprintf("%s.salt", key))
}

// saltDir picks the directory that holds per-repo salt files.
func saltDir(root string) string {
	if d := os.Getenv(saltDirEnv); d != "" {
		return d
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		// Fallback to the legacy directory if the platform has no config
		// dir. This branch is unlikely but keeps the function total.
		return filepath.Join(root, ".githints")
	}
	return filepath.Join(cfg, "githints", "salts")
}

// repoSaltKey returns a stable, filesystem-safe identifier for the repo
// at root. It is the first 32 hex characters of the SHA-256 of the absolute
// path.
func repoSaltKey(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:32]
}

// LoadOrCreateSalt reads the per-machine salt, creating it with 0600
// permissions if it does not yet exist. New installs store the salt outside
// the repo tree (see SaltPath); legacy repos keep using .githints/.salt.
func LoadOrCreateSalt(root string) ([]byte, error) {
	path := SaltPath(root)
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != saltSize {
			return nil, fmt.Errorf("salt file at %s has unexpected size %d", path, len(data))
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read salt: %w", err)
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create .githints dir: %w", err)
	}
	if err := os.WriteFile(path, salt, 0o600); err != nil {
		return nil, fmt.Errorf("write salt: %w", err)
	}
	return salt, nil
}

// DeriveKey produces the integrity key from the salt and the git user's
// email. If the repo has no user.email configured, the key is derived from
// the salt alone, which still prevents an attacker who only has the DB from
// forging HMACs (they would need the salt file too).
func DeriveKey(salt []byte, userEmail string) []byte {
	// Include the repo-specific email so keys are not accidentally shared
	// across unrelated repos on the same machine.
	h := hmac.New(sha256.New, salt)
	if userEmail != "" {
		h.Write([]byte(userEmail))
	}
	return h.Sum(nil)
}

// KeyFromRepo loads the salt and derives the key for the repo at root.
func KeyFromRepo(root string) ([]byte, error) {
	salt, err := LoadOrCreateSalt(root)
	if err != nil {
		return nil, err
	}
	email, _ := gitutil.UserEmail()
	return DeriveKey(salt, email), nil
}

// hmacPayload is the stable, JSON-serialized representation of a row for
// HMAC purposes. commit_hash is intentionally excluded because it is mutated
// by store.ClaimPending after the row is inserted.
type hmacPayload struct {
	PrevHMAC   string `json:"prev_hmac"`
	FilePath   string `json:"file_path"`
	Branch     string `json:"branch"`
	Source     string `json:"source"`
	Summary    string `json:"summary"`
	Reason     string `json:"reason"`
	DiffStat   string `json:"diff_stat"`
	DiffHash   string `json:"diff_hash"`
	AgentID    string `json:"agent_id"`
	RecordedAt int64  `json:"recorded_at"`
}

func payloadFromChange(c store.Change) hmacPayload {
	return hmacPayload{
		PrevHMAC:   c.PrevHMAC,
		FilePath:   c.FilePath,
		Branch:     c.Branch,
		Source:     c.Source,
		Summary:    c.Summary,
		Reason:     c.Reason,
		DiffStat:   c.DiffStat,
		DiffHash:   c.DiffHash,
		AgentID:    c.AgentID,
		RecordedAt: c.RecordedAt,
	}
}

// ComputeHMAC returns the hex-encoded HMAC-SHA256 for a change row, linked
// to the previous row's HMAC.
func ComputeHMAC(key []byte, c store.Change) string {
	payload := payloadFromChange(c)
	data, err := json.Marshal(payload)
	if err != nil {
		// JSON marshalling of strings + ints cannot fail in practice.
		panic(fmt.Sprintf("marshal hmac payload: %v", err))
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// IntegrityError describes one broken link in the HMAC chain.
type IntegrityError struct {
	ID           int64
	Problem      string
	WantHMAC     string
	GotHMAC      string
	WantPrev     string
	GotPrev      string
	RecordedAt   int64
	PrevRecorded int64
}

// VerifyChain walks every row in id order and verifies that each row's
// prev_hmac matches the previous row's hmac and that each hmac recomputes
// correctly. Rows with empty hmac are treated as legacy/unverified and are
// reported but do not break the chain for subsequent rows.
func VerifyChain(key []byte, rows []store.Change) []IntegrityError {
	var errs []IntegrityError
	var prev store.Change

	for i, c := range rows {
		if c.HMAC == "" {
			errs = append(errs, IntegrityError{
				ID:      c.ID,
				Problem: "row has no HMAC (legacy or unverified)",
			})
			prev = c
			continue
		}

		if i > 0 && c.PrevHMAC != prev.HMAC {
			errs = append(errs, IntegrityError{
				ID:       c.ID,
				Problem:  "prev_hmac does not match previous row's hmac",
				WantPrev: prev.HMAC,
				GotPrev:  c.PrevHMAC,
			})
		}

		want := ComputeHMAC(key, c)
		if !hmac.Equal([]byte(c.HMAC), []byte(want)) {
			errs = append(errs, IntegrityError{
				ID:       c.ID,
				Problem:  "hmac does not recompute",
				WantHMAC: want,
				GotHMAC:  c.HMAC,
			})
		}

		if i > 0 && c.RecordedAt < prev.RecordedAt {
			errs = append(errs, IntegrityError{
				ID:           c.ID,
				Problem:      "recorded_at went backwards (possible clock tampering)",
				RecordedAt:   c.RecordedAt,
				PrevRecorded: prev.RecordedAt,
			})
		}

		prev = c
	}
	return errs
}

// MerkleRoot computes a SHA-256 Merkle tree root over all change rows,
// ordered by id. It is a compact, public fingerprint of the entire log that
// can be committed elsewhere (git note, CI artifact) and verified later.
// Empty log returns the empty string.
func MerkleRoot(rows []store.Change) string {
	if len(rows) == 0 {
		return ""
	}

	hashes := make([][sha256.Size]byte, len(rows))
	for i, c := range rows {
		payload := payloadFromChange(c)
		data, _ := json.Marshal(payload)
		hashes[i] = sha256.Sum256(data)
	}

	for len(hashes) > 1 {
		next := make([][sha256.Size]byte, 0, (len(hashes)+1)/2)
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				h := sha256.New()
				h.Write(hashes[i][:])
				h.Write(hashes[i+1][:])
				var sum [sha256.Size]byte
				copy(sum[:], h.Sum(nil))
				next = append(next, sum)
			} else {
				next = append(next, hashes[i])
			}
		}
		hashes = next
	}
	return hex.EncodeToString(hashes[0][:])
}

// RotateSalt generates a new integrity salt and re-signs every existing
// change row with the new key so the HMAC chain remains valid. It verifies
// the existing chain first unless force is true. The new salt is written
// atomically: if the DB update succeeds but the salt rename fails, the
// .salt.new file is left behind for manual recovery.
func RotateSalt(root string, force bool) error {
	oldSalt, err := LoadOrCreateSalt(root)
	if err != nil {
		return fmt.Errorf("load salt: %w", err)
	}
	email, _ := gitutil.UserEmail()
	oldKey := DeriveKey(oldSalt, email)

	st, err := store.Open(filepath.Join(root, ".githints", "store.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	rows, err := st.AllChanges()
	if err != nil {
		return fmt.Errorf("load changes: %w", err)
	}

	if !force && len(rows) > 0 {
		if errs := VerifyChain(oldKey, rows); len(errs) > 0 {
			return fmt.Errorf("existing chain has %d integrity problem(s); use -force to rotate anyway", len(errs))
		}
	}

	newSalt := make([]byte, saltSize)
	if _, err := rand.Read(newSalt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	newKey := DeriveKey(newSalt, email)

	type update struct {
		id   int64
		hmac string
		prev string
	}
	updates := make([]update, len(rows))
	var prev string
	for i, c := range rows {
		c.PrevHMAC = prev
		c.HMAC = ComputeHMAC(newKey, c)
		updates[i] = update{id: c.ID, hmac: c.HMAC, prev: c.PrevHMAC}
		prev = c.HMAC
	}

	saltPath := SaltPath(root)
	tmpPath := saltPath + ".new"
	if err := os.WriteFile(tmpPath, newSalt, 0o600); err != nil {
		return fmt.Errorf("write new salt: %w", err)
	}

	if err := st.WithTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("UPDATE changes SET hmac = ?, prev_hmac = ? WHERE id = ?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range updates {
			if _, err := stmt.Exec(u.hmac, u.prev, u.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("re-sign rows: %w", err)
	}

	if err := os.Rename(tmpPath, saltPath); err != nil {
		return fmt.Errorf("activate new salt: %w", err)
	}
	return nil
}
