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

- **Agent-driven** — the `record_change` MCP tool or the
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
| `source` | `"agent"`, `"llm"` (rows from earlier versions; the hook no longer writes it), or `"fallback"`. Legacy `"hook"` rows are migrated to `"fallback"`. |
| `summary` | Short human description of what changed. |
| `reason` | Optional explanation of why it changed. |
| `diff_stat` | e.g. `+5 -2`, captured for agent rows from the working tree. |
| `diff_hash` | SHA-256 of the unified diff for this file/commit. |
| `agent_id` | Optional session/client fingerprint. |
| `recorded_at` | Authoritative Unix timestamp, set in Go. |
| `hmac` / `prev_hmac` | Hex HMAC-SHA256 chaining the row to the previous one. |
| `clock_tamper_warning` | Set when `recorded_at` jumps backward unexpectedly. |
| `created_at` | SQLite timestamp, for human reference only. |
| `kind` | `"change"` (default) or `"outcome"`. An outcome row is a later statement about a change, in the same table so it shares the chain. |
| `target_id` / `target_diff_hash` | For an outcome: the id of the change it is about and that change's `diff_hash` when the outcome was recorded. `0` / empty for changes. |
| `outcome` | For an outcome: `worked`, `partial`, `failed`, `reverted` or `unknown`. Empty for changes. |
| `candidate_tree` / `base_commit` / `verdict` | For a review receipt (`kind = "review"`): the tree `git write-tree` gave for the index, the HEAD it was staged on, and `approved`, `escalated` or `inconclusive`. Empty otherwise. |

Indexes exist on `file_path`, `commit_hash`, `branch`, `recorded_at` and
`target_id`.

Every reader that means "changes" (history, recent, search, range, the hook's
claim and the counts) filters `kind = 'change'`; `AllChanges` and
`ChangesForCommit`, which feed verification and anchors, see every row. An
outcome is never claimed by a commit, so its `commit_hash` stays empty and it is
covered by the whole-log Merkle root rather than a per-commit one.

The four outcome fields are part of the signed payload but are `omitempty`, and
`kind` is dropped when it is `"change"`, so a row written before they existed
marshals to the same bytes and keeps its HMAC and its anchored roots. Outcomes
are append-only; the newest outcome per change is what readers attach.

