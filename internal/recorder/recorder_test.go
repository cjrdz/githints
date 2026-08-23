package recorder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

// testKey is a deterministic key for unit tests so HMACs are stable.
var testKey = []byte("unit-test-key")

func TestRecordStoresAgentAsPending(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "cmd/api/main.go",
		Summary:  "add handler",
		Reason:   "feature request",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("cmd/api/main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].CommitHash != "" {
		t.Errorf("agent row commit_hash = %q, want empty (pending)", rows[0].CommitHash)
	}
	if rows[0].Source != "agent" {
		t.Errorf("source = %q, want agent", rows[0].Source)
	}

	if _, err := os.Stat(filepath.Join(dir, ".githints", "cmd", "api", "main.go.md")); err != nil {
		t.Fatalf("per-file hint not rendered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".githints", "CHANGES.md")); err != nil {
		t.Fatalf("changelog not rendered: %v", err)
	}
}

func TestRecordStoresHookWithHash(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath:   "cmd/api/main.go",
		Summary:    "hook fallback",
		Source:     "fallback",
		DiffStat:   "+5 -2",
		CommitHash: "deadbeef",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("cmd/api/main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].CommitHash != "deadbeef" {
		t.Errorf("hook row commit_hash = %q, want deadbeef", rows[0].CommitHash)
	}
	if rows[0].DiffStat != "+5 -2" {
		t.Errorf("diff_stat = %q, want +5 -2", rows[0].DiffStat)
	}
}

func TestRecordCapturesAgentDiffStat(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create a real git repo so WorktreeDiffStat can measure the change.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "main.go",
		Summary:  "add Run",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].DiffStat == "" {
		t.Errorf("agent row diff_stat is empty, expected working-tree stat")
	}
}

func TestRecordRejectsInvalidSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{FilePath: "x.go", Summary: "x", Source: "other"}); err == nil {
		t.Fatal("expected error for invalid source")
	}
	if err := Record(st, dir, testKey, Input{FilePath: "x.go", Summary: "x", Source: "hook"}); err == nil {
		t.Fatal("expected error for legacy 'hook' source")
	}
}

func TestRecordAcceptsLLMAndFallbackSources(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	for _, src := range []string{"llm", "fallback"} {
		if err := Record(st, dir, testKey, Input{
			FilePath:   src + ".go",
			Summary:    "summary from " + src,
			Source:     src,
			CommitHash: "abc",
		}); err != nil {
			t.Fatalf("Record source %s: %v", src, err)
		}
		rows, err := st.FileHistory(src+".go", 10)
		if err != nil {
			t.Fatalf("FileHistory: %v", err)
		}
		if len(rows) != 1 || rows[0].Source != src {
			t.Errorf("source not persisted: got %+v", rows[0])
		}
	}
}

func TestValidateFilePath(t *testing.T) {
	absPath := "/etc/cron.d/malicious"
	if runtime.GOOS == "windows" {
		absPath = `C:\etc\cron.d\malicious`
	}

	tests := []struct {
		path string
		want string // empty means valid
	}{
		{"internal/auth/token.go", ""},
		{"main.go", ""},
		{"a/b/c/d.go", ""},
		{"foo/../bar.go", ""}, // lexical resolution stays inside .githints/
		{"", "file path is required"},
		{absPath, "absolute"},
		{"../../etc/cron.d/malicious", "local, relative path"},
		{"../bar.go", "local, relative path"},
		{"..", "local, relative path"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := ValidateFilePath(tt.path)
			if tt.want == "" {
				if err != nil {
					t.Errorf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRecordRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	err = Record(st, dir, testKey, Input{
		FilePath: "../../etc/cron.d/malicious",
		Summary:  "escape",
		Source:   "agent",
	})
	if err == nil {
		t.Fatal("expected path traversal rejection, got nil")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("error should mention '..', got: %v", err)
	}
}

func TestScanSecrets(t *testing.T) {
	// Synthetic JWT shape built at runtime so secret scanners don't flag a
	// hard-coded test fixture as a leaked bearer token.
	syntheticJWT := "Authorization: Bearer eyJ" + strings.Repeat("A", 20) + "." + "eyJ" + strings.Repeat("B", 20) + ".signature"

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"clean text", "rotate signing key", false},
		{"aws access key", "leaked AKIAIOSFODNN7EXAMPLE in log", true},
		{"github pat", "token= ghp_abcdefghijklmnopqrstuvwxyz0123456789AB", true},
		{"private key block", "-----BEGIN RSA PRIVATE KEY-----\nMIIE", true},
		{"jwt", syntheticJWT, true},
		{"looks like jwt but isn't", "eyJshort.eyJshort.nopair", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSecrets(tt.text).Matched
			if got != tt.want {
				t.Errorf("ScanSecrets(%q) matched = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestRecordRejectsSecretInSummary(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	err = Record(st, dir, testKey, Input{
		FilePath: "config/secrets.go",
		Summary:  "hardcoded AKIAIOSFODNN7EXAMPLE by mistake",
		Source:   "agent",
	})
	if err == nil {
		t.Fatal("expected error when summary contains a secret, got nil")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("error should mention secret, got: %v", err)
	}

	rows, err := st.FileHistory("config/secrets.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows after rejection, got %d", len(rows))
	}
}

func TestRecordRejectsSecretInReason(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	err = Record(st, dir, testKey, Input{
		FilePath: "config/secrets.go",
		Summary:  "document the token format",
		Reason:   "uses ghp_abcdefghijklmnopqrstuvwxyz0123456789AB as an example",
		Source:   "agent",
	})
	if err == nil {
		t.Fatal("expected error when reason contains a secret, got nil")
	}
}

// Nothing between the MCP argument and SQLite bounded these before, so a
// multi-megabyte summary was stored and then re-rendered on every subsequent
// record for that file.
func TestRecordRejectsOversizedText(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	cases := []struct {
		name string
		in   Input
	}{
		{"summary", Input{FilePath: "a.go", Summary: strings.Repeat("x", MaxSummaryLen+1), Source: "agent"}},
		{"reason", Input{FilePath: "a.go", Summary: "ok", Reason: strings.Repeat("x", MaxReasonLen+1), Source: "agent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Record(st, dir, testKey, tc.in)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), "too long") {
				t.Errorf("error should mention length, got: %v", err)
			}
		})
	}

	// A summary exactly at the limit is allowed.
	if err := Record(st, dir, testKey, Input{
		FilePath: "a.go", Summary: strings.Repeat("x", MaxSummaryLen), Source: "agent",
	}); err != nil {
		t.Errorf("summary at the limit was rejected: %v", err)
	}
}

func TestBatchRecordRejectsOversizedBatch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	inputs := make([]Input, MaxBatchSize+1)
	for i := range inputs {
		inputs[i] = Input{FilePath: fmt.Sprintf("f%d.go", i), Summary: "x", Source: "agent"}
	}
	if err := BatchRecord(st, dir, testKey, inputs); err == nil {
		t.Fatal("expected oversized batch to be rejected")
	}

	count, err := st.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Errorf("rejected batch wrote %d rows", count)
	}
}

