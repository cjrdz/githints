# githints

This repo is the githints tool itself. It tracks what changed and why, on a
per-file basis, and exposes those records over an MCP server or via a CLI.

## How this repo is tracked

If the repo is registered as an MCP server in your config, use the MCP tools.
If it is **not** registered as an MCP server (the current workspace setup for
this repo), use the equivalent CLI commands from the repo root:

    ./githints record -file="<repo-relative path>" -summary="<what changed>" [-reason="<why>"]

For repos that are registered as MCP servers, use the MCP tools. For the
current repo — or any repo not running the MCP server — use the CLI commands
below.

## Rule: start every session with `get_session_context`

If using MCP, call `get_session_context()` before reading files or editing.
If using CLI, call:

    ./githints status

It shows the current session start, how much history has been recorded, and
which files still need a `record` call.

## Rule: record changes after editing

### MCP mode

    record_change(file="<repo-relative path>", summary="<what changed>", reason="<why>")

Use `record_batch` for multiple files in one conceptual step:

    record_batch(changes=[
      {"file": "a.go", "summary": "...", "reason": "..."},
      {"file": "b.go", "summary": "..."}
    ])

### CLI mode

    ./githints record -file="a.go" -summary="..." -reason="..."

`summary` should be specific: "Replaced the linear scan in FindUser with a map
lookup" not "Updated function". `reason` is optional but valuable.

## Rule: check history before editing unfamiliar files

MCP:

    get_file_history(file="...")

CLI:

    ./githints history -file="..."

## Rule: use the structural index before editing unfamiliar files

When the repo has a structural index, orient yourself with code-level queries:

- `list_symbols(file="...")` / `./githints index list-symbols -file="..."`
- `get_dependents(file="...")` / `./githints index dependents -file="..."`
- `find_symbol(name="...")` / `./githints index find-symbol -name="..."`
- `get_index_summary(limit=10)` / `./githints index summary`

Every index tool response includes `last_indexed_at`, so you can decide whether
the data is fresh enough or whether to re-index.

## Rule: verify the actual diff when a summary is unclear

MCP:

    get_diff(file="...")

CLI:

    ./githints diff -file="..."

## Core CLI commands

### `init`

    ./githints init

Installs the `post-commit` and `pre-commit` git hooks in the current repo,
creates `.githints/`, and writes the default `config.json`. Run this in any
repo you want to track. After moving the binary or the repo, re-run `init` to
repoint the hooks.

### `serve`

    ./githints serve

Starts the MCP server on stdio. This is what `opencode.json` points to for the
backend and frontend repos.

### `verify`

    ./githints verify

Checks the integrity chain (HMAC of recorded changes, merkle roots anchored in
git notes). Exits non-zero on tampering.

### `status`

    ./githints status

Shows which files are modified, which have pending records, and the current
session state.

### `render`

    ./githints render

Regenerates all rendered markdown (per-file hints, `CHANGES.md`, `INDEX.md`)
from the current store.

### `record`

    ./githints record -file="..." -summary="..." [-reason="..."] [-agent-id="..."]

Records a change for a file when you are not using the MCP server.

## Structural index

The index is a separate SQLite cache (`.githints/index.db`) plus rendered notes
under `.githints/index/` and a rollup at `.githints/INDEX.md`. It is a derived
cache, not part of the integrity-verified changelog, so you can rebuild it at any
time.

    ./githints index              # rebuild from scratch
    ./githints index --force      # override partial-write and max_bytes guards
    ./githints index status       # file/symbol counts and last scan time
    ./githints index verify       # report drift (stale notes, ghost rows,
                                  # uncovered source files with reasons); exits
                                  # non-zero on drift
    ./githints index --obsidian   # render index notes as Obsidian wikilinks

The index is updated automatically by the post-commit hook when indexing is
enabled (default) in `.githints/config.json`.

### Index configuration

