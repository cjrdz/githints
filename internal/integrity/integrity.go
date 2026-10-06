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
// HMACs. For that actor the external check is the Merkle anchor written to
// refs/notes/githints after each commit, which `githints verify` recomputes
// (see anchor.go). It is only as external as wherever the notes ref is pushed.
package integrity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cjrdz/githints/internal/safefs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/store"
)

const saltFileName = ".salt"
const saltSize = 32
const saltDirEnv = "GITHINTS_SALT_DIR"

// repoIDFile names the per-repository identifier that keys the salt file.
// It lives in .githints/, so it moves with the repository; the salt used to be
// keyed on the absolute path, so renaming or moving the checkout silently
// orphaned it and every row then failed verify.
const repoIDFile = "repo-id"

// ErrSaltMissing means the change log has signed rows but no salt was found.
// A new salt would make every existing row fail verify, so one is not created.
var ErrSaltMissing = errors.New("integrity salt missing")

// SaltPath returns the path to the salt file. Legacy repos keep using
// .githints/.salt if it exists. New installs store the salt outside the
// repo tree (under os.UserConfigDir()/githints/salts, or GITHINTS_SALT_DIR
// if set) so it cannot be committed accidentally when sharing rendered
// markdown from .githints/. The file is named by the repo id when there is
// one, and by the hash of the absolute path for repositories set up before
// repo ids existed.
func SaltPath(root string) string {
	legacy := filepath.Join(root, ".githints", saltFileName)
	if _, err := os.Stat(legacy); err == nil || !os.IsNotExist(err) {
		return legacy
	}
	if id := readRepoID(root); id != "" {
		return filepath.Join(saltDir(root), id+".salt")
	}
	return pathKeyedSaltPath(root)
}

func pathKeyedSaltPath(root string) string {
	return filepath.Join(saltDir(root), repoSaltKey(root)+".salt")
}

// readRepoID returns the repo id, or "" if there is none usable. An id that
// git tracks came from a clone and could steer which salt on this machine is
// used, so it is ignored.
func readRepoID(root string) string {
	data, err := safefs.ReadFile(filepath.Join(root, ".githints"), repoIDFile, 128)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return ""
	}
	if tracked, err := gitutil.IsTracked(root, filepath.Join(".githints", repoIDFile)); err == nil && tracked {
		return ""
	}
	return id
}

// ensureRepoID returns the repo id, creating one if needed.
func ensureRepoID(root string) (string, error) {
	if id := readRepoID(root); id != "" {
		return id, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate repo id: %w", err)
	}
	id := hex.EncodeToString(b)
	if err := safefs.WriteFile(filepath.Join(root, ".githints"), repoIDFile, []byte(id+"\n"), safefs.StateDirPerm, 0o600); err != nil {
		return "", fmt.Errorf("write repo id: %w", err)
	}
	return id, nil
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
// path. Kept for repositories set up before repo ids.
func repoSaltKey(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:32]
}

// LoadOrCreateSalt reads the per-machine salt, creating it with 0600
// permissions if it does not yet exist -- unless the change log already has
// rows, in which case a new salt would invalidate all of them and
// ErrSaltMissing is returned with recovery steps instead.
func LoadOrCreateSalt(root string) ([]byte, error) {
	return loadSalt(root, false)
}

