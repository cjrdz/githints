# githints

Lightweight, local change tracking for AI coding agents. It keeps a small
SQLite journal of what changed and why, then renders it into plain markdown
under `.githints/` so any model (or human) can catch up without tooling.

Two write paths feed the same store:

- **Agent-driven** (`record_change` MCP tool / `githints record`): the agent
  writes a concrete summary right after editing a file. Because the commit
  does not exist yet, these rows are stored as **pending** (`commit_hash = ''`)
  and shown as **uncommitted** in the markdown.
- **Hook-driven fallback** (`post-commit` git hook): after a commit, the hook
  claims any pending agent rows for the files in that commit, then records a
  generic diff-stat entry only for files the agent did **not** fully cover.

The hook also ignores anything under `.githints/` itself, so regenerating
markdown never creates self-referential noise.

```
.githints/
  store.db              # SQLite — the queryable source of truth
  CHANGES.md            # root rollup, newest first, auto-generated
  cmd/api/main.go.md    # per-file hint, mirrors your source tree
  internal/auth/token.go.md
```

## Build and install

Requires Go 1.23+.

```sh
go mod tidy
go build -o githints .
```

Put the binary on your `PATH`, or reference it by absolute path in MCP
configs. One global binary is enough — stores and hooks live inside each
repo.

## Set up in a repo

From the repo root:

```sh
githints init
```

This:

1. Creates `.githints/` (with `store.db` and an initial `CHANGES.md`).
2. Installs `.git/hooks/post-commit`, which calls `githints hook-run` using
   an absolute path to the binary — no `PATH` dependency inside the hook.

Add this to `.gitignore`:

```gitignore
.githints/
```

Commit everything else under `.githints/`. The `.md` files are small,
diffable, and are the whole point. `store.db` rebuilds incrementally as the
hook/tool fire, so it is fine to lose on a fresh clone (same tradeoff as
`node_modules`).

## Wire it into your agent

`githints serve` is a stdio MCP server. It resolves its repo root from the
process's current working directory, so **do not** register it globally in a
user-level MCP config — it would only ever see whichever repo happened to be
the cwd when it launched.

Use a **project-scoped** config instead.

### Claude Code

Drop `.mcp.json` in the repo root:

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

Claude Code launches project-scoped servers with `cwd = project root`, so
each repo gets an isolated `githints` instance.

### opencode

Use a project-level `opencode.json` per repo:

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

If the binary is not on your `PATH` (for example, you built it locally as
`./githints` in the repo root), use the project-relative path:

```json
"command": ["./githints", "serve"]
```

If your opencode version only supports a global config, create two named
entries and give each an explicit `cwd` pointing at the repo root. Check
`opencode mcp --help` or its docs for whether `cwd` is supported.

### Agent rule

Drop `AGENTS.md` (included in this repo) into the target repo, or fold its
rule into your existing `AGENTS.md` / `CLAUDE.md`:

> After editing a file, call `record_change` with the repo-relative path,
> a concrete summary, and an optional reason.

## Self-tracking

githints can track itself. Run `githints init` inside the `githints` repo.
Because the hook ignores changes under `.githints/`, markdown files that get
rewritten after a source edit are not themselves treated as source edits —
no self-referential noise loop.

## Multiple projects

- **Monorepo** (`backend/` and `frontend/` under one `.git`): run
  `githints init` once at the root. Paths are naturally scoped, e.g.
  `backend/cmd/api/main.go` and `frontend/src/App.tsx`.

- **Separate repos**: run `githints init` inside each repo. Each gets its
  own isolated `.githints/store.db`, hook, and hint tree. They must not share
  a store — a backend path and a frontend path in the same database would be
  meaningless cross-noise.

## Local-only repos (no remote, never pushed)

Push status is irrelevant. `record_change` writes only to the local SQLite
file, and the hook is `post-commit`, which fires on every `git commit` with
or without a remote.

If you edit a file, call `record_change`, edit it again, and call
`record_change` again, both rows stay pending until you commit. The hook then
claims all pending rows for that file with the new commit hash. If you never
commit, the rows simply remain labeled **uncommitted** indefinitely.

## CLI reference

```sh
githints init                                   set up .githints/ + install hook
githints serve                                  run the MCP server (stdio)
githints hook-run                               internal: called by the hook
githints record -file=F -summary=S [-reason=R]  manual agent write, for testing
```

## How the hook decides whether to record a fallback

For each file in the commit (excluding `.githints/`):

1. Claim any pending agent rows for that file.
2. If there are no agent rows for the file/hash, write a hook fallback.
3. If there are agent rows, compare diff stats:
   - Capture the working-tree diff stat when `record_change` fires.
   - After the commit, sum the agent diff stats and compare them to the
     commit's diff stat.
   - If they match exactly, the agent covered the whole change.
   - If they differ, there was an unrecorded manual tweak, so the hook adds
     a fallback entry.
   - If any agent row lacks a diff stat (e.g. first commit or an untracked
     file), the hook gives the agent the benefit of the doubt to avoid a
     duplicate fallback.

## Uninstall / clean

Remove the hook and generated folder:

```sh
rm .git/hooks/post-commit
rm -rf .githints/
```

The source tree is untouched.

## Status

Compiles, passes unit tests, and has been exercised end-to-end for:

- agent records before a commit and the pending → claimed flow,
- no hook fallback when the agent fully covers a file,
- hook fallback when a manual tweak happens after the agent's record,
- self-tracking without looping on `.githints/` files,
- separate repos with isolated stores,
- first-commit fallback for `git diff --name-only hash^`.
