// Package doctor implements the doctor CLI command.
package doctor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/mcpcfg"
	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/managed"
	"github.com/cjrdz/githints/internal/store"
)

// Level orders doctor results; Fail makes doctor exit non-zero.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelFail
)

func (l Level) String() string {
	return [...]string{"ok  ", "warn", "FAIL"}[l]
}

// Result is one doctor check.
type Result struct {
	Level  Level
	Name   string
	Detail string
	Fix    string
}

// Run checks everything githints depends on and says how to fix what is wrong.
func Run() error {
	results := runChecks()
	failed := 0
	for _, r := range results {
		fmt.Printf("%s  %-10s %s\n", r.Level, r.Name, r.Detail)
		if r.Fix != "" && r.Level != LevelOK {
			fmt.Printf("            fix: %s\n", r.Fix)
		}
		if r.Level == LevelFail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func runChecks() []Result {
	var out []Result
	add := func(level Level, name, detail, fix string) {
		out = append(out, Result{level, name, detail, fix})
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		add(LevelFail, "git", "git is not on PATH", "install git; githints shells out to it for everything")
		return out
	}
	gitVersion, _ := exec.Command(gitPath, "--version").Output()
	add(LevelOK, "git", strings.TrimSpace(string(gitVersion)), "")

	root, err := gitutil.RepoRoot()
	if err != nil {
		add(LevelFail, "repo", "not inside a git repository", "cd into the repository, or run `git init`")
		return out
	}
	add(LevelOK, "repo", root, "")

	dbPath := filepath.Join(root, ".githints", "store.db")
	if _, err := os.Stat(dbPath); err != nil {
		add(LevelFail, "init", "githints is not set up here", "githints init")
		return out
	}

	out = append(out, CheckHooks(root)...)

	cfg, cfgErr := config.Load(root)
	if cfgErr != nil {
		add(LevelFail, "config", cfgErr.Error(), "fix or delete .githints/config.json")
	} else {
		add(LevelOK, "config", "parsed", "")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		add(LevelFail, "store", err.Error(), "see the message above; the store can be moved aside and re-created with `githints init`")
		return out
	}
	defer st.Close()
	if problems, err := st.IntegrityCheck(); err != nil || len(problems) > 0 {
		detail := fmt.Sprintf("integrity_check: %v %s", err, strings.Join(problems, "; "))
		add(LevelFail, "store", detail, "restore .githints/store.db from a backup; the rendered markdown is regenerated from it")
	} else {
		add(LevelOK, "store", "integrity_check ok", "")
	}

	out = append(out, checkIntegrity(root, st)...)

	if cfgErr == nil && cfg.Index.Enabled {
		out = append(out, checkIndex(root))
	}

	out = append(out, checkMCP(root))
	return out
}

// CheckHooks reports whether the post-commit and pre-commit hooks are
// installed and executable. It wraps managed.CheckHooks into doctor Results.
func CheckHooks(root string) []Result {
	statuses, err := managed.CheckHooks(root)
	if err != nil {
		return []Result{{LevelFail, "hooks", err.Error(), "githints init"}}
	}
	var out []Result
	for _, s := range statuses {
		var level Level
		switch s.Level {
		case managed.HookOK:
			level = LevelOK
		case managed.HookWarn:
			level = LevelWarn
		case managed.HookFail:
			level = LevelFail
		}
		// Group per-hook statuses under the "hooks" check name; the detail
		// still names the specific hook.
		out = append(out, Result{level, "hooks", s.Detail, s.Fix})
	}
	return out
}

func checkIntegrity(root string, st *store.Store) []Result {
	rows, err := st.AllChanges()
	if err != nil {
		return []Result{{LevelFail, "chain", err.Error(), ""}}
	}
	// ExistingKey, not KeyFromRepo: a diagnostic must not create, adopt or
	// move a salt as a side effect.
	key, saltPath, err := integrity.ExistingKey(root)
	if errors.Is(err, integrity.ErrSaltMissing) {
		level := LevelFail
		if len(rows) == 0 {
			level = LevelWarn // created on first record
		}
		return []Result{{level, "salt", err.Error(), "githints salt import <file>  (from `githints salt export` where it still exists)"}}
	}
	if err != nil {
		return []Result{{LevelFail, "salt", err.Error(), ""}}
	}
	out := []Result{{LevelOK, "salt", saltPath, ""}}

	errs := integrity.VerifyChain(key, rows)
	switch {
	case len(rows) == 0:
		out = append(out, Result{LevelOK, "chain", "no rows yet", ""})
	case len(errs) == 0:
		out = append(out, Result{LevelOK, "chain", fmt.Sprintf("%d row(s) verify", len(rows)), ""})
	case core.KeyChangedHint(rows, errs) != "":
		email, _ := gitutil.UserEmail()
		out = append(out, Result{LevelFail, "chain", fmt.Sprintf("every row fails: the key changed (user.email is now %q, or the salt was replaced)", email),
			"restore the old user.email or salt; otherwise `githints rotate-salt -force`"})
	default:
		out = append(out, Result{LevelFail, "chain", fmt.Sprintf("%d problem(s) in %d row(s)", len(errs), len(rows)), "githints verify (shows each one)"})
	}

	// The anchors are what catches a log that was edited and re-signed with
	// the salt, which the chain check above cannot.
	notes, err := gitutil.ReadNotes("refs/notes/githints")
	if err != nil {
		return append(out, Result{LevelWarn, "anchors", err.Error(), ""})
	}
	rep := integrity.VerifyAnchors(rows, notes)
	switch {
	case len(rep.Problems) > 0:
		out = append(out, Result{LevelFail, "anchors", fmt.Sprintf("%d problem(s): %s", len(rep.Problems), rep.Problems[0]), "githints verify (shows each one)"})
	case rep.Notes == 0 && len(rows) > 0:
		out = append(out, Result{LevelWarn, "anchors", "no Merkle anchors in refs/notes/githints yet", "they are written by the post-commit hook; check the hooks above"})
	case rep.Notes > 0:
		out = append(out, Result{LevelOK, "anchors", fmt.Sprintf("%d note(s) match the log", rep.Checked), ""})
	}
	return out
}

func checkIndex(root string) Result {
	dbPath := lang.IndexDBPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return Result{LevelWarn, "index", "enabled but never built", "githints index"}
	}
	db, err := index.Open(dbPath)
	if err != nil {
		return Result{LevelFail, "index", err.Error(), "delete .githints/index.db and run `githints index`"}
	}
	defer db.Close()
	at, err := db.LastIndexedAt()
	if err != nil || at == 0 {
		return Result{LevelWarn, "index", "enabled but never built", "githints index"}
	}
	age := time.Since(time.Unix(at, 0)).Round(time.Minute)
	if v, err := db.IndexResolverVersion(); err == nil && v < index.ResolverVersion {
		return Result{LevelWarn, "index", fmt.Sprintf("last indexed %s ago, by an older githints: import links may be missing", age), "githints index"}
	}
	return Result{LevelOK, "index", fmt.Sprintf("last indexed %s ago", age), ""}
}

// checkMCP looks for githints in the project-level config of every client
// setup knows. Global configs are not checked; that is a warning, not a
// failure, since the CLI works without any.
func checkMCP(root string) Result {
	var found []string
	for _, p := range mcpcfg.ProjectConfigFiles(root) {
		data, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(data), "githints") {
			found = append(found, mcpcfg.DisplayPath(root, p))
		}
	}
	if len(found) == 0 {
		return Result{LevelWarn, "mcp", "no project MCP config registers githints (global configs are not checked)",
			"githints setup"}
	}
	return Result{LevelOK, "mcp", "registered in " + strings.Join(found, ", "), ""}
}
