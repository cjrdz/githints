package hooks

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/risk"
	"github.com/cjrdz/githints/internal/store"
)

// reviewRepo is an initialized repository with one commit and an auth file
// staged: a high-risk candidate.
func reviewRepo(t *testing.T, gate string) string {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)
	if gate != "" {
		cfg := `{"review":{"gate":"` + gate + `"}}`
		if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "README.md", "# x\n")
	clitest.RunGit(t, dir, "commit", "-m", "initial")
	write(t, dir, "internal/auth/token.go", "package auth\n")
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", rel)
}

func review(t *testing.T, verdict string, lenses ...string) store.Change {
	t.Helper()
	root, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := risk.RecordReview(st, key, risk.Inputs{Store: st, Root: root}, risk.ReviewInput{
		Verdict: verdict, Findings: "checked", Lenses: lenses, AgentID: "reviewer",
	})
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}
	return c
}

func receipts(t *testing.T) risk.Receipts {
	t.Helper()
	root, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r, err := risk.CurrentReceipts(st, root, risk.High)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReviewGateBlocksOnlyWhenAsked(t *testing.T) {
	for gate, wantBlock := range map[string]bool{"": false, "warn": false, "block": true, "bogus": false} {
		t.Run("gate="+gate, func(t *testing.T) {
			reviewRepo(t, gate)
			err := PreCommit()
			if blocked := errors.Is(err, errPrecommitBlocked); blocked != wantBlock {
				t.Errorf("blocked = %v (%v), want %v", blocked, err, wantBlock)
			}
		})
	}
}

// Whether review applies is the user's choice, made in the environment; a
// config.json from a clone cannot force it.
func TestReviewGateOffSwitch(t *testing.T) {
	reviewRepo(t, "block")
	t.Setenv("GITHINTS_REVIEW", "0")
	if err := PreCommit(); err != nil {
		t.Errorf("GITHINTS_REVIEW=0 still gated the commit: %v", err)
	}
}

func TestApprovedReceiptLetsTheCommitThrough(t *testing.T) {
	reviewRepo(t, "block")
	review(t, store.VerdictApproved, risk.Lenses...)
	if err := PreCommit(); err != nil {
		t.Fatalf("an approved receipt for the staged tree was not honored: %v", err)
	}
}

func TestReceiptThatIsNotAnApprovalStillBlocks(t *testing.T) {
	for name, lenses := range map[string][]string{"escalated": risk.Lenses, "too few lenses": {"risk"}} {
		t.Run(name, func(t *testing.T) {
			reviewRepo(t, "block")
			verdict := store.VerdictApproved
			if name == "escalated" {
				verdict = store.VerdictEscalated
			}
			review(t, verdict, lenses...)
			if err := PreCommit(); !errors.Is(err, errPrecommitBlocked) {
				t.Errorf("got %v, want the commit blocked", err)
			}
		})
	}
}

// Changing what is staged after the review leaves the receipt behind: it was
// for a different tree.
func TestRestagingMakesTheReceiptStale(t *testing.T) {
	dir := reviewRepo(t, "block")
	review(t, store.VerdictApproved, risk.Lenses...)
	write(t, dir, "internal/auth/token.go", "package auth\n\nvar x = 1\n")
	r := receipts(t)
	if r.Current != nil || r.Stale != 1 || r.Valid {
		t.Errorf("after re-staging: current=%v stale=%d valid=%v, want none, 1, false", r.Current, r.Stale, r.Valid)
	}
	if err := PreCommit(); !errors.Is(err, errPrecommitBlocked) {
		t.Errorf("a stale receipt let the commit through: %v", err)
	}
}

// The commit uses the receipt up, it lands under the commit's anchor, and
// verify checks it against the commit. Staging the same tree again on the
// same base (a revert of the revert) does not bring it back.
func TestCommitUsesTheReceiptUp(t *testing.T) {
	dir := reviewRepo(t, "block")
	rec := review(t, store.VerdictApproved, risk.Lenses...)
	clitest.RunGit(t, dir, "commit", "-m", "auth")
	if err := HookRun(); err != nil {
		t.Fatalf("HookRun: %v", err)
	}
	hash, _ := gitutil.LastCommitHash()

	_, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, ok, err := st.GetChange(rec.ID)
	if err != nil || !ok {
		t.Fatalf("receipt gone: %v", err)
	}
	if got.CommitHash != hash {
		t.Fatalf("receipt commit = %q, want %s", got.CommitHash, hash)
	}
	all, err := st.AllChanges()
	if err != nil {
		t.Fatal(err)
	}
	if problems, err := integrity.VerifyReviews(all); err != nil || len(problems) != 0 {
		t.Errorf("VerifyReviews: %v %v", problems, err)
	}
	notes, err := gitutil.ReadNotes("refs/notes/githints")
	if err != nil {
		t.Fatal(err)
	}
	if rep := integrity.VerifyAnchors(all, notes); len(rep.Problems) != 0 {
		t.Errorf("anchors: %v", rep.Problems)
	}

	// Moving the receipt to another commit is caught.
	clitest.RunGit(t, dir, "commit", "--allow-empty", "-m", "other")
	other, _ := gitutil.LastCommitHash()
	moved := got
	moved.CommitHash = other
	if problems, _ := integrity.VerifyReviews([]store.Change{moved}); len(problems) != 1 {
		t.Errorf("a receipt moved to another commit was not reported: %v", problems)
	}

	// Revert to the reviewed tree's base and stage the auth file again: the
	// tree and base recur, the used receipt must not.
	clitest.RunGit(t, dir, "reset", "-q", "--hard", "HEAD~2")
	write(t, dir, "internal/auth/token.go", "package auth\n")
	if r := receipts(t); r.Current != nil || r.Valid {
		t.Errorf("a used receipt applied again: %+v", r.Current)
	}
}

func TestReviewNeedsSomethingStaged(t *testing.T) {
	dir := reviewRepo(t, "")
	clitest.RunGit(t, dir, "reset", "-q")
	root, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := risk.RecordReview(st, nil, risk.Inputs{Store: st, Root: root}, risk.ReviewInput{Verdict: store.VerdictApproved}); err == nil {
		t.Error("a review of nothing was recorded")
	}
}

func TestReviewInputIsChecked(t *testing.T) {
	reviewRepo(t, "")
	root, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := risk.Inputs{Store: st, Root: root}
	for name, rv := range map[string]risk.ReviewInput{
		"bad verdict":           {Verdict: "lgtm"},
		"escalated, no finding": {Verdict: store.VerdictEscalated},
		"unknown lens":          {Verdict: store.VerdictApproved, Lenses: []string{"vibes"}},
		"secret in findings":    {Verdict: store.VerdictApproved, Findings: "key AKIA" + "ABCDEFGHIJKLMNOP"},
	} {
		if _, _, err := risk.RecordReview(st, nil, in, rv); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSecretGateNamesTheLineNotTheValue(t *testing.T) {
	dir := reviewRepo(t, "")
	key := "AKIA" + "ABCDEFGHIJKLMNOP"
	write(t, dir, "config/aws.go", "package config\n\nconst id = \""+key+"\"\n")
	out, err := clitest.CaptureStderr(t, PreCommit)
	if err != nil {
		t.Fatalf("warn mode blocked: %v", err)
	}
	if !strings.Contains(out, "config/aws.go:3") {
		t.Errorf("finding not named:\n%s", out)
	}
	if strings.Contains(out, key) {
		t.Error("the credential was printed")
	}
	t.Setenv("GITHINTS_SECRET_BLOCK", "1")
	if err := PreCommit(); !errors.Is(err, errPrecommitBlocked) {
		t.Errorf("GITHINTS_SECRET_BLOCK=1 did not block: %v", err)
	}
}

// A line points at the commit that last changed it, the change recorded for
// the file there, and the review that commit used; renames are followed, and
// a line changed in the working tree says so.
func TestExplainLine(t *testing.T) {
	dir := reviewRepo(t, "")
	recordAgent(t, "internal/auth/token.go", "added the token package")
	review(t, store.VerdictApproved, risk.Lenses...)
	clitest.RunGit(t, dir, "commit", "-m", "auth: add token")
	if err := HookRun(); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "mv", "internal/auth/token.go", "internal/auth/tokens.go")
	clitest.RunGit(t, dir, "commit", "-m", "rename")

	_, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	text, err := st.ExplainLine("internal/auth/tokens.go", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth: add token", "added the token package", "(the file was internal/auth/token.go then)", "review receipt"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "internal/auth/tokens.go"), []byte("package auth // edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text, err = st.ExplainLine("internal/auth/tokens.go", 1); err != nil || !strings.Contains(text, "not committed yet") {
		t.Errorf("uncommitted line: %v\n%s", err, text)
	}
	if _, err := st.ExplainLine("internal/auth/tokens.go", 99); err == nil {
		t.Error("a line past the end of the file was explained")
	}
}

// Someone with write access to store.db turns an escalation into a full
// approval. verify would report it after the fact; the gate must refuse it
// before the commit, by checking the receipt's signature.
func TestForgedApprovalDoesNotPassTheGate(t *testing.T) {
	dir := reviewRepo(t, "block")
	rec := review(t, store.VerdictEscalated, "risk")
	db, err := sql.Open("sqlite", filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE changes SET verdict = 'approved', summary = ? WHERE id = ?`,
		risk.ReceiptSummary(store.VerdictApproved, risk.High, risk.Lenses), rec.ID); err != nil {
		t.Fatal(err)
	}
	_ = db.Close() // the forgery is written; the rest reads through the store

	r := receipts(t)
	if r.Valid || !strings.Contains(r.Why, "does not match its signature") {
		t.Errorf("forged receipt: valid=%v why=%q", r.Valid, r.Why)
	}
	if err := PreCommit(); !errors.Is(err, errPrecommitBlocked) {
		t.Errorf("a forged approval let the commit through: %v", err)
	}
}
