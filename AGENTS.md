# githints

This repo has a local MCP server called `githints` for tracking what
changed and why, on a per-file basis. It's already running for you if
your MCP config includes it.

## Rule: start every session with `get_session_context`

Before reading any file or making any edit, call:

    get_session_context()

It returns when this session started, how much history this repo has
recorded, which githints tools you have already used this session, and
suggested next steps tailored to what you haven't done yet.

Do the suggested history calls before reading files directly — the recorded
history explains why the code is shaped the way it is, which saves you from
re-litigating settled decisions. Session state is per-process: if the MCP
server restarts, the session resets, and that is expected.

## Rule: record changes after editing

Right after you finish editing a file, call:

    record_change(file="<repo-relative path>", summary="<what changed>", reason="<why, if not obvious>")

- `summary` should be specific: "Replaced the linear scan in FindUser with
  a map lookup" not "Updated function".
- `reason` is optional but valuable — include it whenever the change isn't
  self-explanatory from the summary alone (e.g. "fixes #142", "requested
  by user to reduce p99 latency").
- Call it once per file, before moving to the next one, unless several files
  were edited in the same conceptual step. In that case use `record_batch`:

      record_batch(changes=[
        {"file": "a.go", "summary": "...", "reason": "..."},
        {"file": "b.go", "summary": "..."}
      ])

If the MCP tools are not available in your environment, use the equivalent
CLI command from the repo root:

    ./githints record -file="<repo-relative path>" -summary="<what changed>" [-reason="..."]

## Rule: check history before editing unfamiliar files

Before making non-trivial changes to a file you haven't touched this
session, call `get_file_history(file="...")` to see why it's shaped the
way it is. This avoids re-litigating past decisions.

## Rule: use the structural index before editing unfamiliar files

When the repo has a structural index, orient yourself with code-level queries
before editing:

- `list_symbols(file="...")` — see what's defined in the file.
- `get_dependents(file="...")` — find what imports it; use this as your
  "blast radius" check before refactoring or deleting.
- `find_symbol(name="...")` — search for a symbol by exact or prefix name.
- `get_index_summary(limit=10)` — check index freshness and the most imported
  hub files.

Every index tool response includes `last_indexed_at`, so you can decide whether
the data is fresh enough to trust or whether you should re-index or read the
file directly.

## Rule: verify the actual diff when a summary is unclear

If a recorded summary doesn't match what you see in the file, call
`get_diff(file="...")` to inspect the real unified diff before trusting it.

## Catching up at the start of a session

Start with `get_session_context()` (see the first rule), then call
`get_recent_changes(limit=20)` to see what happened since you were last
here, especially if changes may have come from another agent, a teammate,
or a manual git commit (those show up with `source: fallback` and a
generic summary instead of `source: agent`).

For targeted forensics, use:

- `search_changes(query="...")` — full-text search over summaries/reasons.
- `get_changes_in_range(since="...", until="...", file="...")` — timeline
  query by recorded timestamp.

## Optional local Ollama summarization

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

## Structural index

When available, githints maintains a separate SQLite index (`.githints/index.db`)
plus rendered notes under `.githints/index/` and a rollup at `.githints/INDEX.md`.
It is a derived cache, not part of the integrity-verified changelog, so you can
rebuild it at any time.

- `githints index` — rebuild the structural index from scratch.
- `githints index status` — show file/symbol counts and last indexed time.
- `githints index --obsidian` — render index notes as Obsidian wikilinks for the
  graph view.

The index is updated automatically by the post-commit hook when indexing is
enabled (default) in `.githints/config.json`.

## Regenerating markdown

If the rendered markdown diverges from the store (for example, after resolving
a merge conflict or switching shared-history modes), run:

    githints render

This re-renders every per-file hint and `CHANGES.md` from the current store.

## What you do NOT need to do

- Don't edit anything under `.githints/` directly — it's fully
  regenerated from `record_change` calls and the git hook. Manual edits
  will be overwritten.
- Don't commit `.githints/` unless the repo was initialized with
  `githints init -share`. In the default private mode it is fully gitignored.
  In shared mode only the state files (`store.db`, `.salt`, `config.json`)
  are ignored; the rendered markdown is meant to be committed and shared.