// loadSalt implements LoadOrCreateSalt. replacing is true only for a forced
// rotation, which re-signs every row and so may start from a fresh salt.
func loadSalt(root string, replacing bool) ([]byte, error) {
	path := SaltPath(root)
	if err := refuseTrackedSalt(root, path); err != nil {
		return nil, err
	}
	data, err := readSalt(path)
	if err == nil {
		if path == pathKeyedSaltPath(root) {
			adoptUnderRepoID(root, data)
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	// Nothing where the repo id points; a salt from before repo ids may still
	// be under the path hash.
	if legacyPath := pathKeyedSaltPath(root); legacyPath != path {
		if data, err := readSalt(legacyPath); err == nil {
			adoptUnderRepoID(root, data)
			return data, nil
		}
	}

	if !replacing {
		if n := signedRows(root); n > 0 {
			return nil, fmt.Errorf("%w: the change log has %d signed row(s), but there is no salt at %s.\n"+
				"A new salt would make every one of them fail `githints verify`, so none was created.\n"+
				"  - Moved the repository or changed machines? Restore the salt: on the old machine run\n"+
				"    `githints salt export -o salt.txt`, then here `githints salt import salt.txt`.\n"+
				"  - Salt gone for good? `githints rotate-salt -force` re-signs the log under a new salt\n"+
				"    (this discards the existing tamper evidence)", ErrSaltMissing, n, path)
		}
	}

	if path != filepath.Join(root, ".githints", saltFileName) {
		id, err := ensureRepoID(root)
		if err != nil {
			return nil, err
		}
		path = filepath.Join(saltDir(root), id+".salt")
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	if err := writeSaltFile(root, path, salt); err != nil {
		return nil, err
	}
	return salt, nil
}

// adoptUnderRepoID copies a path-keyed salt to a repo-id-keyed name, so the
// next rename or move of the checkout does not lose it. Best effort: the
// path-keyed copy is left in place and keeps working until then.
func adoptUnderRepoID(root string, salt []byte) {
	id, err := ensureRepoID(root)
	if err != nil {
		return
	}
	_ = writeSaltFile(root, filepath.Join(saltDir(root), id+".salt"), salt) // may already exist; either way the salt is usable
}

func readSalt(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, fmt.Errorf("read salt: %w", err)
	}
	if len(data) != saltSize {
		return nil, fmt.Errorf("salt file at %s has unexpected size %d", path, len(data))
	}
	return data, nil
}

// writeSaltFile creates a salt file 0600 in a 0700 directory. It never
// replaces an existing file.
func writeSaltFile(root, path string, salt []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create salt dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and one created
	// earlier by something else may be group- or world-readable.
	if dir == saltDir(root) && filepath.Base(dir) != ".githints" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("restrict salt dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write salt: %w", err)
	}
	if _, err := f.Write(salt); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return fmt.Errorf("write salt: %w", err)
	}
	return f.Close()
}

// signedRows reports how many rows carry an HMAC, without creating a store
// that does not exist yet.
func signedRows(root string) int {
	dbPath := filepath.Join(root, ".githints", "store.db")
	if fi, err := os.Stat(dbPath); err != nil || fi.Size() == 0 {
		return 0
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return 0
	}
	defer st.Close()
	rows, err := st.AllChanges()
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if r.HMAC != "" {
			n++
		}
	}
	return n
}

// ExistingKey derives the key from a salt that already exists, never
// creating, adopting, or moving one. For read-only diagnostics.
func ExistingKey(root string) ([]byte, string, error) {
	path := SaltPath(root)
	data, err := readSalt(path)
	if os.IsNotExist(err) && path != pathKeyedSaltPath(root) {
		path = pathKeyedSaltPath(root)
		data, err = readSalt(path)
	}
	if os.IsNotExist(err) {
		return nil, "", fmt.Errorf("%w at %s", ErrSaltMissing, SaltPath(root))
	}
	if err != nil {
		return nil, "", err
	}
	email, _ := gitutil.UserEmail()
	return DeriveKey(data, email), path, nil
}

// ExportSalt returns the salt for root, hex-encoded, for moving it to another
// machine or checkout. It never creates one.
func ExportSalt(root string) (string, error) {
	data, err := readSalt(SaltPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: nothing to export at %s", ErrSaltMissing, SaltPath(root))
		}
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// ImportSalt installs a hex-encoded salt for root. It refuses to replace an
// existing salt unless force is set, since that would orphan every row
// signed with it.
func ImportSalt(root, encoded string, force bool) (string, error) {
	salt, err := hex.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(salt) != saltSize {
		return "", fmt.Errorf("not a githints salt: want %d hex-encoded bytes", saltSize)
	}
	path := SaltPath(root)
	if path != filepath.Join(root, ".githints", saltFileName) {
		id, err := ensureRepoID(root)
		if err != nil {
			return "", err
		}
		path = filepath.Join(saltDir(root), id+".salt")
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			return "", fmt.Errorf("a salt already exists at %s; replacing it would orphan every row signed with it (use -force if you are sure)", path)
		}
		if err := os.Remove(path); err != nil {
			return "", err
		}
	}
	return path, writeSaltFile(root, path, salt)
}

