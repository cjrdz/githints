# githints

This repo has a local MCP server called `githints` for tracking what
changed and why, on a per-file basis. It's already running for you if
your MCP config includes it.

## Rule: call `record_change` after every file edit

Right after you finish editing a file (before moving to the next one,
not batched at the end of the session), call:

    record_change(file="<repo-relative path>", summary="<what changed>", reason="<why, if not obvious>")

- `summary` should be specific: "Replaced the linear scan in FindUser with
  a map lookup" not "Updated function".
- `reason` is optional but valuable — include it whenever the change isn't
  self-explanatory from the summary alone (e.g. "fixes #142", "requested
  by user to reduce p99 latency").
- Call it once per file, even if you touched several files in one task.

## Rule: check history before editing unfamiliar files

Before making non-trivial changes to a file you haven't touched this
session, call `get_file_history(file="...")` to see why it's shaped the
way it is. This avoids re-litigating past decisions.

## Catching up at the start of a session

Call `get_recent_changes(limit=20)` early in a session to see what
happened since you were last here, especially if changes may have come
from another agent, a teammate, or a manual git commit (those show up
with `source: hook` and a generic summary instead of `source: agent`).

## What you do NOT need to do

- Don't edit anything under `.githints/` directly — it's fully
  regenerated from `record_change` calls and the git hook. Manual edits
  will be overwritten.
- Don't worry about committing `.githints/store.db` — it's gitignored.
  The markdown files under `.githints/` are meant to be committed.
