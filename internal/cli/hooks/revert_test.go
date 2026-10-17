package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

// revertRepo is a repository with one recorded feature commit that touches two
// files: a.go is new, base.go is modified.
type revertRepo struct {
	t       *testing.T
	dir     string
	feature string // hash of the feature commit
}

func newRevertRepo(t *testing.T) *revertRepo {
	t.Helper()
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)
	r := &revertRepo{t: t, dir: dir}

	r.write("base.go", "package base\n")
	clitest.RunGit(t, dir, "add", "base.go")
	clitest.RunGit(t, dir, "commit", "-m", "base")

	r.write("base.go", "package base\n\nfunc F() {}\n")
	r.write("a.go", "package a\n")
	recordAgent(t, "base.go", "add F")
	recordAgent(t, "a.go", "add package a")
	clitest.RunGit(t, dir, "add", "base.go", "a.go")
	clitest.RunGit(t, dir, "commit", "-m", "feature")
	if err := HookRun(); err != nil {
		t.Fatalf("HookRun: %v", err)
	}
	r.feature, _ = gitutil.LastCommitHash()
	return r
}

func (r *revertRepo) write(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *revertRepo) hook() {
	r.t.Helper()
	if err := HookRun(); err != nil {
		r.t.Fatalf("HookRun: %v", err)
	}
}

