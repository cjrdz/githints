# githints

Lightweight, local change tracking for AI coding agents.

githints keeps a small SQLite journal of what changed and why, then renders it
into plain markdown under `.githints/` so any model (or human) can catch up
without special tooling.

## Quick start

```sh
# Build
go build -o githints .

# Set up inside a repo
githints init
```

Then wire `githints serve` into your project-scoped MCP config. See
[docs/usage.md](docs/usage.md) for Claude Code, opencode, and manual setup.

## How it works

- **Agent-driven writes**: after editing a file, call `record_change` (or
  `record_batch`) with a concrete summary. Rows start as **pending** and are
  labeled **uncommitted** until the post-commit hook claims them.
- **Hook-driven fallback**: the `post-commit` hook claims pending rows and adds
  a generic fallback entry for any file the agent did not fully describe.
- **Pre-commit gate**: warns (or blocks with `GITHINTS_PRECOMMIT_BLOCK=1`) when
  staged files lack a pending record.
- **Tamper-evident log**: each row is HMAC-chained, `recorded_at` is
  monotonically checked, and a per-commit Merkle root is stored as a git note.

## Documentation

- [Architecture](docs/architecture.md) — data model, write paths, integrity
  model, and package layout.
- [Usage](docs/usage.md) — install, agent setup, CLI reference, and workflows.
- [Contributing](docs/contributing.md) — build, test, code style, and how to
  contribute.

## CLI overview

```sh
githints init                  # set up .githints/ and install hooks
githints serve                 # run the MCP stdio server
githints record -file=F -summary=S [-reason=R]
                               # manually record a change
githints verify                # check HMAC chain and markdown consistency
githints changes -since=T -until=T [-file=F]
                               # timeline forensics query
githints status                # store health and pending records
githints rotate-salt [-force]  # rotate integrity salt and re-sign
```
