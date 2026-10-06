# githints architecture

This document describes how githints is structured, how data flows through it,
and the security/integrity model.

## Overview

githints is a local change log for AI coding agents. It keeps a small SQLite
database at `.githints/store.db`, renders it into markdown under `.githints/`,
and exposes both an MCP server and a CLI for reading and writing entries.
Alongside it sits a separate, regenerable structural index of the code
(`.githints/index.db`), described under [Structural index](#structural-index).

For how to use any of this, see [usage.md](usage.md); this document is about
how it works.

Two writers feed the same store:

- **Agent-driven** — the `record_change`/`record_batch` MCP tools or the
  `githints record` CLI command.
- **Hook-driven fallback** — the `post-commit` hook runs after each commit to
  claim pending agent rows and to add fallback entries for files the agent did
  not fully describe.

A `pre-commit` hook can warn (or block) when staged files have no pending
agent record.

## Data model

The source of truth is `.githints/store.db`, a SQLite database.

### `changes`

Each row is one recorded change.

| Column | Purpose |
|--------|---------|
| `id` | Auto-increment primary key. |
| `file_path` | Repo-relative path to the changed file. |
| `commit_hash` | Empty for pending agent rows; filled by the post-commit hook. |
| `branch` | Branch name captured at record time. |
| `source` | `"agent"`, `"llm"`, or `"fallback"`. Legacy `"hook"` rows are migrated to `"fallback"`. |
| `summary` | Short human description of what changed. |
| `reason` | Optional explanation of why it changed. |
| `diff_stat` | e.g. `+5 -2`, captured for agent rows from the working tree. |
| `diff_hash` | SHA-256 of the unified diff for this file/commit. |
| `agent_id` | Optional session/client fingerprint. |
| `recorded_at` | Authoritative Unix timestamp, set in Go. |
| `hmac` / `prev_hmac` | Hex HMAC-SHA256 chaining the row to the previous one. |
| `clock_tamper_warning` | Set when `recorded_at` jumps backward unexpectedly. |
| `created_at` | SQLite timestamp, for human reference only. |

Indexes exist on `file_path`, `commit_hash`, `branch`, and `recorded_at`.

### `changes_fts`

A virtual FTS5 table over `summary` and `reason`. It is kept in sync by an
`AFTER INSERT` trigger, so `search_changes` is always current.

### `githints_meta`

Small key/value table for durable metadata:

- `last_recorded_at` — highest `recorded_at` written so far, persisted across
  restarts so clock-skew detection survives process restarts.
- `last_verify_at` — timestamp of the last successful `githints verify`.

## Write paths

### Agent path

`record_change` and `record_batch` accept a repo-relative file path, summary,
and optional reason. Before insertion the recorder:

1. Validates the path: repo-relative, local (`filepath.IsLocal`), and free of
   control or invisible formatting characters.
2. Enforces the size caps (`MaxSummaryLen`, `MaxReasonLen`, `MaxAgentIDLen`).
3. Removes control characters, terminal escapes, bidi overrides and zero-width
   characters from summary, reason and agent id (`textsafe`), *then* scans them
   for credential shapes (`internal/secrets`) and rejects the row on a match.
   Sanitizing first matters: a zero-width space inside a key would otherwise hide
   it from the scan.
4. Captures the working-tree diff stat and a SHA-256 of the unified diff.
5. Stamps the current branch.
6. Computes the next HMAC using the integrity key and inserts the row.
7. Re-renders the affected per-file markdown and `CHANGES.md`.

Agent rows are inserted with `commit_hash = ''` and are shown as
**uncommitted** in rendered output until the post-commit hook claims them.

### Hook path

After a commit, `githints hook-run` (the `post-commit` hook):

1. Loads `.githints/config.json` and creates the Ollama client only if enabled.
2. Resolves HEAD and the list of files changed by that commit.
3. For each file (skipping `.githints/`):
   - Claims any pending agent rows for that file/hash.
   - Loads the agent rows already recorded for that file/hash.
   - Compares the sum of agent diff stats to the commit's diff stat.
   - If they match exactly, no fallback is written.
   - Otherwise, asks the local Ollama model (when enabled) for a one-line
     caption of the scrubbed and truncated diff. On any error, timeout, or
     circuit-breaker-open state, it writes a generic fallback row instead.
4. Re-renders markdown for any touched file.
5. Writes a Merkle anchor for this commit as a `refs/notes/githints` git
   note (see [Merkle anchors](#merkle-anchors)).

### Pre-commit gate

`githints hook-precommit` lists staged files and checks that each has a pending
agent row. If not, it prints a warning. Setting `GITHINTS_PRECOMMIT_BLOCK=1`
makes the hook return a non-zero exit code and abort the commit. That is the
only outcome that can fail a commit: any internal error (a locked store, a bad
config) is printed and the commit proceeds.

### Hook installation

`init` asks git where hooks live (`git rev-parse --git-path hooks`), so
`core.hooksPath` and linked worktrees work. A hooks path inside the work tree is
a committed hook manager's directory, so `init` refuses to write machine-specific
hooks there. The hook script holds the binary's path in a single-quoted shell
variable (never interpolated into code), tries it first, falls back to
`githints` on `PATH`, and exits 0 if neither exists. `init -chain` moves an
existing hook to `<hook>.pre-githints`, which the new hook runs first.

## Rendering

The `hint` package reads the SQLite store and writes two kinds of artifacts:

- **Per-file hints** — `.githints/<file_path>.md` mirrors the source tree and
  shows the last N changes for that file.
- **Root changelog** — `.githints/CHANGES.md` is a repo-wide rollup of recent
  changes, grouped by commit.

Both files are fully derived from the database, so they can be deleted and
regenerated at any time (`githints init` re-creates an empty changelog).

Both renderers escape agent-supplied text through `internal/textsafe`, the one
escaper shared with the index notes. Prose is flattened to a single line (so a
newline cannot forge a heading or an entry), HTML-escaped, and has markdown
metacharacters backslash-escaped, including Obsidian's `==`, `%%` and `#tag`.
Values in a `` `code span` `` get a fence longer than any backtick run inside
them instead, since backslashes would render literally there.

Per-file hints are written through an `os.Root` opened on `.githints/` itself,
so no link inside it can redirect a write anywhere else, not even elsewhere in
the repository. A source path whose hint would overwrite githints' own output
(`CHANGES`, `INDEX`, `index/`, `.obsidian/`) is skipped with a warning.

## Local Ollama integration

The optional `internal/llm` package provides a stdlib-only Ollama client for
`/api/generate`. It is created only when `ollama.enabled` is true; otherwise
`NewClient` returns `nil` and no HTTP client is allocated.

Before any content leaves the process, `ScrubDiff` redacts lines from files
matching common secret patterns (`.env`, `*.pem`, `id_rsa*`, etc.) and lines
containing high-signal credential shapes. The scrubbed diff is then truncated
to `max_diff_bytes` before the JSON payload is built.

The client enforces a strict per-request timeout and an in-memory circuit
breaker: after three consecutive failures or timeouts it stops calling Ollama
for the rest of the process lifetime. Model output is sanitized (one line, no
control characters, no shell metacharacters, bounded length) before it is
returned. On any failure the caller falls back to the existing generic text.

The MCP server exposes the integration through optional `summarize` flags on
`get_diff` and `get_recent_changes`.

## Untrusted input

A repository is attacker-controlled input: a clone can ship symlinks, a
`config.json`, a `tsconfig.json`, even a `.githints/` directory. The rules:

- **All githints-managed file I/O goes through `internal/safefs`.** Directories
  must be plain (not a symlink or junction, which `os.OpenRoot` would follow in
  its last component); reads and writes go through an `os.Root`; reads accept
  regular files only (a FIFO is refused before it can block) and are size-capped.
  This covers hints, index notes, `INDEX.md`, the stale-note prune, the
  databases, `config.json`, `go.mod`, `tsconfig.json`, `package.json` and the
  graph page. `init` refuses to rewrite `AGENTS.md`, `CLAUDE.md` or `.gitignore`
  if it is a link.
- **State that arrived in a clone is refused.** A `store.db`, `index.db` or
  legacy `.salt` that git tracks did not come from this machine. A tracked
  `repo-id` is ignored. SQLite opens with `trusted_schema=OFF` on every
  connection.
- **Text is neutralized before it is displayed** (`internal/textsafe`): markdown,
  terminal output and MCP responses never carry control characters, escapes or
  bidi overrides from recorded text, file names or symbol names.
- **git runs with fixed behaviour.** Diffs use `--no-ext-diff --no-textconv`, so
  a repository's `.gitattributes` cannot make githints run a diff driver. Every
  git call has a timeout and a capped buffer, and every commit-ish is checked
  with `IsValidCommitish` before it reaches argv.
- **Repository config is bounded.** `config.json` limits have ceilings, and a
  repository that enables Ollama is announced on stderr.

## Structural index

`internal/index` keeps a second SQLite database, `.githints/index.db`, with
`symbols`, `imports` and `facets` tables, renders one note per file under
`.githints/index/` and a rollup at `.githints/INDEX.md`. It is a cache: it has no
integrity chain and `githints index` rebuilds it from scratch.

- **Scanning.** A full scan walks the tree (skipping gitignored and
  `.githintsignore`d files, decided in two batched `git check-ignore` calls) and
  parses on a worker pool, merging results in walk order. The post-commit hook
  runs an incremental scan over the files a commit touched, reading them through
  an `os.Root` with `Lstat`, so a committed symlink is never followed.
- **Parsing.** Go uses `go/parser`; the TypeScript family a hand-written
  lexer-plus-patterns parser; other languages declarative specs over a shared
  blanking lexer. Framework detectors then record facets. See
  [extensibility.md](extensibility.md).
- **Import resolution.** An import is stored as the key the importer wrote; a
  file is matched to it through its language's `ImportPath`. Per-scan hooks
  (`BeginScan`) load project configuration once per scan: the nearest `go.mod`
  per directory, the nearest `tsconfig.json` with `extends` and the workspace's
  `package.json` names, Python project roots. The index records a resolver
  version so a change in resolution is reported rather than silently mixing old
  and new keys.
- **Graph.** `index.BuildGraph` turns import edges into a file graph, grouping
  files that share an import path (a Go package) into one node, and supports a
  focus with a hop depth and a node cap. `internal/index/graphexport` writes it
  as JSON, DOT, Mermaid, or a self-contained HTML viewer whose CSP is
  `default-src 'none'` with the inline script and style pinned by hash; graph
  data sits in a non-executed JSON block and the viewer only uses `textContent`.

## Integrity model

The threat model is an attacker who can modify `store.db` but does not have the
local integrity key. This includes external scripts, accidental corruption, or a
compromised process that only has access to the repo directory.

### Key derivation

On first use, `githints init` creates a 32-byte random salt (`0600` permissions).
New installs store the salt outside the repo tree (under `os.UserConfigDir()` or
`GITHINTS_SALT_DIR`) so it is not accidentally committed when sharing rendered
markdown from `.githints/`. Existing repos that already have `.githints/.salt`
keep using that legacy location.

The key is derived as:

```
HMAC-SHA256(salt, git_user_email)
```

If `user.email` is not configured, the key is derived from the salt alone. The
salt is machine-local, so keys are not portable across machines without
`githints salt export` / `salt import`.

The salt file is named by a random repo id kept in `.githints/repo-id`, which
moves with the checkout. It used to be named by the hash of the repository's
absolute path, so renaming or moving the checkout silently orphaned it; a salt
from that scheme is adopted under a repo id the first time it is loaded. A
`repo-id` that git tracks is ignored, since it would let a clone choose which
salt on this machine is used.

If the log already has signed rows and no salt can be found, githints refuses
to create one (`integrity.ErrSaltMissing`) and prints the recovery steps: a new
salt would make every row fail verify. When every signed row does fail,
`verify` says the key most likely changed (a different salt or `user.email`)
rather than that every row was tampered with.

### What the chain does and does not defend against

- **It does detect:** insertion, deletion, or modification of rows by anyone who
  lacks the key. It also detects accidental DB corruption.
- **It does not stop:** a determined attacker running as the same OS user who
  locates the salt file, because that user can read the salt and recompute valid
  HMACs. For that actor the external check is the Merkle anchor stored as a
  `refs/notes/githints` git note, which `githints verify` compares against the
  log. Git does not push notes by default; push `refs/notes/githints` if the
  anchor should survive the machine.

### HMAC chain

Every inserted row stores:

- `prev_hmac` — the HMAC of the previous row.
- `hmac` — HMAC-SHA256 over a JSON payload of the row's immutable fields.

`commit_hash` is intentionally excluded from the HMAC payload because
`ClaimPendingTx` mutates it after insertion; the per-commit Merkle root in
`refs/notes/githints` is what binds rows to their commit. All other fields are
included, `diff_hash` and `clock_tamper_warning` among them — the latter must be
signed, or clearing it in the database would erase the tamper evidence while the
chain still verified clean. That is why the clock check runs in
`store.CheckClockTamper` before the row is signed, rather than inside `Insert`.

`githints verify` walks the chain and reports broken or missing links, a
non-empty `prev_hmac` on the first row, and any backward `recorded_at` jumps.

### Clock tamper detection

`recorded_at` is generated in Go (`time.Now().Unix()`) rather than by SQLite so
it can be compared monotonically. If a new row's timestamp is more than
`ClockSkewTolerance` seconds earlier than the previous maximum, the row is
flagged with `clock_tamper_warning = 1` and a warning is printed to stderr.
The previous maximum is persisted in `githints_meta.last_recorded_at` so the
check survives process restarts.

### Merkle anchors

After each commit the post-commit hook writes a note to `refs/notes/githints`
on that commit (`integrity.BuildAnchor`):

```
githints-root: <Merkle root of the rows stamped with this commit>
githints-version: 2
githints-commit-rows: <how many rows that was>
githints-log-count: <rows in the whole log at this moment>
githints-log-last-id: <highest row id at this moment>
githints-log-root: <Merkle root of every row up to that id>
```

`githints verify` reads every note back in two git invocations
(`gitutil.ReadNotes`) and `integrity.VerifyAnchors` recomputes each one:

- each note's per-commit root and row count against the rows now stamped with
  that commit;
- the newest note's log root and count against every row up to its high-water
  id, which catches a row deleted from an earlier commit even if that commit's
  note was removed too.

Version 2 leaves are domain-separated (`0x00` leaf, `0x01` node), cover the
row id, and exclude `hmac`/`prev_hmac` so a salt rotation does not change any
root. Only the per-commit root covers `commit_hash`: the log root also spans
rows still pending at anchoring time, which a later commit stamps. Notes
written before version 2 contain only `githints-root:` and are still checked
with the original `integrity.MerkleRoot`.

Not covered: rows recorded since the newest anchor (verify reports how many),
and an attacker who rewrites the notes ref locally as well as the database.
The second is why pushing `refs/notes/githints` matters.

### Salt rotation

`githints rotate-salt` generates a new salt, re-signs every existing row with
the new key, and replaces the old salt. The existing chain is verified first
unless `-force` is passed.

## MCP server

`githints serve` starts a stdio MCP server using `mark3labs/mcp-go`.

Tools:

- `get_session_context` — session orientation: when the session started, how
  much history exists, which tools have been used this session, and suggested
  next steps. Intended as the first call of a session.
- `record_change` — record one file change.
- `record_batch` — record several file changes in one call.
- `get_file_history` — history for one file.
- `get_recent_changes` — recent repo-wide changes; optional `summarize` flag.
- `search_changes` — FTS search over summaries and reasons.
- `get_diff` — unified diff for a file (committed or working tree); optional
  `summarize` flag.
- `get_changes_in_range` — timeline query by `recorded_at`.
- `list_symbols` — symbols defined in a source file, including line ranges and
  signatures; includes `last_indexed_at` so the agent can assess freshness.
- `find_symbol` — exact and prefix symbol search across the repo.
- `get_dependents` — reverse import lookup: which files import a given one.
- `get_dependency_graph` — the file/package import graph (`index.BuildGraph`),
  optionally around one file, as JSON or Mermaid. The same graph backs
  `githints index graph`, whose exporters live in `internal/index/graphexport`
  (JSON, DOT, Mermaid, and a self-contained HTML viewer pinned by a hash-based
  Content-Security-Policy).
- `get_index_summary` — structural index totals and top hub files by import
  in-degree.
- `find_facets` — framework constructs by role (`route`, `model`,
  `component`, ...), across frameworks.

Every structural index tool response includes the timestamp of the last full
or incremental index scan so the agent can decide whether the data is fresh
enough to trust or whether it should re-index or read the file directly.

The server resolves the repository root from `-root`, then `GITHINTS_ROOT`,
then `CLAUDE_PROJECT_DIR`, then its working directory, and refuses to start in a
repository where `init` has not run. Nothing but JSON-RPC is written to stdout.

### Session tracking

`internal/mcpserver/session.go` holds a `SessionTracker`: the session start
time, the set of tools called so far, and the change count sampled when the
session began. Tools are marked as used by the registration wrapper in `Run()`,
which reads the name off the `mcp.Tool` it registers — so a tool cannot be
exposed without being tracked, and no tool name is written twice.

`get_session_context` renders that state as text, suggesting only tools that
have not been called yet. State is in-memory and per-process: a server restart
is a new session. The stdio transport is single-session by construction, so two
agents sharing one stdio would share the state; that is a property of the
transport rather than something the tracker can resolve.

githints uses `mark3labs/mcp-go` (v1), and `internal/mcpserver` is the only
package that imports it. Its API moved between pre-releases, so an upgrade is its
own change: build, run the gate, and check `tools/list` still answers.

### Client registration

`githints setup` and `mcp-config` (`mcpconfig.go`) write the server entry into
each MCP client's config from one table describing every client: its file, the
top-level key, the entry shape, whether the root must be pinned, and how to
detect it. Formats were checked against each vendor's documentation. Only
githints' own entry is ever added or (with `-update`) replaced; JSON with
comments is never rewritten; Codex's TOML is only appended to; global configs get
a per-repository entry name and a one-time backup, and are written only when the
client is named.

## CLI

Commands are dispatched from the `commands` table in `main.go`, the single
source of truth; `TestUsageMatchesCommandTable` keeps it and the usage text in
sync. `hook-run` and `hook-precommit` are dispatchable but hidden from usage.
The full reference is in [usage.md](usage.md#cli-reference). Exit status: 0 ok,
1 error, 2 usage error, 3 `verify` found problems.

## Package layout

```
main.go, version.go    # CLI dispatch, init, hooks, verify, status, ...
cli_read.go            # history, recent, search, diff
cli_graph.go           # index graph
doctor.go              # githints doctor
mcpconfig.go           # setup and mcp-config: the MCP client table
internal/
  config/              # .githints/config.json loader, env overrides, bounds
  store/               # change-log SQLite schema, migrations, queries
  recorder/            # write path: validation, sanitizing, secret scan, insert, render
  hint/                # change-log markdown rendering and verification
  integrity/           # salt, repo id, key derivation, HMAC chain, Merkle anchors
  gitutil/             # git invocations: timeouts, output caps, notes, hooks dir
  safefs/              # os.Root-confined, size-capped file I/O for githints' files
  textsafe/            # the one escaper: control characters, markdown, code spans
  secrets/             # the one credential-pattern list
  llm/                 # optional local Ollama client and diff scrubbing
  index/               # structural index: store, scan, render, verify, graph
    lang/              #   parsers, language specs, detectors, import resolution
    graphexport/       #   JSON, DOT, Mermaid and the HTML viewer
  mcpserver/           # MCP stdio server and tool handlers (the only mcp-go importer)
```