// refuseTrackedSalt rejects a legacy in-repo salt that git tracks. A file that
// is in the index came from a commit, so anyone with the repository has it:
// the HMAC key would be public, or chosen by whoever committed it.
func refuseTrackedSalt(root, path string) error {
	legacy := filepath.Join(root, ".githints", saltFileName)
	if path != legacy {
		return nil
	}
	tracked, err := gitutil.IsTracked(root, filepath.Join(".githints", saltFileName))
	if err != nil || !tracked {
		// Not a git repository (tests, or a bare directory): nothing to check.
		return nil
	}
	return fmt.Errorf("%s is tracked by git, so the integrity key is public; "+
		"run `git rm --cached .githints/.salt`, delete the file, then `githints rotate-salt -force` "+
		"to re-sign the log under a private salt", legacy)
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
// by store.ClaimPendingTx after the row is inserted; the per-commit Merkle
// root in refs/notes/githints is what binds rows to their commit.
//
// clock_tamper_warning IS included: it is immutable once written, and leaving
// it out meant an attacker with database write access could flip it from 1 to
// 0 and erase the tamper evidence while the chain still verified clean.
type hmacPayload struct {
	PrevHMAC           string `json:"prev_hmac"`
	FilePath           string `json:"file_path"`
	Branch             string `json:"branch"`
	Source             string `json:"source"`
	Summary            string `json:"summary"`
	Reason             string `json:"reason"`
	DiffStat           string `json:"diff_stat"`
	DiffHash           string `json:"diff_hash"`
	AgentID            string `json:"agent_id"`
	RecordedAt         int64  `json:"recorded_at"`
	ClockTamperWarning bool   `json:"clock_tamper_warning"`
}

func payloadFromChange(c store.Change) hmacPayload {
	return hmacPayload{
		PrevHMAC:           c.PrevHMAC,
		FilePath:           c.FilePath,
		Branch:             c.Branch,
		Source:             c.Source,
		Summary:            c.Summary,
		Reason:             c.Reason,
		DiffStat:           c.DiffStat,
		DiffHash:           c.DiffHash,
		AgentID:            c.AgentID,
		RecordedAt:         c.RecordedAt,
		ClockTamperWarning: c.ClockTamperWarning,
	}
}

// ComputeHMAC returns the hex-encoded HMAC-SHA256 for a change row, linked
// to the previous row's HMAC. The error is only reachable if the payload ever
// grows a type encoding/json cannot handle; it used to panic here, which meant
// a crash mid-commit-hook where nothing recovers.
func ComputeHMAC(key []byte, c store.Change) (string, error) {
	data, err := json.Marshal(payloadFromChange(c))
	if err != nil {
		return "", fmt.Errorf("marshal hmac payload: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// ProblemHMACMismatch is the Problem of a row whose HMAC does not recompute
// under the current key.
const ProblemHMACMismatch = "hmac does not recompute"

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

		if i == 0 && c.PrevHMAC != "" {
			// The head of the chain has no predecessor. Without this check a
			// forged first row could claim any prev_hmac it liked.
			errs = append(errs, IntegrityError{
				ID:       c.ID,
				Problem:  "first row has a non-empty prev_hmac (chain head was forged or rows are missing)",
				GotPrev:  c.PrevHMAC,
				WantPrev: "",
			})
		}

		if i > 0 && c.PrevHMAC != prev.HMAC {
			errs = append(errs, IntegrityError{
				ID:       c.ID,
				Problem:  "prev_hmac does not match previous row's hmac",
				WantPrev: prev.HMAC,
				GotPrev:  c.PrevHMAC,
			})
		}

		want, err := ComputeHMAC(key, c)
		if err != nil {
			errs = append(errs, IntegrityError{
				ID:      c.ID,
				Problem: fmt.Sprintf("cannot recompute hmac: %v", err),
			})
			prev = c
			continue
		}
		if !hmac.Equal([]byte(c.HMAC), []byte(want)) {
			errs = append(errs, IntegrityError{
				ID:       c.ID,
				Problem:  ProblemHMACMismatch,
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

// MerkleRoot is the version 1 anchor algorithm, kept so notes written before
// version 2 still verify. New anchors use merkleRootV2 via BuildAnchor.
//
// MerkleRoot computes a SHA-256 Merkle tree root over all change rows,
// ordered by id. It is a compact, public fingerprint of the entire log that
// can be committed elsewhere (git note, CI artifact) and verified later.
// Empty log returns the empty string.
//
// The marshal error is returned rather than ignored: silently hashing "" for a
// row that failed to encode would weaken the fingerprint without any signal.
func MerkleRoot(rows []store.Change) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}

	hashes := make([][sha256.Size]byte, len(rows))
	for i, c := range rows {
		data, err := json.Marshal(payloadFromChange(c))
		if err != nil {
			return "", fmt.Errorf("marshal row %d for merkle leaf: %w", c.ID, err)
		}
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
	return hex.EncodeToString(hashes[0][:]), nil
}

// RotateSalt generates a new integrity salt and re-signs every existing
// change row with the new key so the HMAC chain remains valid. It verifies
// the existing chain first unless force is true. The new salt is written
// atomically: if the DB update succeeds but the salt rename fails, the
// .salt.new file is left behind for manual recovery.
func RotateSalt(root string, force bool) error {
	oldSalt, err := loadSalt(root, force)
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
		c.HMAC, err = ComputeHMAC(newKey, c)
		if err != nil {
			return fmt.Errorf("re-sign row %d: %w", c.ID, err)
		}
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
