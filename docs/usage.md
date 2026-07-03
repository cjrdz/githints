# githints usage guide

How to install, set up, and use githints day-to-day.

## Install

Requires Go 1.23+.

```sh
go build -o githints .
```

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

# Rotate the integrity salt and re-sign the chain

githints rotate-salt
```

## Pre-commit gate

By default the pre-commit hook warns when staged files have no pending
`record_change` row. To make it block commits instead:

```sh
export GITHINTS_PRECOMMIT_BLOCK=1
```

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