A review receipt is the third kind, with the same arrangement: its three fields
are signed and `omitempty`, after the outcome ones. It has no file. Its summary
is a fixed form, `<verdict>; risk <level>; lenses <list>`, with the level
githints computed (the reviewer never supplies one) and the lenses the reviewer
claimed; its reason holds the findings. The post-commit hook stamps
`commit_hash` on every unused receipt whose tree and base equal the new
commit's tree and parent (`ClaimReviewsTx`), before the anchor is written, so
the anchor covers it. A stamped receipt is never valid again, which is what
makes it one-time even when the same tree is staged on the same base later.
`verify` reads the tree and parent of every receipt's commit with one `git
cat-file --batch` and reports a receipt whose commit is not the change it
reviewed.

The gate and `review_status` recompute a receipt's HMAC with the repository
key before believing it, so an approval written into the database by hand is
refused at commit time rather than reported by `verify` afterwards.

Threat model: a receipt is as strong as the HMAC key, like every row. It is
evidence that a review was recorded for that tree, by whoever held the key, not
proof that the review was thorough. Whether review applies is the user's choice
(`GITHINTS_REVIEW=0`); a repository can turn the gate on and add classes to
paths, never lower a level.

### `changes_fts`

A virtual FTS5 table over `summary` and `reason`. It is kept in sync by an
`AFTER INSERT` trigger, so a `query_changes` search is always current.

### Notes tables (in `index.db`)

Notes are markdown files in `.githints/notes/`; the files are the source of
truth. `index.db` holds a search index over them (`notes`, `note_tags`,
`note_refs` and an FTS5 table `notes_fts`), created with `IF NOT EXISTS` and kept
outside `schemaVersion`, so a structural-index reset or `Clear` never drops them
and they can be rebuilt from the directory at any time. They are **not** part of
the integrity-verified change log. See [Notes](#notes).

### `githints_meta`

Small key/value table for durable metadata:

- `last_recorded_at` — highest `recorded_at` written so far, persisted across
  restarts so clock-skew detection survives process restarts.
- `last_verify_at` — timestamp of the last successful `githints verify`.

## Write paths

### Agent path

`record_change` accepts a repo-relative file path, summary and optional
reason, or `changes=[...]` of them, recorded in one transaction. Before insertion the recorder:

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

1. Loads `.githints/config.json`.
2. Resolves HEAD and the list of files changed by that commit.
3. For each file (skipping `.githints/`):
   - Claims any pending agent rows for that file/hash.
   - Loads the agent rows already recorded for that file/hash.
   - If the agent already described the file for this commit, no fallback is
     written.
   - Otherwise, writes a generic fallback row.
4. Looks for reverts (see [Revert detection](#revert-detection)).
5. Re-renders markdown for any touched file.
6. Writes a Merkle anchor for this commit as a `refs/notes/githints` git
   note (see [Merkle anchors](#merkle-anchors)).

### Revert detection

`recordReverts` (`internal/cli/hooks/revert.go`) reads the new commit's message
for `This reverts commit <40 or 64 hex>` lines (`gitutil.RevertedCommits`; at
most 20, full hashes only). The message is untrusted, so it only says where to
look. For each recorded change of a named commit it compares
`git ls-tree <this commit> -- <file>` with `git ls-tree <reverted commit>^1 --
<file>`, and records a `reverted` outcome only if this commit touched the file
and the two are identical (both absent counts). The outcome has `source =
fallback`, which readers show as auto-detected, and `commit_hash` set to the
reverting commit so that commit's per-commit anchor covers it. The note is fixed
text naming the commit; no message text is stored. A repeat run finds the same
note already present and writes nothing. Work is bounded (200 rows per commit),
and any failure is printed and skipped: detection never fails the hook.

### Pre-commit gate

`githints hook-precommit` first scans the lines the staged diff adds for
credential shapes (`secrets.ScanDiff`, the same list the recorder refuses),
reporting file and line only; `GITHINTS_SECRET_BLOCK=1` makes a finding block.
It then lists staged files and checks that each has a pending agent row. If not, it prints a warning. Setting `GITHINTS_PRECOMMIT_BLOCK=1`
makes the hook return a non-zero exit code and abort the commit. That is the
only outcome that can fail a commit besides the review gate: with
`review.gate` set to `block`, a high-risk staged change (see `internal/risk`)
with no approved receipt for its exact tree also aborts it. Any internal error
(a locked store, a bad config) is printed and the commit proceeds.

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
metacharacters backslash-escaped, including the `==` highlight, `%%` comment and `#tag` extensions.
Values in a `` `code span` `` get a fence longer than any backtick run inside
them instead, since backslashes would render literally there.

Per-file hints are written through an `os.Root` opened on `.githints/` itself,
so no link inside it can redirect a write anywhere else, not even elsewhere in
the repository. A source path whose hint would overwrite githints' own output
(`CHANGES`, `INDEX`, `index/`, `notes/`, and `.obsidian/`, where releases
before the graph viewer wrote vault settings) is skipped with a warning.

## Diff redaction

`get_diff` and `githints diff` hand back a diff through one function,
`secrets.DisplayDiff`: `ScrubDiff` first redacts every content line of files
matching common secret names (`.env`, `*.pem`, `id_rsa*`, etc.) and any line
containing a high-signal credential shape, then the result is truncated at
`MaxDiffResultBytes` with a marker. Headers are kept so the reader still sees
which files changed. No githints package imports `net`.

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
- **A link is never written through.** `os.Root` follows a symlink that stays
  inside the root, so `safefs.WriteFileIn` `Lstat`s the target and every directory
  on the way and refuses a link or junction. Without it a link planted in
  `.githints/` (shared mode commits that directory, so a clone can ship one) turned
  a routine rewrite of `CHANGES.md`, a hint, `graph.html` or `notes.html` into an
  overwrite of whatever it pointed at, for example `CHANGES.md -> store.db`. Every
  write into `.githints/` goes through this one function.
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
- **Repository config is bounded.** `config.json` limits have ceilings.