// The batch is one transaction: a row that fails validation must not leave
// earlier rows committed.
func TestBatchRecordRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	err = BatchRecord(st, dir, testKey, []Input{
		{FilePath: "good.go", Summary: "fine", Source: "agent"},
		{FilePath: "bad.go", Summary: "leaks AKIAIOSFODNN7EXAMPLE", Source: "agent"},
	})
	if err == nil {
		t.Fatal("expected batch to fail")
	}

	count, err := st.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Errorf("partial batch landed: %d rows, want 0", count)
	}
}

// The clock-tamper flag must be signed, so it has to be decided before the
// row's HMAC is computed.
func TestRecordSignsClockTamperFlag(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "a.go", Summary: "first", Source: "agent", RecordedAt: 100_000,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Far enough back to exceed ClockSkewTolerance.
	if err := Record(st, dir, testKey, Input{
		FilePath: "b.go", Summary: "second", Source: "agent", RecordedAt: 1_000,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.AllChanges()
	if err != nil {
		t.Fatalf("AllChanges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if !rows[1].ClockTamperWarning {
		t.Fatal("expected the backward-jumping row to be flagged")
	}
	// VerifyChain independently reports the backward timestamp, so look
	// specifically at whether the signature itself recomputes.
	hmacBroken := func(rows []store.Change) bool {
		for _, e := range integrity.VerifyChain(testKey, rows) {
			if strings.Contains(e.Problem, "hmac does not recompute") {
				return true
			}
		}
		return false
	}

	if hmacBroken(rows) {
		t.Fatal("signature should recompute for rows as written")
	}
	// Erasing the flag must now break the signature.
	rows[1].ClockTamperWarning = false
	if !hmacBroken(rows) {
		t.Fatal("clearing clock_tamper_warning went undetected by the HMAC")
	}
}

func TestRecordStampsBranch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("checkout", "-b", "feature/branch-stamp")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "main.go",
		Summary:  "add Run",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Branch != "feature/branch-stamp" {
		t.Errorf("branch = %q, want feature/branch-stamp", rows[0].Branch)
	}
	if rows[0].RecordedAt == 0 {
		t.Errorf("recorded_at is zero")
	}
	if rows[0].HMAC == "" {
		t.Errorf("hmac is empty")
	}
}

func TestRecordHMACChainsRows(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "a.go",
		Summary:  "first",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if err := Record(st, dir, testKey, Input{
		FilePath: "b.go",
		Summary:  "second",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record second: %v", err)
	}

	rows, err := st.AllChanges()
	if err != nil {
		t.Fatalf("AllChanges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].PrevHMAC != "" {
		t.Errorf("first row prev_hmac = %q, want empty", rows[0].PrevHMAC)
	}
	if rows[1].PrevHMAC != rows[0].HMAC {
		t.Errorf("second row prev_hmac = %q, want first row hmac %q", rows[1].PrevHMAC, rows[0].HMAC)
	}
}

func TestRecordAgentID(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "auth.go",
		Summary:  "fix auth",
		AgentID:  "claude-code-session-xyz",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("auth.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 || rows[0].AgentID != "claude-code-session-xyz" {
		t.Fatalf("agent_id not persisted: %+v", rows[0])
	}
}

func TestRecordCapturesDiffHash(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".githints"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := Record(st, dir, testKey, Input{
		FilePath: "main.go",
		Summary:  "add Run",
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows, err := st.FileHistory("main.go", 10)
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].DiffHash == "" {
		t.Errorf("diff_hash is empty")
	}
}

func TestBatchRecord(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := BatchRecord(st, dir, testKey, []Input{
		{FilePath: "a.go", Summary: "first", Source: "agent"},
		{FilePath: "b.go", Summary: "second", Source: "agent"},
		{FilePath: "a.go", Summary: "third", Source: "agent"},
	}); err != nil {
		t.Fatalf("BatchRecord: %v", err)
	}

	for _, f := range []string{"a.go", "b.go"} {
		rows, err := st.FileHistory(f, 10)
		if err != nil {
			t.Fatalf("FileHistory %s: %v", f, err)
		}
		if len(rows) == 0 {
			t.Fatalf("expected rows for %s", f)
		}
	}

	count, err := st.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	if _, err := os.Stat(filepath.Join(dir, ".githints", "CHANGES.md")); err != nil {
		t.Fatalf("changelog not rendered: %v", err)
	}
}
