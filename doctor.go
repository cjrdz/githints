package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/store"
)

// checkLevel orders doctor results; fail makes doctor exit non-zero.
type checkLevel int

const (
	levelOK checkLevel = iota
	levelWarn
	levelFail
)

func (l checkLevel) String() string {
	return [...]string{"ok  ", "warn", "FAIL"}[l]
}

type checkResult struct {
	level  checkLevel
	name   string
	detail string
	fix    string
}

// cmdDoctor checks everything githints depends on and says how to fix what
// is wrong. Each failure mode it covers used to surface somewhere else, if at
// all: a hook pointing at a deleted binary only warns after every commit, a
// lost salt shows up as every row failing verify, and an unregistered MCP
// server shows up as an agent that never records anything.
func cmdDoctor() error {
	results := runDoctor()
	failed := 0
	for _, r := range results {
		fmt.Printf("%s  %-10s %s\n", r.level, r.name, r.detail)
		if r.fix != "" && r.level != levelOK {
			fmt.Printf("            fix: %s\n", r.fix)
		}
		if r.level == levelFail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func runDoctor() []checkResult {
	var out []checkResult
	add := func(level checkLevel, name, detail, fix string) {
		out = append(out, checkResult{level, name, detail, fix})
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		add(levelFail, "git", "git is not on PATH", "install git; githints shells out to it for everything")
		return out
	}
	gitVersion, _ := exec.Command(gitPath, "--version").Output()
	add(levelOK, "git", strings.TrimSpace(string(gitVersion)), "")

	root, err := gitutil.RepoRoot()
	if err != nil {
		add(levelFail, "repo", "not inside a git repository", "cd into the repository, or run `git init`")
		return out
	}
	add(levelOK, "repo", root, "")

	dbPath := filepath.Join(root, ".githints", "store.db")
	if _, err := os.Stat(dbPath); err != nil {
		add(levelFail, "init", "githints is not set up here", "githints init")
		return out
	}

	out = append(out, checkHooks(root)...)

	cfg, cfgErr := config.Load(root)
	if cfgErr != nil {
		add(levelFail, "config", cfgErr.Error(), "fix or delete .githints/config.json")
	} else {
		add(levelOK, "config", "parsed", "")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		add(levelFail, "store", err.Error(), "see the message above; the store can be moved aside and re-created with `githints init`")
		return out
	}
	defer st.Close()
	if problems, err := st.IntegrityCheck(); err != nil || len(problems) > 0 {
		detail := fmt.Sprintf("integrity_check: %v %s", err, strings.Join(problems, "; "))
		add(levelFail, "store", detail, "restore .githints/store.db from a backup; the rendered markdown is regenerated from it")
	} else {
		add(levelOK, "store", "integrity_check ok", "")
	}

	out = append(out, checkIntegrity(root, st)...)

	if cfgErr == nil && cfg.Index.Enabled {
		out = append(out, checkIndex(root))
	}

	out = append(out, checkMCP(root))
	return out
}

// hookBinRe extracts the recorded binary path from a current hook script.
var hookBinRe = regexp.MustCompile(`(?m)^githints_bin='((?:[^']|'\\'')*)'$`)

func checkHooks(root string) []checkResult {
	dir, err := gitutil.HooksDir(root)
	if err != nil {
		return []checkResult{{levelFail, "hooks", err.Error(), "githints init"}}
	}
	var out []checkResult
	for _, name := range []string{"post-commit", "pre-commit"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out = append(out, checkResult{levelFail, "hooks", name + " is not installed in " + dir, "githints init"})
			continue
		case err != nil:
			out = append(out, checkResult{levelFail, "hooks", err.Error(), ""})
			continue
		case !strings.Contains(string(data), managedHookMarker):
			out = append(out, checkResult{levelWarn, "hooks", name + " exists but was not installed by githints", "githints init -chain (keeps it and runs it first)"})
			continue
		}
		m := hookBinRe.FindStringSubmatch(string(data))
		if m == nil {
			out = append(out, checkResult{levelWarn, "hooks", name + " was written by an older githints", "githints init (rewrites it)"})
			continue
		}
		bin := strings.ReplaceAll(m[1], `'\''`, `'`)
		if fi, err := os.Stat(filepath.FromSlash(bin)); err != nil || fi.Mode()&0o111 == 0 {
			if _, perr := exec.LookPath("githints"); perr == nil {
				out = append(out, checkResult{levelWarn, "hooks", name + " points at " + bin + ", which is gone; it falls back to githints on PATH", "githints init (repoints it)"})
			} else {
				out = append(out, checkResult{levelFail, "hooks", name + " points at " + bin + ", which is gone, and githints is not on PATH", "githints init"})
			}
			continue
		}
		if runtime.GOOS == "windows" {
			// Execute bits mean nothing on Windows; Git for Windows runs hooks through sh.
			out = append(out, checkResult{levelOK, "hooks", name + " -> " + bin, ""})
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.Mode()&0o111 == 0 {
			out = append(out, checkResult{levelFail, "hooks", name + " is not executable, so git skips it", "chmod +x " + path})
			continue
		}
		out = append(out, checkResult{levelOK, "hooks", name + " -> " + bin, ""})
	}
	return out
}

func checkIntegrity(root string, st *store.Store) []checkResult {
	rows, err := st.AllChanges()
	if err != nil {
		return []checkResult{{levelFail, "chain", err.Error(), ""}}
	}
	// ExistingKey, not KeyFromRepo: a diagnostic must not create, adopt or
	// move a salt as a side effect.
	key, saltPath, err := integrity.ExistingKey(root)
	if errors.Is(err, integrity.ErrSaltMissing) {
		level := levelFail
		if len(rows) == 0 {
			level = levelWarn // created on first record
		}
		return []checkResult{{level, "salt", err.Error(), "githints salt import <file>  (from `githints salt export` where it still exists)"}}
	}
	if err != nil {
		return []checkResult{{levelFail, "salt", err.Error(), ""}}
	}
	out := []checkResult{{levelOK, "salt", saltPath, ""}}

	errs := integrity.VerifyChain(key, rows)
	switch {
	case len(rows) == 0:
		out = append(out, checkResult{levelOK, "chain", "no rows yet", ""})
	case len(errs) == 0:
		out = append(out, checkResult{levelOK, "chain", fmt.Sprintf("%d row(s) verify", len(rows)), ""})
	case keyChangedHint(rows, errs) != "":
		email, _ := gitutil.UserEmail()
		out = append(out, checkResult{levelFail, "chain", fmt.Sprintf("every row fails: the key changed (user.email is now %q, or the salt was replaced)", email),
			"restore the old user.email or salt; otherwise `githints rotate-salt -force`"})
	default:
		out = append(out, checkResult{levelFail, "chain", fmt.Sprintf("%d problem(s) in %d row(s)", len(errs), len(rows)), "githints verify (shows each one)"})
	}

	// The anchors are what catches a log that was edited and re-signed with
	// the salt, which the chain check above cannot.
	notes, err := gitutil.ReadNotes("refs/notes/githints")
	if err != nil {
		return append(out, checkResult{levelWarn, "anchors", err.Error(), ""})
	}
	rep := integrity.VerifyAnchors(rows, notes)
	switch {
	case len(rep.Problems) > 0:
		out = append(out, checkResult{levelFail, "anchors", fmt.Sprintf("%d problem(s): %s", len(rep.Problems), rep.Problems[0]), "githints verify (shows each one)"})
	case rep.Notes == 0 && len(rows) > 0:
		out = append(out, checkResult{levelWarn, "anchors", "no Merkle anchors in refs/notes/githints yet", "they are written by the post-commit hook; check the hooks above"})
	case rep.Notes > 0:
		out = append(out, checkResult{levelOK, "anchors", fmt.Sprintf("%d note(s) match the log", rep.Checked), ""})
	}
	return out
}

func checkIndex(root string) checkResult {
	dbPath := lang.IndexDBPath(root)
	if _, err := os.Stat(dbPath); err != nil {
		return checkResult{levelWarn, "index", "enabled but never built", "githints index"}
	}
	db, err := index.Open(dbPath)
	if err != nil {
		return checkResult{levelFail, "index", err.Error(), "delete .githints/index.db and run `githints index`"}
	}
	defer db.Close()
	at, err := db.LastIndexedAt()
	if err != nil || at == 0 {
		return checkResult{levelWarn, "index", "enabled but never built", "githints index"}
	}
	age := time.Since(time.Unix(at, 0)).Round(time.Minute)
	return checkResult{levelOK, "index", fmt.Sprintf("last indexed %s ago", age), ""}
}

// checkMCP looks for githints in the project-level config of every client
// setup knows. Global configs (Claude Desktop, Windsurf, Cline) are not
// checked; that is a warning, not a failure, since the CLI works without any.
func checkMCP(root string) checkResult {
	var found []string
	for _, p := range projectConfigFiles(root) {
		data, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(data), "githints") {
			found = append(found, displayPath(root, p))
		}
	}
	if len(found) == 0 {
		return checkResult{levelWarn, "mcp", "no project MCP config registers githints (global configs are not checked)",
			"githints setup"}
	}
	return checkResult{levelOK, "mcp", "registered in " + strings.Join(found, ", "), ""}
}
