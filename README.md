# githints

![CI](https://github.com/cjrdz/githints/actions/workflows/ci.yml/badge.svg)
[![GitHub release](https://img.shields.io/github/v/release/cjrdz/githints?sort=semver)](https://github.com/cjrdz/githints/releases)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cjrdz/githints)](https://goreportcard.com/report/github.com/cjrdz/githints)

Lightweight, local change tracking for AI coding agents.

githints keeps a small SQLite journal of what changed and why, then renders it
into plain markdown under `.githints/` so any model (or human) can catch up
without special tooling.

## Quick start

```sh
# Install (requires Go 1.23+)
go install github.com/cjrdz/githints@latest

# Or build from source
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
- **Optional local Ollama summarization**: when enabled in `.githints/config.json`,
  fallback diffs are sent to a local Ollama model for a one-line caption. It is
  opt-in, timeout-bound, and never blocks a commit.
- **Pre-commit gate**: warns (or blocks with `GITHINTS_PRECOMMIT_BLOCK=1`) when
  staged files lack a pending record.
- **Tamper-evident log**: each row is HMAC-chained, `recorded_at` is
  monotonically checked, and a per-commit Merkle root is stored as a git note.

## Platforms

Linux, macOS, and Windows (with [Git for Windows](https://gitforwindows.org/)).

## Documentation

- [Architecture](docs/architecture.md) — data model, write paths, integrity
  model, and package layout.
- [Usage](docs/usage.md) — install, agent setup, CLI reference, and workflows.
- [Contributing](CONTRIBUTING.md) — build, test, code style, and how to
  contribute.

## CLI overview

```sh
githints init [-share]           # set up .githints/, install hooks, and gitignore
                                 #   -share commits rendered markdown; state stays local
githints serve                   # run the MCP stdio server
githints record -file=F -summary=S [-reason=R]
                                 # manually record a change
githints verify                  # check HMAC chain and markdown consistency
githints changes -since=T -until=T [-file=F]
                                 # timeline forensics query
githints render                  # re-render all markdown from the store
githints status                  # store health and pending records
githints rotate-salt [-force]    # rotate integrity salt and re-sign
githints version                 # print the githints version
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