## Notes

`internal/notes` owns the format and every rule about what a note may hold;
`internal/index/notes.go` only stores what it is given.

- **Sync, not a hook.** Every reader (`Get`, `Search`, the brief) first runs
  `Sync`: one directory listing and one `Lstat` per note, re-reading only files
  whose size or mtime changed. Hand edits therefore show up with no other step,
  and a file that fails a check is *removed* from the index rather than left with
  its older text.
- **Reading.** All access is through an `os.Root` on `.githints/`
  (`safefs.OpenExisting` for readers, `safefs.Open` for the writer). The reader
  `Lstat`s each file and refuses anything but a regular file, because
  `safefs.ReadFileIn` follows a link that stays inside the root: without this,
  `notes/x.md -> ../store.db` would be indexed as a note. The `notes/` directory
  is checked the same way, and a file with more than one hard link is refused
  (`safefs.ReadSingleLinkFileIn`, link count from the open file: `Nlink` on Unix,
  `GetFileInformationByHandle` on Windows). Names are `[a-z0-9][a-z0-9-]{0,63}`,
  an alphabet with no dot or separator in it, and not a Windows device name.
- **The index is a cache, not an authority.** `Recheck` re-applies the note rules to
  a row before `Get` or the export uses it; a row that fails is deleted and the
  file re-read, since `Sync` trusts a row whose size and mtime still match.
- **Writing.** `Record` builds the document, writes it as a file, parses it back
  and only then touches the disk; without `Replace` the file is created with
  `O_EXCL`, so an existing note is never overwritten, even in a race.
- **Parsing.** Front matter is a hand-written subset of YAML (flat keys, one-line
  lists, nothing else): no anchors, aliases, tags or multi-line values, so there
  is no parser to attack. Unknown keys are ignored, a multi-line value under a
  known key is refused, and a front matter over 8 KiB or 64 lines is refused.
  `FuzzParse` checks that nothing panics, the caps hold, and an accepted note
  survives being written and read again.
- **Checks, in order.** Control, zero-width and bidi characters are stripped, then
  the caps are enforced, then `internal/secrets` scans the title, the raw tags
  (before lowercasing hides an upper-case key), the paths and the body.
- **Search.** A query is words: each is quoted and made a prefix and all are
  required, so FTS5 operators and column filters are just text and cannot be a
  syntax error. Length, word count and word length are capped.
- **Output.** Listings and notes carry a notice that they are unsigned data, and
  every stored field is flattened, stripped and clipped.
- **Viewer.** `notes.html` follows the graph viewer: `default-src 'none'`, the one
  script and style pinned by hash, data in a non-executed JSON block, `textContent`
  only, no markdown rendering. Bodies past a 4 MiB page budget are left out.
- **Reserved.** A source file under `notes/` would render its hint into
  `.githints/notes/`, so `notes` is a reserved directory for hints, like `index`.

## Export pack

`internal/export` writes `githints export`. It is a set of explicit queries
(`AllChanges`, `FilesWithHistory`, `FileHistory`, the notes index), never a walk
of `.githints/`: that directory holds the salt, `repo-id` and the databases, and
"copy what is there" is how a key ends up in an attachment.

- **Destination.** `prepareDir` accepts a new directory or an empty plain one and
  refuses anything else, including a symlink and anything under `.githints/`.
  "Under" is decided by file identity (`os.SameFile` on the path and each existing
  ancestor) as well as by resolved path: since Go 1.23 `filepath.EvalSymlinks` no
  longer resolves a Windows junction, so `-o junction\pack` with the junction
  pointing at `.githints` passed a path-only check. This was found by running the
  tool on Windows, and `TestExportRefusesADestinationThroughAJunctionIntoState`
  covers it there. All
  writes go through an `os.Root` on it with `O_EXCL`, so a file can neither
  replace another nor leave the pack.
