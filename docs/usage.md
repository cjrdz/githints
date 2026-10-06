# githints usage guide

How to install githints, connect it to your editors and agents, and use it day
to day.

- [Install](#install)
- [Set up a repository](#set-up-a-repository)
- [Connect your editors and agents](#connect-your-editors-and-agents)
- [What to commit and what to keep local](#what-to-commit-and-what-to-keep-local)
- [Daily workflow](#daily-workflow)
- [Integrity: verify, anchors and the salt](#integrity-verify-anchors-and-the-salt)
- [Structural index](#structural-index)
- [Dependency graph](#dependency-graph)
- [Configuration](#configuration)
- [Sharing history with your team](#sharing-history-with-your-team)
- [Single repos, monorepos and multiple repos](#single-repos-monorepos-and-multiple-repos)
- [Upgrading](#upgrading)
- [CLI reference](#cli-reference)
- [Troubleshooting](#troubleshooting)
- [Uninstall](#uninstall)

## Install

Supported platforms: Linux, macOS and Windows, on amd64 and arm64.

`git` must be installed and on `PATH` — githints shells out to it for
everything that matters. On Windows install
[Git for Windows](https://gitforwindows.org/), which also provides the POSIX sh
that runs the git hooks.

### Install script (recommended)

No Go toolchain needed. Both scripts download a prebuilt binary and verify it
against the release `checksums.txt` before installing. If the checksum cannot be
checked at all (no `checksums.txt`, or no SHA-256 tool), they stop rather than
install an unverified binary. When the GitHub CLI is installed and signed in,
they also run `gh attestation verify` to confirm the archive was built by this
repository's release workflow.

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/cjrdz/githints/main/install.sh | sh
```

```powershell
# Windows
irm https://raw.githubusercontent.com/cjrdz/githints/main/install.ps1 | iex
```

| Variable | Effect |
| --- | --- |
| `GITHINTS_VERSION` | Install a specific tag instead of the latest release |
| `GITHINTS_BIN_DIR` | Install somewhere other than the default |
| `GITHINTS_INSECURE_SKIP_VERIFY=1` | Install even when the checksum cannot be checked (a mismatch is still fatal) |

The default is `/usr/local/bin` when writable, otherwise `~/.local/bin`; on
Windows, `%LOCALAPPDATA%\Programs\githints`, which is added to your user PATH.

### Native packages

Every release attaches `.deb`, `.rpm`, `.apk` and Arch `.pkg.tar.zst` packages
for amd64 and arm64. They install the binary at `/usr/bin/githints` and depend
on `git`. Download them from the
[releases page](https://github.com/cjrdz/githints/releases):

```sh
sudo pacman -U githints_<version>_linux_amd64.pkg.tar.zst   # Arch
sudo dpkg -i githints_<version>_linux_amd64.deb              # Debian, Ubuntu
sudo rpm -i githints_<version>_linux_amd64.rpm               # Fedora, RHEL
sudo apk add --allow-untrusted githints_<version>_linux_amd64.apk   # Alpine
```

### From source

Requires the Go version in `go.mod` (currently 1.26.7) or newer:

```sh
go install github.com/cjrdz/githints@latest   # into $(go env GOPATH)/bin
go build -o githints .                        # from a checkout
```

### Where the binary lives, and why it matters

`githints init` records the binary's resolved path in each repository's git
hooks, so the hooks work even when a GUI git client runs them with a minimal
`PATH`. If that path disappears (a package-manager upgrade replacing a versioned
directory, or you moving the binary), the hooks fall back to `githints` on
`PATH`. Installing to a stable location, as the scripts and packages do, avoids
relying on the fallback. After moving the binary yourself, re-run
`githints init` in each tracked repository.

## Set up a repository

From the repository root:

```sh
githints setup
```

That is `init` plus registering the MCP server with your clients (next
section). `init` on its own:

- creates `.githints/` (private to your user: mode `0700`) with the store and
  an initial `CHANGES.md`;
- installs the `post-commit` and `pre-commit` hooks wherever git runs hooks for
  this repository, honoring `core.hooksPath` and linked worktrees;
- creates the integrity salt outside the repository;
- adds managed blocks to `.gitignore`, `AGENTS.md` and `CLAUDE.md`.

If a hook already exists, `init` stops and asks: `-chain` keeps yours and runs it
first, `-force` replaces it. If `core.hooksPath` points inside the repository
(husky, lefthook), `init` refuses to write machine-specific hooks into a
committed directory and prints the two lines to add there instead.

Every other command refuses to run until `init` has. Run `githints doctor`
afterwards to check the whole setup.

### Agent instruction files

Registering the MCP server makes the tools *available*; the instruction file is
what tells an agent to use them. `init` writes a managed block, bracketed by
`<!-- >>> githints (managed) -->` and `<!-- <<< githints (managed) -->`, into:

| File | Content | Read by |
| --- | --- | --- |
| `AGENTS.md` | the workflow rules | opencode, Codex, Gemini CLI, Cursor and most other agents |
| `CLAUDE.md` | `@AGENTS.md` | Claude Code, which reads `CLAUDE.md` and follows the import |

So every client sees the same rules from one place. Content outside the markers
is never touched, and re-running `init` updates the blocks in place. The MCP
server also sends a condensed form of the rules in its `instructions` field, as
a backstop for clients that read no file.

## Connect your editors and agents

`githints serve` is a stdio MCP server. `githints setup` registers it with every
client it detects, from the client's command on `PATH` or its folder in the
repository:

```sh
githints setup                         # detected clients
githints setup -clients=claude,vscode  # exactly these
githints setup -clients=all            # every project-level client
githints setup -dry-run                # show what would be written, write nothing
githints setup -list                   # supported clients, and which are detected
githints setup -update                 # refresh githints' own entries, e.g. after putting githints on PATH
githints mcp-config <client> [-write]  # print, or add, one client's entry
```

| Client | File written | Notes |
| --- | --- | --- |
| Claude Code (`claude`) | `.mcp.json` | Warp reads it too |
| opencode | `opencode.json` | an `opencode.jsonc` with comments is left to you |
| Codex CLI | `.codex/config.toml` | read only for projects you marked trusted; pins the root |
| VS Code / Copilot (`vscode`) | `.vscode/mcp.json` | `servers` with `type: stdio` |
| Cursor | `.cursor/mcp.json` | root via `${workspaceFolder}` |
| Zed | `.zed/settings.json` | `context_servers` |
| Kiro | `.kiro/settings/mcp.json` | pins the root |
| Gemini CLI | `.gemini/settings.json` | |
| JetBrains Junie | `.junie/mcp/mcp.json` | pins the root |
| Continue | `.continue/mcpServers/githints.json` | pins the root |
| Amazon Q CLI (legacy) | `.amazonq/mcp.json` | pins the root |
| Claude Desktop | user config | global: only when named |
| Windsurf / Devin Desktop | `mcp_config.json` | global: only when named |
| Cline CLI | `~/.cline/data/settings/cline_mcp_settings.json` | global: only when named |
| Goose, JetBrains AI Assistant | — | prints the steps to follow |

What `setup` will and will not do:

- It only ever adds githints' own entry. Other servers and settings are kept,
  and an existing githints entry is left alone unless you pass `-update`.
- A file with comments (JSONC) is never rewritten; you get the entry to paste.
  Codex's TOML is only appended to.
- "Pins the root" means the entry carries this checkout's absolute path,
  because that client does not document starting servers in the project
  directory.
- Global configs are shared by every project on the machine, so each repository
  gets its own entry (`githints-<repo>-<hash>`), and the file is backed up to
  `<file>.githints-backup` before the first change.
- If `githints` is not on `PATH`, entries use the binary's absolute path and
  `setup` says so. Global entries always do, since GUI apps often start with a
  minimal `PATH`.

Restart or reload the client afterwards so it starts the server.

### Writing an entry by hand

Every client needs the same two things: a command that runs `githints serve`,
and a repository root. `githints mcp-config <client>` prints the exact entry. For
example, Claude Code's `.mcp.json`:

```json
{
  "mcpServers": {
    "githints": { "type": "stdio", "command": "githints", "args": ["serve"] }
  }
}
```

A client that starts servers somewhere other than the repository needs the root
pinned:

```sh
githints serve -root=/path/to/repo             # flag
GITHINTS_ROOT=/path/to/repo githints serve     # environment
```

Precedence is the flag, then `GITHINTS_ROOT`, then `CLAUDE_PROJECT_DIR` (which
Claude Code sets for the servers it starts), then the working directory.
Nothing but JSON-RPC is ever written to stdout, so no client needs special
handling.

## What to commit and what to keep local

| Path | Commit it? |
| --- | --- |
| `.githints/` | **No**, in the default private mode: `init` gitignores it. It holds the store, the index and your rendered notes. In shared mode (`init -share`) the rendered markdown is meant to be committed and the state files stay ignored; see [Sharing history](#sharing-history-with-your-team). |
| The integrity salt | **Never.** It is the key to the change log and lives outside the repository (`githints salt path`). Back it up with `githints salt export`. |
| `AGENTS.md`, `CLAUDE.md` (managed blocks) | **Yes.** They tell every agent on the team to use githints. |
| `.gitignore` (managed block) | **Yes.** |
| Project MCP configs (`.mcp.json`, `.vscode/mcp.json`, ...) | **Only if portable**: when they name `githints` (on `PATH`) and do not pin an absolute root. Entries that pin the root, or point at an absolute binary path, are specific to your machine. `githints setup` tells you which ones those are. |
| `*.githints-backup` | **No.** Backups of global client configs, kept next to them. |
| `.githints/graph.html` | **No** (it is inside `.githints/`). Export elsewhere with `-o` if you want to publish a graph. |
| `refs/notes/githints` | **Push it** (`git push origin refs/notes/githints`) if you want the Merkle anchors to exist anywhere but your machine. Git does not push notes by default. |

## Daily workflow

1. Edit a file.
2. Record it: the agent calls `record_change` (or `record_batch`) with a concrete
   summary and, where it is not obvious, a reason. By hand:
   `githints record -file=F -summary="..." -reason="..."`.
3. Commit as usual. The post-commit hook ties the pending records to the commit
   and writes a fallback entry for any changed file nobody described, so nothing
   is silently lost.

Reading it back, from MCP or the CLI:

| Question | MCP tool | CLI |
| --- | --- | --- |
| Where do I start? | `get_session_context()` | `githints status` |
| Why is this file like this? | `get_file_history(file)` | `githints history -file=F` |
| What happened recently? | `get_recent_changes(limit)` | `githints recent` |
| Where did we change X? | `search_changes(query)` | `githints search -query=Q` |
| What changed between two dates? | `get_changes_in_range(since, until)` | `githints changes -since=T -until=T` |
| What does the diff really say? | `get_diff(file, hash?)` | `githints diff -file=F [-hash=H]` |

Diffs are redacted: hunks of credential-carrying files (`.env*`, `*.pem`,
`*.key`, `id_rsa*`, `*secret*`, ...) and lines matching a known secret shape come
back as `[REDACTED SECRET LINE]`. They are truncated at about 128 KiB with an
explicit marker. `search` takes an FTS5 `MATCH` expression, so `NEAR`, prefix
`*` and column filters work.

### Limits

Writes and reads are bounded, and exceeding a limit is an error, never a silent
truncation: summary and reason 4000 bytes each, `agent_id` 128 bytes, batches of
at most 100 (atomic: all rows land or none), search queries 500 characters, every
`limit` between 1 and 500. A summary or reason carrying a recognizable credential
(cloud or API key, GitHub or Slack token, private key, JWT) is refused. Control
characters, terminal escapes, bidi overrides and zero-width characters are
removed before storing.

### Pre-commit gate

The `pre-commit` hook warns when a staged file has no pending record. To make it
block the commit instead:

```sh
export GITHINTS_PRECOMMIT_BLOCK=1
```

Only that case blocks. If githints itself fails (a locked store, a bad config),
the hook prints the problem and lets the commit through.

## Integrity: verify, anchors and the salt

```sh
githints verify
```

checks three things and exits 3 if any fails:

- **The HMAC chain.** Every row is signed with a key derived from the salt and
  your git `user.email`, and linked to the previous row.
- **The rendered markdown** matches the store.
- **The Merkle anchors.** After each commit the hook writes a note to
  `refs/notes/githints` with a Merkle root of that commit's rows and of the whole
  log so far. Edited, deleted or re-pointed rows fail here even if someone
  re-signed the whole chain with the salt.

The chain alone cannot stop someone running as you, who can read the salt; the
anchors are the check for that case, and they are only as external as wherever
you push them. See [SECURITY.md](../SECURITY.md#threat-model).

The salt lives outside the repository, keyed by an id in `.githints/repo-id`, so
moving or renaming the checkout keeps it:

```sh
githints salt path                   # where it is
githints salt export -o salt.txt     # back it up; keep it as private as a password
githints salt import salt.txt        # restore it, e.g. on a new machine
githints rotate-salt [-force]        # new salt, every row re-signed
```

`rotate-salt -force` re-signs rows even when the chain does not verify, which
also discards the tamper evidence; use it only when the old salt is gone for
good.

## Structural index

Alongside the change log, githints keeps a **structural index**: a SQLite cache
(`.githints/index.db`) of the symbols, imports and framework roles in the
repository, a markdown note per file under `.githints/index/`, and a rollup at
`.githints/INDEX.md`. It is a derived cache, not part of the integrity-verified
log, so rebuilding it is always safe.

```sh
githints index                 # full rebuild
githints index status          # counts, languages, last scan
githints index verify          # stale notes, ghost rows, uncovered files (exit 1 on drift)
githints index languages       # what this binary can index, and what is enabled here
githints index facets -facet=route   # framework constructs by role
```

The post-commit hook refreshes the files each commit touched. Use
`githints index --force` to rebuild past the partial-write and size guards.

| Agent question | MCP tool |
| --- | --- |
| What is defined in this file? | `list_symbols(file)` |
| Where is this symbol defined? | `find_symbol(name)` |
| What breaks if I change this file? | `get_dependents(file)` |
| What does its neighbourhood look like? | `get_dependency_graph(file, depth)` |
| Is the index fresh, and what are the hubs? | `get_index_summary(limit)` |
| Where are the routes / models / components? | `find_facets(facet)` |

Every response includes `last_indexed_at`. Without the MCP server, read the notes
under `.githints/index/` and `.githints/INDEX.md`.

### Languages

`index.languages` selects from the languages the binary supports (default
`["go"]`):

| Language | Extensions | Parser |
| --- | --- | --- |
| `go` | `.go` | `go/parser` (exact) |
| `typescript` | `.ts` `.tsx` `.mts` `.cts` `.js` `.jsx` `.mjs` `.cjs` | heuristic |
| `vue`, `svelte`, `astro` | `.vue` `.svelte` `.astro` | script blocks, via the TypeScript parser |
| `python` | `.py` `.pyi` | spec |
| `rust`, `java`, `csharp`, `php` | `.rs` `.java` `.cs` `.php` | spec |
| `sql`, `prisma` | `.sql` `.prisma` | spec |

`githints index languages` is authoritative for the binary you are running. A
repository can add languages of its own as JSON specs in `.githints/langs/`; see
[Extending the index](extensibility.md).

### How imports are resolved

An import becomes an edge in "Imported by", hub rankings and the dependency
graph only when it resolves to a file in the repository. Resolution is per
package, so a monorepo connects across its packages:

- **Go**: the module path of the nearest `go.mod` at or above each file. Every
  module of a multi-module repo (with or without `go.work`) resolves.
- **TypeScript family**: relative imports; `paths` aliases from the nearest
  `tsconfig.json`/`jsconfig.json` that defines them, following relative
  `extends`; and workspace packages by their `package.json` name (`@acme/ui`,
  `@acme/ui/button`), mapped to source files that exist (a `main` pointing at
  `dist/` is tried as `src/`). Everything else, like `react`, is an external
  package.
- **Python**: modules are named from the nearest project root (`pyproject.toml`,
  `setup.py`, `setup.cfg`) and its `src/`, so `services/api/src/app/x.py` is
  `app.x`. Relative imports (`from .db import x`) are resolved. Without a project
  file, names are repo-relative (`app/service.py` is `app.service`).
- **Java**: `src/main/java` (and its test and Kotlin variants) is stripped in
  every module.
- Rust, C# and PHP files contribute symbols and outbound imports, but no inbound
  edges yet.

### Framework facets

Facets name the *role* a construct plays, independently of the framework:
Django, GORM, Prisma, SQLAlchemy and Eloquent models are all `model`; chi, Flask,
FastAPI, Spring and Laravel routes are all `route`. The facets are `route`,
`model`, `component`, `migration`, `job` and `test`. Shipped detectors: django,
flask, fastapi, sqlalchemy, chi, gorm, bun, react, vue, spring, eloquent,
entityframework, tokio, prisma.

Detection is gated on a file's imports or path, so a framework is only claimed
where it is actually used, and it runs over comment- and string-stripped lines so
an example in a docstring is not reported. Facets appear in each note under
`## Framework`.

### Excluding files

The index skips everything `.gitignore` ignores. A `.githintsignore` at the
repository root (same syntax) excludes more, such as generated code or fixtures.
It can only subtract: it cannot re-include an ignored file.

Signatures stored in the index have string and template contents blanked, so
source strings never reach the cache.

## Dependency graph

```sh
githints index graph                          # -> .githints/graph.html
githints index graph -focus=src/api/user.ts   # a file's neighbourhood (-depth=2 by default)
githints index graph -external                # include stdlib and third-party packages
```

The page is a single offline file: no external scripts or styles, no network
requests (its Content-Security-Policy forbids them), and file names on screen as
text only. It has a force-directed layout grouped by directory with node size by
how many places import it; colour by language, directory or framework role;
hover details and a side panel of imports, importers and package files; search
(`/`), a sortable table view, `#<name>` links to a node, and light and dark
themes.

Nodes are files where a file is a module (TypeScript, Python) and packages where
many files share an import path (Go). Graphs past 1500 nodes keep the most
connected ones; narrow with `-focus`, or raise `-max-nodes`.

Other formats go to stdout, or to a file with `-o`:

```sh
githints index graph -format=mermaid -focus=main.go -depth=1   # GitHub issues and READMEs
githints index graph -format=dot | dot -Tsvg > graph.svg       # Graphviz
githints index graph -format=json                              # nodes and edges, for scripts
```

Agents get the same graph from `get_dependency_graph`.

### Obsidian

`githints index --obsidian` (or `"obsidian_wikilinks": true`) renders the index
notes with `[[wikilinks]]`, so opening `.githints/` as an Obsidian vault gives
Obsidian's graph view. The first full scan seeds `.githints/.obsidian/graph.json`
with a `path:index` filter (never overwriting your own settings), so the graph
opens on the code rather than on the change-history notes, which link to nothing.
File names containing `[`, `]` or `|` are URL-encoded in link targets, so links
never break.

## Configuration

Per-repository settings live in `.githints/config.json`. Every key is optional:

```json
{
  "index": {
    "enabled": true,
    "languages": ["go", "typescript"],
    "max_bytes": 209715200,
    "max_file_size": 1048576,
    "parse_timeout_ms": 5000,
    "obsidian_wikilinks": false,
    "graph_html": false
  },
  "ollama": {
    "enabled": false,
    "endpoint": "http://127.0.0.1:11434",
    "model": "qwen2.5:3b-instruct",
    "timeout_ms": 3000,
    "max_diff_bytes": 4096
  }
}
```

| Key | Meaning |
| --- | --- |
| `index.enabled` | Build the index (and refresh it after each commit). |
| `index.languages` | Languages to index; see [Languages](#languages). |
| `index.max_bytes` | Size cap for `index.db` (at most 1 GiB). A full rebuild refuses past it; the hook stops at the file that would cross it and says so. |
| `index.max_file_size` | Files larger than this are skipped (at most 64 MiB). |
| `index.parse_timeout_ms` | Per-file parse budget (at most 60 s). |
| `index.obsidian_wikilinks` | Render index notes with `[[wikilinks]]`. |
| `index.graph_html` | Keep `.githints/graph.html` current after `githints index` and every commit. |
| `ollama.*` | Optional local summarization; see below. |

Environment variables override the file: `GITHINTS_INDEX_ENABLED`,
`GITHINTS_INDEX_LANGUAGES` (a comma-separated list that replaces the configured
set), `GITHINTS_INDEX_MAX_BYTES`, `GITHINTS_INDEX_MAX_FILE_SIZE`,
`GITHINTS_INDEX_PARSE_TIMEOUT_MS`, `GITHINTS_INDEX_OBSIDIAN_WIKILINKS`,
`GITHINTS_INDEX_GRAPH_HTML`, and `GITHINTS_OLLAMA_ENABLED`, `_ENDPOINT`,
`_MODEL`, `_TIMEOUT_MS`, `_MAX_DIFF_BYTES`.

Other variables: `GITHINTS_ROOT` (pin the served repository),
`GITHINTS_PRECOMMIT_BLOCK=1` (make the gate block), `GITHINTS_SALT_DIR` (where
salts live), `GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK=1` (see below).

`config.json` is repository content, so it can arrive in a clone. Its limits are
bounded for that reason, and githints says so on stderr when a repository's own
config turns Ollama on.

### Optional local Ollama summarization

Off by default. When enabled, the post-commit hook asks a local Ollama model for
a one-line caption of each fallback diff (scrubbed of secrets and truncated to
`max_diff_bytes` first), and `get_diff` / `get_recent_changes` accept
`summarize=true`. If Ollama is unreachable, slow or returns something unusable,
githints falls back to the generic text immediately; a commit never waits on it.

The endpoint must resolve to a loopback address, checked when the config loads
and again on the exact address dialed. `GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK=1`
lifts that.

## Sharing history with your team

By default `.githints/` is gitignored and everything stays on your machine. To
let the rendered markdown travel with the repository, initialize in shared mode:

```sh
githints init -share
```

Then only the state files are ignored, and `CHANGES.md` and the per-file hints
can be committed:

```gitignore
# >>> githints (managed)
.githints/store.db*
.githints/index.db*
.githints/.salt
.githints/.salt.new
.githints/repo-id
.githints/config.json
# <<< githints (managed)
```

The hook renders markdown *after* a commit, so updated hints land in the next
commit. If `CHANGES.md` or a hint conflicts in a merge, run `githints render` to
regenerate every file from the store.

## Single repos, monorepos and multiple repos

- **Single repository**: the default; run `githints setup` once.
- **Monorepo**: run `githints setup` once at the root. History, search and
  verify cover the whole tree, and the index resolves imports per package (see
  [How imports are resolved](#how-imports-are-resolved)). Use
  `githints index graph -focus=...` to look at one package. Two Python projects
  that both have a top-level package named `app` still share that name, so
  imports between such projects can attach to the wrong one.
- **Multiple repositories**: run `githints setup` in each. Stores, salts, hooks
  and MCP servers are separate, and there is no cross-repository view. Do not
  share a `store.db` between repositories.

## Upgrading

Install the new version the way you installed the old one, then in each tracked
repository:

```sh
githints init      # repoints the hooks if the binary path changed
githints index     # rebuild the index with the new version's resolution
githints doctor
```

The post-commit hook only rescans changed files, so after an upgrade that
changes import resolution, the hook and `doctor` both say when a full
`githints index` is needed. MCP clients pick up the new binary when they next
start the server.

## CLI reference

Every command takes `-h`. Exit status: 0 ok, 1 error, 2 usage error, 3 `verify`
found problems.

| Command | Does |
| --- | --- |
| `setup [-clients=a,b\|all] [-dry-run] [-list] [-update] [-share] [-chain]` | `init` if needed, then register the MCP server with clients |
| `init [-chain\|-force] [-share]` | Set up `.githints/`, the hooks, the salt and the managed blocks |
| `mcp-config CLIENT [-write] [-update]` | Print, or add, one client's MCP entry |
| `doctor` | Check everything githints depends on; prints a fix per problem; changes nothing |
| `status` | Store health, hooks, salt, pending records |
| `serve [-root=PATH]` | Run the MCP stdio server |
| `record -file=F -summary=S [-reason=R] [-agent-id=A]` | Record a change by hand |
| `history -file=F [-limit=N]` | A file's recorded history |
| `recent [-limit=N]` | Latest changes across the repository |
| `search -query=Q [-limit=N]` | Full-text search of summaries and reasons |
| `diff -file=F [-hash=H]` | Redacted diff of the working tree, or of one commit |
| `changes -since=T -until=T [-file=F] [-limit=N]` | Changes in a time range (RFC 3339 or Unix seconds) |
| `verify` | Check the HMAC chain, the rendered markdown and the Merkle anchors |
| `render` | Regenerate the change-log markdown from the store |
| `rotate-salt [-force]` | New salt, every row re-signed |
| `salt path\|export [-o FILE]\|import [-force] FILE` | Locate, back up or restore the salt |
| `index [-force] [-obsidian]` | Rebuild the structural index |
| `index status` / `verify` / `languages` | Index statistics / drift report / supported languages |
| `index facets [-facet=] [-framework=] [-file=] [-limit=]` | Framework constructs by role |
| `index graph [-format=html\|json\|dot\|mermaid] [-o=FILE] [-focus=F] [-depth=N] [-external] [-max-nodes=N]` | Export the dependency graph |
| `version` / `help` | Version with commit and build date / command list |

## Troubleshooting

Start with `githints doctor`. It checks git, the hooks (installed where git runs
them, executable, pointing at a binary that exists), the config, the store's
SQLite integrity, the salt, the HMAC chain, the Merkle anchors, the index and
MCP registration, and prints a fix for each problem.

**"githints is not set up in this repository".** Run `githints setup` (or
`init`). Only `init` creates `.githints/`.

**Hooks never run.** If `core.hooksPath` is set, `init` installs there; if it
points inside the repository (husky, lefthook), add `githints hook-run` to your
post-commit hook and `githints hook-precommit` to your pre-commit hook. If you
already had a hook, `githints init -chain` keeps it running first.

**Every row fails `verify`.** The key is derived from the salt and your git
`user.email`, so a change to either fails every row at once, and `verify` says
so. Set the old email back for the repository, or restore the salt with
`githints salt import`. Only if neither is possible, `githints rotate-salt -force`.

**"integrity salt missing".** The log has signed rows but this machine has no
salt for it. githints refuses to create a new one, since every row would fail.
Export the salt where it still exists and import it here.

**`merkle anchors` problems.** Rows were changed, deleted or moved after a commit
anchored them. If you did not do it, treat it as tampering.

**The MCP server fails to start (`CONNECTION_CLOSED`, "failed").** The client
cannot find `githints` on its `PATH`. On Windows, a PATH change reaches only
programs started afterwards, and Windows Terminal passes its own launch-time
environment to every new tab: close the terminal app completely, and any open
editor or agent, then reopen it and check that `githints version` works. Or run
`githints setup -update` from a shell where githints is not on `PATH`, which
writes the binary's absolute path into the configs instead.

**The agent does not use githints.** Check `githints doctor`'s `mcp` line,
restart the client, and make sure `AGENTS.md` / `CLAUDE.md` have the managed
block.

**Index links are missing after an upgrade.** Run `githints index` once.

## Uninstall

From the repository root:

```sh
# The hooks, wherever git runs them. If init -chain kept an older hook,
# move it back afterwards (mv post-commit.pre-githints post-commit).
hooks=$(git rev-parse --git-path hooks)
rm "$hooks/post-commit" "$hooks/pre-commit"

# The salt lives outside the repository; remove it before deleting .githints/,
# which holds the id that names it.
rm "$(githints salt path)"

# The store, rendered markdown, index and config.
rm -rf .githints/

# The Merkle anchors (and on the remote too, if you pushed them).
git update-ref -d refs/notes/githints
```

Then delete the `githints (managed)` blocks from `.gitignore`, `AGENTS.md` and
`CLAUDE.md`, and the githints entry from your MCP client configs (for global
ones, `<file>.githints-backup` has the original). Your source tree is otherwise
untouched.
