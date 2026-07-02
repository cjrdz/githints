# githints architecture

This document describes how githints is structured, how data flows through it,
and the security/integrity model.

## Overview

githints is a local change log for AI coding agents. It keeps a small SQLite
database at `.githints/store.db`, renders it into markdown under `.githints/`,
and exposes both an MCP server and a CLI for reading and writing entries.

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
| `source` | `"agent"` or `"hook"`. |
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

1. Validates the path is local and repo-relative (`filepath.IsLocal`).
2. Scans summary/reason for obvious secret patterns (AWS keys, GitHub PATs,
   private keys, JWTs) and rejects the row if any match.
3. Captures the working-tree diff stat and a SHA-256 of the unified diff.
4. Stamps the current branch.
5. Computes the next HMAC using the integrity key and inserts the row.
6. Re-renders the affected per-file markdown and `CHANGES.md`.

Agent rows are inserted with `commit_hash = ''` and are shown as
**uncommitted** in rendered output until the post-commit hook claims them.

### Hook path

After a commit, `githints hook-run` (the `post-commit` hook):

1. Resolves HEAD and the list of files changed by that commit.
2. For each file (skipping `.githints/`):
   - Claims any pending agent rows for that file/hash.
   - Loads the agent rows already recorded for that file/hash.
   - Compares the sum of agent diff stats to the commit's diff stat.
   - If they match exactly, no fallback is written.
   - Otherwise, writes a hook fallback row with the real commit hash.
3. Re-renders markdown for any touched file.
4. Computes a Merkle root over all rows for this commit and stores it as a
   `refs/notes/githints` git note.

### Pre-commit gate

`githints hook-precommit` lists staged files and checks that each has a pending
agent row. If not, it prints a warning. Setting `GITHINTS_PRECOMMIT_BLOCK=1`
makes the hook return a non-zero exit code and abort the commit.

## Rendering

The `hint` package reads the SQLite store and writes two kinds of artifacts:

- **Per-file hints** — `.githints/<file_path>.md` mirrors the source tree and
  shows the last N changes for that file.
- **Root changelog** — `.githints/CHANGES.md` is a repo-wide rollup of recent
  changes, grouped by commit.

Both files are fully derived from the database, so they can be deleted and
regenerated at any time (`githints init` re-creates an empty changelog).

Markdown rendering escapes HTML and markdown metacharacters to prevent
injection when the files are later rendered in an MCP client's webview.

## Integrity model

The threat model is an attacker who can modify `store.db` but does not have the
local integrity key.

### Key derivation

On first use, `githints init` creates `.githints/.salt` (32 random bytes,
`0600` permissions). The key is derived as:

```
HMAC-SHA256(salt, git_user_email)
```

If `user.email` is not configured, the key is derived from the salt alone. The
salt is machine-local, so keys are not portable across machines.

### HMAC chain

Every inserted row stores:

- `prev_hmac` — the HMAC of the previous row.
- `hmac` — HMAC-SHA256 over a JSON payload of the row's immutable fields.

`commit_hash` is intentionally excluded from the HMAC payload because
`ClaimPending` mutates it after insertion. All other fields, including
`diff_hash`, are included.

`githints verify` walks the chain and reports broken or missing links, plus
any backward `recorded_at` jumps.

### Clock tamper detection

`recorded_at` is generated in Go (`time.Now().Unix()`) rather than by SQLite so
it can be compared monotonically. If a new row's timestamp is more than
`ClockSkewTolerance` seconds earlier than the previous maximum, the row is
flagged with `clock_tamper_warning = 1` and a warning is printed to stderr.
The previous maximum is persisted in `githints_meta.last_recorded_at` so the
check survives process restarts.

### Merkle root

`integrity.MerkleRoot` computes a SHA-256 Merkle tree root over all rows. The
post-commit hook stores the per-commit root as a git note at
`refs/notes/githints`, giving each commit an external, travel-with-the-repo
fingerprint of its change records.

### Salt rotation

`githints rotate-salt` generates a new salt, re-signs every existing row with
the new key, and replaces the old salt. The existing chain is verified first
unless `-force` is passed.

## MCP server

`githints serve` starts a stdio MCP server using `mark3labs/mcp-go`.

Tools:

- `record_change` — record one file change.
- `record_batch` — record several file changes in one call.
- `get_file_history` — history for one file.
- `get_recent_changes` — recent repo-wide changes.
- `search_changes` — FTS search over summaries and reasons.
- `get_diff` — unified diff for a file (committed or working tree).
- `get_changes_in_range` — timeline query by `recorded_at`.

The server resolves the repo root from its current working directory, so it
should be launched with `cwd = project root` (project-scoped MCP configs).

## CLI

Commands are implemented in `main.go`:

- `init` — create `.githints/`, salt, and hooks.
- `serve` — run the MCP server.
- `record` — manual agent-style write.
- `hook-run` / `hook-precommit` — called by git hooks.
- `verify` — check HMAC chain and markdown consistency.
- `changes` — query by time range.
- `status` — show store health and pending records.
- `rotate-salt` — rotate the integrity salt and re-sign.

## Package layout

```
main.go                # CLI wiring
internal/
  store/               # SQLite schema, migrations, queries
  recorder/            # validation, secret scan, insert, render trigger
  hint/                # markdown rendering and markdown verification
  integrity/           # salt, key derivation, HMAC chain, Merkle root
  gitutil/             # thin git shell wrappers
  mcpserver/           # MCP stdio server and tool handlers
```