- **Formats.** JSONL is exact. CSV prefixes cells that start a formula
  (`neutralize`; a fuzz test checks no output cell starts one after any leading
  whitespace). The changelog and hints go through `textsafe` like every other
  rendering. The DuckDB script holds no log text: columns are declared from the
  same `columns` list the other formats use, and files are read by fixed name.
- **Hostile rows.** A hint path comes from a row, and the store is a file. A path
  that is not repo-relative is skipped and listed in `skipped`, and `os.Root`
  would refuse it anyway.
- **Manifest.** Counts, every file's sha256 and size, the v2 whole-log Merkle root
  (`integrity.BuildAnchor`), and the verify status from `ExistingKey` (which never
  creates a salt), `VerifyChain` and `VerifyAnchors`. Anything it cannot check is
  `unavailable`, in fixed text, because an error message can name a path. It is
  written last.
- **Notes.** Only notes the index accepted are exported, as `Doc.Marshal` of the
  parsed note, so one that failed a check (a credential, a link) is not in the pack.
- **Cap.** At most `MaxRows` (1,000,000) rows.

## Structural index

`internal/index` keeps a second SQLite database, `.githints/index.db`, with
`symbols`, `imports`, `facets` and `file_keys` tables, renders one note per file
under `.githints/index/` and a rollup at `.githints/INDEX.md`. It is a cache: it
has no integrity chain and `githints index` rebuilds it from scratch, in one
transaction, so a reader never sees it half-written.

- **Scanning.** A full scan asks git once per pass for every ignored path
  (`git ls-files -c -o -i --exclude-standard --directory`, then again with
  `.githintsignore` as the excludes file), walks the tree skipping them, and
  parses on a worker pool, merging results in walk order. The post-commit hook
  runs an incremental scan over the files a commit touched, reading them through
  an `os.Root` with `Lstat`, so a committed symlink is never followed. Its cost
  follows the commit: language rules and detectors compile on first use, and
  import keys are looked up in `file_keys` rather than recomputed for every
  indexed file, except when the commit changed a file keys derive from (a
  `go.mod`, `Cargo.toml`, `composer.json`, `pyproject.toml`...).
  `githints index verify` recomputes them and reports any that went stale.
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
  log. Git does not push notes by default; `githints anchors push` sends
  `refs/notes/githints` to a configured remote (never forced) if the anchor
  should survive the machine, and `doctor` warns, without contacting the
  remote, while the newest anchors have not been pushed.

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