Per-repo config lives in `.githints/config.json` under the `index` key:

    {
      "index": {
        "enabled": true,
        "languages": ["go"],
        "max_bytes": 1048576,
        "max_file_size": 1048576,
        "parse_timeout_ms": 5000,
        "obsidian_wikilinks": false
      }
    }

Environment overrides: `GITHINTS_INDEX_ENABLED`, `GITHINTS_INDEX_LANGUAGES`,
`GITHINTS_INDEX_MAX_BYTES`, `GITHINTS_INDEX_MAX_FILE_SIZE`,
`GITHINTS_INDEX_PARSE_TIMEOUT_MS`, `GITHINTS_INDEX_OBSIDIAN_WIKILINKS`.

A repo selects from the languages the binary supports via `index.languages`
(default `["go"]`). It cannot add new languages; language parsers live in the
githints project itself under `internal/index/lang/` and are registered in
`NewRegistry()`. Currently supported: `go`, `typescript` (including `.js`,
`.jsx`, `.mts`, `.cts`), `svelte`, and `astro`.

### Import resolution and tsconfig aliases

For TypeScript-family files, the index resolves relative imports and tsconfig
`paths` aliases (e.g. `@core/x`, `@shared/x`, `@features/x`, `@api-types/x`)
installing the active `paths` map from the nearest `tsconfig.json` on each scan.
Only true package specifiers (npm packages, `svelte`, `astro`, etc.) stay
unresolved.

### `.githintsignore`

A repo may include a `.githintsignore` file at its root to exclude additional
files from the index on top of `.gitignore`. Useful for generated code or
fixtures. It is subtract-only: it cannot re-include git-ignored files.

### Signature blanking

Signatures stored in the index have string/template contents blanked (delimiters
kept balanced), so source strings never leak into the cache.

### Index links

Index notes link to each other with relative `.md` links. Hubs that don't
resolve to an indexed file (stdlib, external packages, tsconfig aliases) render
as plain text in `INDEX.md`.

## Catching up at the start of a session

MCP:

    get_session_context()
    get_recent_changes(limit=20)

CLI:

    ./githints status
    ./githints recent [-limit=20]

For targeted forensics:

- `search_changes(query="...")` / `./githints search -query="..."`
- `get_changes_in_range(since="...", until="...", file="...")` /
  `./githints range -since="..." -until="..." -file="..."`

With Ollama enabled, summaries and diffs can be compressed automatically.

## Local Ollama summarization

githints can ask a local Ollama model to caption hook-fallback rows and to
compress diffs on demand. It is **opt-in and off by default**. To enable it,
create `.githints/config.json`:

    {
      "ollama": {
        "enabled": true,
        "endpoint": "http://127.0.0.1:11434",
        "model": "qwen2.5:3b-instruct",
        "timeout_ms": 3000,
        "max_diff_bytes": 4096
      }
    }

The endpoint must resolve to a loopback address unless you set
`GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK=1`. If Ollama is unreachable, times out,
or returns garbage, the hook silently falls back to the generic text and the
commit never hangs.

## Pre-commit gate

Each tracked repo has a `pre-commit` hook that warns when staged files lack a
pending `record_change`. Set `GITHINTS_PRECOMMIT_BLOCK=1` to make it a hard
error instead of a warning.

## Keeping the binary and the workspace up to date

The MCP servers and git hooks run the `githints` binary. After pulling or editing
new githints commits, rebuild it from the githints repo:

    cd /path/to/githints
    go build -o githints .

Then re-run the following in each consuming repo:

    /path/to/githints/githints init
    /path/to/githints/githints index

`init` repoints the hooks to the new binary path; `index` refreshes the rendered
notes. MCP servers pick up the new binary on the next session start.

## What you do NOT need to do

- Don't edit anything under `.githints/` directly — it's fully regenerated from
  `record_change` calls and the git hook. Manual edits will be overwritten.
- Don't commit `.githints/` unless the repo was initialized with
  `githints init -share`. In the default private mode it is fully gitignored.
  In shared mode only the state files (`store.db`, `.salt`, `config.json`)
  are ignored; the rendered markdown is meant to be committed and shared.
