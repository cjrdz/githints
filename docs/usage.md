# githints usage guide

How to install, set up, and use githints day-to-day.

## Install

Requires Go 1.25.5+. Supported platforms: Linux, macOS, Windows.

```sh
# One-command install
go install github.com/cjrdz/githints@latest

# Or build from source
go build -o githints .
```

On Windows, install [Git for Windows](https://gitforwindows.org/) first — it
provides the POSIX sh that runs the git hooks.

Put `githints` on your `PATH`, or use a project-relative path in your MCP
config.

## Set up a repo

From the repo root:

```sh
githints init
```

This creates `.githints/` (including `store.db` and an initial `CHANGES.md`)
and installs `.git/hooks/post-commit` and `.git/hooks/pre-commit`.

Add `.githints/` to `.gitignore`:

```gitignore
.githints/
```

The generated `.md` files are useful to read, but they can always be
regenerated from `store.db`, so they do not need to be committed.

## Agent integration

`githints serve` is a stdio MCP server. It resolves the repo root from its
current working directory, so use a **project-scoped** MCP config.

### Claude Code

Create `.mcp.json` in the repo root:

```json
{
  "mcpServers": {
    "githints": {
      "command": "githints",
      "args": ["serve"]
    }
  }
}
```

### opencode

Create `opencode.json` in the repo root:

```json
{
  "mcp": {
    "githints": {
      "type": "local",
      "command": ["githints", "serve"],
      "enabled": true
    }
  }
}
```

If the binary is not on your `PATH`, use `"command": ["./githints", "serve"]`.

## Daily workflow

1. Edit a file.
2. Call `record_change` (or `record_batch`) with a concrete summary and
   optional reason.
3. Stage and commit as usual.
4. The post-commit hook claims the pending rows and records fallbacks for any
   files the agent did not cover.

If you commit without recording, the hook writes a generic fallback entry so
nothing is silently lost.

## Optional local Ollama summarization

githints can ask a local Ollama model to caption hook fallback rows and to
compress read-path output. It is **disabled by default** and requires explicit
configuration.

Create or edit `.githints/config.json`:

```json
{
  "ollama": {
    "enabled": true,
    "endpoint": "http://127.0.0.1:11434",
    "model": "qwen2.5:3b-instruct",
    "timeout_ms": 3000,
    "max_diff_bytes": 4096
  }
}
```

Settings can also be overridden with environment variables:

- `GITHINTS_OLLAMA_ENABLED`
- `GITHINTS_OLLAMA_ENDPOINT`
- `GITHINTS_OLLAMA_MODEL`
- `GITHINTS_OLLAMA_TIMEOUT_MS`
- `GITHINTS_OLLAMA_MAX_DIFF_BYTES`

The endpoint must resolve to a loopback address. To point it at a non-loopback
address, set `GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK=1`.

When enabled:

- The `post-commit` hook sends each fallback diff through a secret-scrubbing
  filter, truncates it to `max_diff_bytes`, and asks Ollama for a one-line
  caption. If Ollama is down, times out, or returns unusable output, the hook
  falls back to the generic text immediately.
- `get_diff(file=..., summarize=true)` returns a one-sentence summary instead
  of the full unified diff.
- `get_recent_changes(limit=..., summarize=true)` returns a compressed
  overview instead of the full list.

## CLI reference

```sh
# Set up

githints init

# Run the MCP server

githints serve

# Manually record a change (useful for scripts or testing)

githints record -file=internal/auth/token.go -summary="Replace linear scan with map lookup" -reason="reduce p99 latency"

# Check integrity

githints verify

# Timeline query (RFC3339 or Unix seconds)

githints changes -since=2026-06-01T00:00:00Z -until=2026-07-01T00:00:00Z -file=internal/auth/token.go

# Dashboard

githints status

# Rebuild the structural index from scratch

githints index

# Structural index dashboard

githints index status

# Rebuild the index, overwriting partial-write / size-cap guards

githints index --force

# Rotate the integrity salt and re-sign the chain

githints rotate-salt
```

## Structural index and the Obsidian graph view

githints maintains a **structural index** alongside the change log: a derived,
regenerable SQLite cache (`.githints/index.db`) of every symbol and import
in the repo, plus one markdown note per file under `.githints/index/` and a
rollup at `.githints/INDEX.md`.

It is **not** part of the integrity-verified changelog — there is no HMAC
chain on `index.db`, and rebuilding it is always safe:

```sh
githints index          # full rebuild
githints index status   # file/symbol counts, last scan time
```

The post-commit hook refreshes only the files touched by each commit, so the
index stays current without full rescans.

### MCP tools

Agents (or you, manually) can query the index through the MCP server:

| Tool | Question it answers |
|------|---------------------|
| `list_symbols(file=...)` | What is defined in this file? |
| `find_symbol(name=...)` | Where is this symbol defined? |
| `get_dependents(file=...)` | What breaks if I change/delete this file? |
| `get_index_summary(limit=10)` | Is the index fresh? What are the hub files? |

Every response includes `last_indexed_at` so you can judge staleness before
trusting the answer.

### Obsidian graph view

To browse the codebase as a graph, enable Obsidian wikilinks:

```sh
githints index --obsidian
```

or persistently in `.githints/config.json`:

```json
{
  "index": {
    "obsidian_wikilinks": true
  }
}
```

Then open the `.githints/` folder as an Obsidian vault (**File → Open vault**).
The index notes under `.githints/index/` link to each other with `[[wikilinks]]`
at file-level granularity, so the built-in **Graph view** (Ctrl/Cmd+G) shows
which files import which. `.githints/INDEX.md` is the hub list — the most
imported files.

Notes:

- File names containing `[`, `]`, or `|` are URL-encoded in the link target
  (`[[file%5Bname%5D.go.md|file[name].go]]`), so links never break.
- Default (non-Obsidian) rendering is unchanged; the flag only affects the
  index notes, never the integrity-verified `CHANGES.md` or per-file hints.
- If `.githints/` is in your Obsidian vault root, set
  `Options → Files & links → Detect all file extensions` off; the notes are
  plain markdown and need no special handling.

### Configuration

The `index` section of `.githints/config.json` controls indexing:

```json
{
  "index": {
    "enabled": true,
    "languages": ["go", "typescript", "svelte", "astro"],
    "max_bytes": 209715200,
    "max_file_size": 1048576,
    "parse_timeout_ms": 5000,
    "obsidian_wikilinks": false
  }
}
```

`languages` defaults to `["go"]` and can only select from the parsers compiled
into the binary:

| Language | Extensions | Parser |
|----------|-----------|--------|
| `go` | `.go` | stdlib `go/parser` (exact) |
| `typescript` | `.ts` `.tsx` `.mts` `.cts` `.js` `.jsx` `.mjs` `.cjs` | heuristic, stdlib-only |
| `svelte` | `.svelte` (`<script>` blocks) | heuristic, via the TypeScript parser |
| `astro` | `.astro` (frontmatter + `<script>` blocks) | heuristic, via the TypeScript parser |

TypeScript-family notes:

- Relative imports (`./x`, `../x`) are resolved to repo-relative file keys —
  extension and trailing `/index` stripped — so `get_dependents(file=...)` and
  the "Imported by" note sections work across `.ts`/`.svelte`/`.astro` files.
- Package specifiers (`svelte`, `astro`) and tsconfig path aliases (`@core/x`)
  are stored raw and never resolve to files, so `get_dependents` only sees
  relative-import edges.
- Function-body locals are intentionally skipped to keep test files out of
  the index.

Environment overrides: `GITHINTS_INDEX_ENABLED`,
`GITHINTS_INDEX_MAX_BYTES`, `GITHINTS_INDEX_MAX_FILE_SIZE`,
`GITHINTS_INDEX_PARSE_TIMEOUT_MS`, `GITHINTS_INDEX_OBSIDIAN_WIKILINKS`.

## Pre-commit gate

By default the pre-commit hook warns when staged files have no pending
`record_change` row. To make it block commits instead:

```sh
export GITHINTS_PRECOMMIT_BLOCK=1
```

## Sharing history with your team

By default, `githints init` adds `.githints/` to `.gitignore` so the store and
salt stay private. To let the rendered markdown travel with the repo, initialize
in shared mode:

```sh
githints init -share
```

In shared mode, only the state files are ignored:

```gitignore
# >>> githints (managed)
.githints/store.db*
.githints/.salt
.githints/config.json
# <<< githints
```

`CHANGES.md` and the per-file `.md` hints are not ignored, so they can be
committed and shared.

### One-commit lag

The post-commit hook re-renders markdown *after* the commit. This means the
updated `CHANGES.md` lands in the *next* commit, not the one that produced the
changes. This is normal for generated changelogs.

### Merge conflicts

If `CHANGES.md` or a per-file hint conflicts during a merge, run:

```sh
githints render
```

This regenerates every markdown file from the current store, replacing either
merge side with the canonical derived content.

## Multiple projects

- **Monorepo**: run `githints init` once at the root. Paths are naturally
  scoped.
- **Separate repos**: run `githints init` in each repo. Stores and hooks are
  isolated; do not share a `store.db` across repos.

## Uninstall

```sh
rm .git/hooks/post-commit .git/hooks/pre-commit
rm -rf .githints/
```

Your source tree is untouched.