Reading the previous HMAC, signing and inserting happen in one transaction
(`store.InsertSigned`, and `BatchRecord`'s own). Every transaction begins with
the write lock (`_txlock=immediate`), so the MCP server, the hook and the CLI
cannot sign against the same predecessor and fork the chain. A `recorded_at` the
recorder stamped itself is raised to the previous row's time under that lock; one
supplied by the caller is kept as given. `TestHMACPayloadBytesAreFrozen`,
`TestComputeHMACChainIsFrozen` and `TestAnchorRootsAreFrozen` pin the signed
bytes and the anchor roots: a failure there means a change would invalidate
existing databases.

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

`githints salt rotate` generates a new salt, re-signs every existing row with
the new key, and replaces the old salt. The existing chain is verified first
unless `-force` is passed.

## MCP server

`githints serve` starts a stdio MCP server using `mark3labs/mcp-go`.

Tools:

- `get_session_context` — session orientation: when the session started, how
  much history exists, which tools have been used this session, and suggested
  next steps, then the brief (see [The session brief](usage.md#the-session-brief)).
  Intended as the first call of a session.
- `record_change` — record one file change, or `changes=[...]` atomically.
- `record_outcome` — record how an earlier change turned out, by its `#id`.
- `record_note` — write a markdown note (not integrity-checked).
- `search_notes` — find notes by words, file or tag, or read one by `name`.
- `query_changes` — the change log through one `store.QueryChanges`: a file's
  history, recent changes, FTS search over summaries and reasons, and a
  `recorded_at` range, combined as given.
- `get_diff` — redacted unified diff for a file (committed or working tree).
- `find_symbol` — prefix symbol search across the repo, or with `file`, the
  symbols one file defines with line ranges and signatures.
- `get_dependents` — reverse import lookup: which files import a given one;
  with no file, the index totals and the hub files by import in-degree.
- `get_dependency_graph` — the file/package import graph (`index.BuildGraph`),
  optionally around one file, as JSON or Mermaid. The same graph backs
  `githints index graph`, whose exporters live in `internal/index/graphexport`
  (JSON, DOT, Mermaid, and a self-contained HTML viewer pinned by a hash-based
  Content-Security-Policy).
- `find_facets` — framework constructs by role (`route`, `model`,
  `component`, ...), across frameworks.

The seven tools these replaced keep answering under their old names for one
release, each registered with a one-line "Deprecated: use X" description and
the new tool's handler.

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

`githints setup` (`internal/cli/setup`, with the client table in `internal/cli/mcpcfg`) writes the server entry into
each MCP client's config from one table describing every client: its file, the
top-level key, the entry shape, whether the root must be pinned, and how to
detect it. Formats were checked against each vendor's documentation. Only
githints' own entry is ever added or (with `-update`) replaced; JSON with
comments is never rewritten; Codex's TOML is only appended to; global configs get
a per-repository entry name and a one-time backup, and are written only when the
client is named.

## CLI

Commands are dispatched from the `commands` table in `internal/cli/cli.go`, the single
source of truth; `TestUsageMatchesCommandTable` keeps it and the usage text in
sync. `hook-run` and `hook-precommit` are dispatchable but hidden from usage.
The full reference is in [usage.md](usage.md#cli-reference). Exit status: 0 ok,
1 error, 2 usage error, 3 `verify` found problems.

## Package layout

```
main.go                # thin binary entry point; delegates to internal/cli
internal/
  cli/                 # root dispatcher: Main(), commands table, usage, help, version
    core/              # shared CLI helpers: OpenInitialized, PrintChanges, ErrVerifyFailed, ...
    initialize/        # githints init
    hooks/             # githints hook-run, hook-precommit
    record/            # githints record, outcome, render
    verify/            # githints verify
    read/              # githints log, diff, brief
    graph/             # githints index graph
    indexcmd/          # githints index and subcommands
    salt/              # githints salt (path, export, import, rotate)
    doctor/            # githints doctor
    mcpcfg/            # the MCP client table, and setup -print
    setup/             # githints setup
    teardown/          # githints uninstall (and -keep-data)
    serve/             # githints serve (MCP stdio server)
  managed/             # managed hooks, managed-block primitives, agent instruction blocks
  clitest/             # shared test helpers for the CLI packages
  config/              # .githints/config.json loader, env overrides, bounds
  store/               # change-log SQLite schema, migrations, queries
  brief/               # session-start digest built from the store and the index
  notes/               # markdown notes: format, parsing, checks, sync, search, viewer
  export/              # export pack: JSONL, CSV, changelog, hints, notes, manifest, DuckDB script
  recorder/            # write path: validation, sanitizing, secret scan, insert, render
  hint/                # change-log markdown rendering and verification
  integrity/           # salt, repo id, key derivation, HMAC chain, Merkle anchors
  gitutil/             # git invocations: timeouts, output caps, notes, hooks dir
  safefs/              # os.Root-confined, size-capped file I/O for githints' files
  textsafe/            # the one escaper: control characters, markdown, code spans
  secrets/             # the one credential-pattern list and diff redaction
  index/               # structural index: store, scan, render, verify, graph
    lang/              #   parsers, language specs, detectors, import resolution
    graphexport/       #   JSON, DOT, Mermaid and the HTML viewer
  mcpserver/           # MCP stdio server and tool handlers (the only mcp-go importer)
```