// outcomes returns every outcome row, oldest first, plus the whole log.
func (r *revertRepo) outcomes() (outs, all []store.Change) {
	r.t.Helper()
	st, err := store.Open(filepath.Join(r.dir, ".githints", "store.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	all, err = st.AllChanges()
	if err != nil {
		r.t.Fatal(err)
	}
	for _, c := range all {
		if c.Kind == store.KindOutcome {
			outs = append(outs, c)
		}
	}
	return outs, all
}

func (r *revertRepo) verifyChain(all []store.Change) {
	r.t.Helper()
	root, st, err := core.OpenInitialized()
	if err != nil {
		r.t.Fatal(err)
	}
	st.Close()
	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		r.t.Fatal(err)
	}
	if errs := integrity.VerifyChain(key, all); len(errs) != 0 {
		r.t.Errorf("chain does not verify: %+v", errs)
	}
}

func TestRevertCommitMarksTheChangesReverted(t *testing.T) {
	r := newRevertRepo(t)
	clitest.RunGit(t, r.dir, "revert", "--no-edit", "HEAD")
	r.hook()
	revert, _ := gitutil.LastCommitHash()

	outs, all := r.outcomes()
	if len(outs) != 2 {
		t.Fatalf("%d outcome rows, want one per reverted change", len(outs))
	}
	targets := map[string]bool{}
	for _, o := range outs {
		targets[o.FilePath] = true
		if o.OutcomeState != store.OutcomeReverted {
			t.Errorf("state = %q, want reverted", o.OutcomeState)
		}
		if o.Source != "fallback" {
			t.Errorf("source = %q: an inferred outcome must not look agent-asserted", o.Source)
		}
		if o.CommitHash != revert {
			t.Errorf("commit_hash = %q, want the revert commit %q", o.CommitHash, revert)
		}
		if !strings.Contains(o.Summary, revert[:8]) || strings.Contains(o.Summary, "feature") {
			t.Errorf("note = %q: should name the reverting commit and carry no message text", o.Summary)
		}
	}
	if !targets["a.go"] || !targets["base.go"] {
		t.Errorf("outcomes for %v, want a.go and base.go", targets)
	}
	r.verifyChain(all)

	// The outcome shows under the change it is about, flagged as inferred.
	st, err := store.Open(filepath.Join(r.dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hist, _ := st.FileHistory("a.go", 10)
	var found bool
	for _, c := range hist {
		if c.Source == "agent" && c.LatestOutcome != nil && c.LatestOutcome.OutcomeState == store.OutcomeReverted {
			found = true
		}
	}
	if !found {
		t.Errorf("history does not show the reverted outcome: %+v", hist)
	}

	// The revert commit's per-commit anchor covers the outcomes it caused.
	rows, _ := st.ChangesForCommit(revert)
	n := 0
	for _, c := range rows {
		if c.Kind == store.KindOutcome {
			n++
		}
	}
	if n != 2 {
		t.Errorf("revert commit's rows hold %d outcomes, want 2", n)
	}
}

// Running the hook again for the same commit must not record the outcome twice.
func TestRevertDetectionIsIdempotent(t *testing.T) {
	r := newRevertRepo(t)
	clitest.RunGit(t, r.dir, "revert", "--no-edit", "HEAD")
	r.hook()
	r.hook()
	if outs, _ := r.outcomes(); len(outs) != 2 {
		t.Errorf("%d outcome rows after running the hook twice, want 2", len(outs))
	}
}

// A message is a claim. One naming a real commit while changing nothing it did
// must not mark anything.
func TestForgedRevertMessageMarksNothing(t *testing.T) {
	r := newRevertRepo(t)

	// Touches a file the feature touched, but does not restore it.
	r.write("a.go", "package a\n\nvar Different = 1\n")
	clitest.RunGit(t, r.dir, "add", "a.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "tidy\n\nThis reverts commit "+r.feature+".")
	r.hook()

	// Touches nothing the feature touched.
	r.write("other.go", "package other\n")
	clitest.RunGit(t, r.dir, "add", "other.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "unrelated\n\nThis reverts commit "+r.feature+".")
	r.hook()

	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Errorf("forged revert claims produced %d outcomes: %+v", len(outs), outs)
	}
}

// Only the files the commit really restored are marked.
func TestPartialRevertMarksOnlyRestoredFiles(t *testing.T) {
	r := newRevertRepo(t)
	clitest.RunGit(t, r.dir, "revert", "--no-commit", "HEAD")
	clitest.RunGit(t, r.dir, "checkout", "HEAD", "--", "base.go") // keep the base.go change
	clitest.RunGit(t, r.dir, "commit", "-m", "Revert part of feature\n\nThis reverts commit "+r.feature+".")
	r.hook()

	outs, _ := r.outcomes()
	if len(outs) != 1 || outs[0].FilePath != "a.go" {
		t.Fatalf("outcomes = %+v, want only a.go", outs)
	}
}

func TestRevertNamingUnknownCommitIsHarmless(t *testing.T) {
	r := newRevertRepo(t)
	r.write("other.go", "package other\n")
	clitest.RunGit(t, r.dir, "add", "other.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "odd\n\nThis reverts commit "+strings.Repeat("0", 40)+".")
	r.hook() // must not fail, and nothing to mark
	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Errorf("outcomes for a commit that does not exist: %+v", outs)
	}
}

// A revert of a commit nobody recorded has nothing to mark.
func TestRevertOfUnrecordedCommitIsHarmless(t *testing.T) {
	r := newRevertRepo(t)
	r.write("plain.go", "package plain\n")
	clitest.RunGit(t, r.dir, "add", "plain.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "plain")
	// No hook run for it, so it has no rows.
	clitest.RunGit(t, r.dir, "revert", "--no-edit", "HEAD")
	r.hook()
	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Errorf("outcomes = %+v, want none", outs)
	}
}

// A file can already be back to what it was for an unrelated reason. A later
// commit claiming the revert did not do that, so it does not get the credit.
func TestRevertClaimOnlyCountsFilesThatCommitTouched(t *testing.T) {
	r := newRevertRepo(t)

	// Remove a.go by hand, with no claim: a.go now matches its pre-feature state.
	clitest.RunGit(t, r.dir, "rm", "-q", "a.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "drop a.go")
	r.hook()

	// A commit that never touches a.go claims to revert the feature.
	r.write("other.go", "package other\n")
	clitest.RunGit(t, r.dir, "add", "other.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "other\n\nThis reverts commit "+r.feature+".")
	r.hook()

	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Errorf("a commit that did not touch the file was credited with reverting it: %+v", outs)
	}
}

// git quotes a non-ASCII path in `--name-only` output unless asked not to, and a
// lookup of the quoted text finds nothing on either side of a revert: both are
// "absent", which reads as "restored". A commit message alone could then mark
// any change to such a file reverted.
func TestForgedRevertCannotMarkFilesWithUnusualNames(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)
	r := &revertRepo{t: t, dir: dir}

	names := []string{"é.txt", "日本語.txt", "has space.txt", " lead.txt"}
	if runtime.GOOS != "windows" { // '"' is not legal in a Windows file name
		names = append(names, `quo"te.txt`)
	}
	for _, n := range names {
		r.write(n, "v1\n")
	}
	clitest.RunGit(t, dir, "add", "--", ".")
	clitest.RunGit(t, dir, "reset", "-q", "--", "AGENTS.md", "CLAUDE.md", ".gitignore")
	clitest.RunGit(t, dir, "commit", "-m", "base")

	for _, n := range names {
		r.write(n, "v2\n")
		recordAgent(t, n, "changed "+n)
	}
	clitest.RunGit(t, dir, append([]string{"add", "--"}, names...)...)
	clitest.RunGit(t, dir, "commit", "-m", "feature")
	r.hook()
	feature, _ := gitutil.LastCommitHash()

	// Without the fix the feature commit's own agent rows are never claimed
	// either, because the recorded name and git's quoted name differ.
	if rows := commitRows(t, dir); len(rows) != len(names) {
		t.Fatalf("the feature commit claimed %d rows, want %d", len(rows), len(names))
	}

	// A commit that sets every file to something else and merely claims a revert.
	for _, n := range names {
		r.write(n, "v3\n")
	}
	clitest.RunGit(t, dir, append([]string{"add", "--"}, names...)...)
	clitest.RunGit(t, dir, "commit", "-m", "forged\n\nThis reverts commit "+feature+".")
	r.hook()
	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Fatalf("a forged revert marked %d changes to oddly named files: %+v", len(outs), outs)
	}
}

func TestGenuineRevertMarksFilesWithUnusualNames(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)
	r := &revertRepo{t: t, dir: dir}

	names := []string{"é.txt", "has space.txt"}
	for _, n := range names {
		r.write(n, "v1\n")
	}
	clitest.RunGit(t, dir, "add", "--", names[0], names[1])
	clitest.RunGit(t, dir, "commit", "-m", "base")
	for _, n := range names {
		r.write(n, "v2\n")
		recordAgent(t, n, "changed "+n)
	}
	clitest.RunGit(t, dir, "add", "--", names[0], names[1])
	clitest.RunGit(t, dir, "commit", "-m", "feature")
	r.hook()
	clitest.RunGit(t, dir, "revert", "--no-edit", "HEAD")
	r.hook()

	outs, all := r.outcomes()
	if len(outs) != len(names) {
		t.Fatalf("%d outcomes for a genuine revert of %d oddly named files: %+v", len(outs), len(names), outs)
	}
	got := map[string]bool{}
	for _, o := range outs {
		got[o.FilePath] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Errorf("no outcome for %q (got %v)", n, got)
		}
	}
	r.verifyChain(all)
}

// Reverting a rename shows the OLD path in the revert commit's diff, while the
// row being undone was recorded under the new one.
func TestRevertOfARenameIsDetected(t *testing.T) {
	r := newRevertRepo(t)
	clitest.RunGit(t, r.dir, "mv", "a.go", "renamed.go")
	recordAgent(t, "renamed.go", "renamed a.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "rename")
	r.hook()
	rename, _ := gitutil.LastCommitHash()

	// A commit that claims to revert the rename but leaves renamed.go in place.
	r.write("other.go", "package other\n")
	clitest.RunGit(t, r.dir, "add", "other.go")
	clitest.RunGit(t, r.dir, "commit", "-m", "claim\n\nThis reverts commit "+rename+".")
	r.hook()
	if outs, _ := r.outcomes(); len(outs) != 0 {
		t.Fatalf("a forged claim marked a renamed file: %+v", outs)
	}

	clitest.RunGit(t, r.dir, "revert", "--no-edit", rename)
	r.hook()
	outs, all := r.outcomes()
	if len(outs) != 1 || outs[0].FilePath != "renamed.go" || outs[0].OutcomeState != store.OutcomeReverted {
		t.Fatalf("outcomes = %+v, want renamed.go marked reverted", outs)
	}
	r.verifyChain(all)
}
