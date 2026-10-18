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
| `AGENTS.md` | the workflow rules | opencode, Codex, Gemini CLI, Cursor, Pi and most other agents |
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
| Pi (`pi`) | `.mcp.json` | read through the pi-mcp-adapter extension; shares the file with Claude Code |
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
| `.githints/notes/`, `.githints/notes.html` | **No**, in either mode. Notes are private by decision: they can hold whatever an agent was told. Back them up yourself if they matter. |
| `refs/notes/githints` | **Push it** (`git push origin refs/notes/githints`) if you want the Merkle anchors to exist anywhere but your machine. Git does not push notes by default. |

## Daily workflow

1. Edit a file.
2. Record it: the agent calls `record_change` (or `record_batch`) with a concrete
   summary and, where it is not obvious, a reason. By hand:
   `githints record -file=F -summary="..." -reason="..."`.
3. Commit as usual. The post-commit hook ties the pending records to the commit
   and writes a fallback entry for any changed file nobody described, so nothing
   is silently lost.

Later, when you know how it turned out, record that too:
`record_outcome(change_id, outcome, note)` or
`githints outcome -id=N -state=failed -note="..."`. See
[Outcomes](#outcomes).

Reading it back, from MCP or the CLI:

| Question | MCP tool | CLI |
| --- | --- | --- |
| Where do I start? | `get_session_context()` | `githints brief` |
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

### Exporting

`githints export -o DIR` writes the log as plain files for people and notebooks:

| File | What it is |
| --- | --- |
| `changes.jsonl` | Every row, one JSON object per line, exact. Includes outcome rows (`kind: "outcome"`) and each row's `hmac`. |
| `changes.csv` | The same columns. Any cell that would run as a spreadsheet formula (it starts with `=`, `+`, `-` or `@`, after any leading whitespace) gets a leading `'`, so a summary cannot execute in Excel or Sheets; use the JSONL when you need the text exactly. |
| `CHANGES-full.md` | Every change, newest first, grouped by commit, with its reason and its newest outcome. `CHANGES.md` stops at 100 rows. |
| `hints/<file>.md` | The per-file history for every file, regenerated from the store. |
| `notes/` | The notes githints accepted, in canonical form (`-no-notes` leaves them out). |
| `manifest.json` | Counts, the sha256 and size of every other file, the whole-log Merkle root, and what `githints verify` said at export time (chain, clock tamper, anchors). Written last: a directory without one did not finish. |
| `githints.duckdb.sql` | With `-duckdb`: a script that loads the pack into DuckDB. |

```sh
githints export -o ~/exports/myrepo-2026-10 -duckdb
cd ~/exports/myrepo-2026-10 && duckdb githints.duckdb < githints.duckdb.sql
```

The script defines `log` (every row, typed), `changes`, `outcomes`,
`failed_attempts` (changes whose newest outcome is failed, partial or reverted),
`changes_per_file` and, when notes were exported, `notes`. It reads the files
beside it by fixed names and contains none of the log's text, so there is nothing
in it for a summary to inject into. DuckDB is a place to explore the pack, not
something githints runs or depends on.

What it will not do:

- **Overwrite.** `DIR` must not exist or must be an empty directory, and nothing
  is ever written over an existing file. Export into a new directory each time.
  `DIR` cannot be inside `.githints/`.
- **Copy state.** The pack is built from queries on the store and the notes, never
  by copying `.githints/`, which also holds the integrity salt, the repository id
  and the databases. It contains no salt or key, and no path of the machine that
  made it: when the salt is missing the manifest says the chain is `unavailable`
  in fixed words, not with the path.
- **Make notes safe to render.** `notes/` holds each note's markdown exactly as
  written, so a previewer would fetch a hostile note's images and run its embedded
  content. Open the pack's notes as text unless you trust their authors; the
  manifest says so too.
- **Prove anything.** A pack is not signed. Whoever holds a copy can change it, and
  the manifest's hashes only detect accidents. `githints verify` in the repository
  is the check, and `log.root` in the manifest is the value to compare with the
  newest `githints-log-root` in `refs/notes/githints`.

A pack written when `githints verify` would have failed is still written, with
`verify` saying so and a warning on stderr: a log worth investigating is worth
exporting.

### Notes

A change log says what changed. A note holds what it cannot: why an approach was
chosen, the plan for a refactor, a bug that took an afternoon to understand.
Notes are markdown files in `.githints/notes/`, so they are yours to read and
write by hand, and an agent can leave one so the next session does not
rediscover it.

```sh
githints note record -title="Why token rotation moved" -body=- -tags=auth \
    -files=internal/auth/token.go < why.md
githints note search -query=rotation          # words, as prefixes, all required
githints note search -file=internal/auth/token.go
githints note list | get NAME | delete NAME | render
```

The MCP tools are `record_note` and `search_notes` (`name` reads one in full).
`get_session_context` and `githints brief` list the newest.

A note is a file named with lowercase letters, digits and hyphens, then `.md`,
with optional front matter:

```markdown
---
title: Auth Refactor Plan
tags: [auth, refactor]
related_files: [internal/auth/token.go]
related_changes: [142, 143]
---

Move token rotation out of the handler. See [[token-notes]] and
[[internal/auth/token.go]].
```

The front matter is a small subset of YAML read by hand: flat `key: value` lines,
lists on one line (`[a, b]`), nothing else. Other keys are ignored. A file with
none takes its title from its first `# ` heading or its name. `[[name]]` links a
note and `[[path/to/file.go]]` a file; searching by `-file` finds notes that
name a file either way. Links are not followed anywhere: they relate notes to
each other and to files in the index.

What githints does about notes being untrusted text:

- **Not integrity-checked.** Nothing signs a note. Whoever can write
  `.githints/notes/` can write a note, and everything that shows one says so.
- **Never instructions.** A note reaches the model like a summary does: flattened,
  stripped of terminal escapes, labelled as data.
- **Credentials are refused.** After control and zero-width characters are
  stripped, a note that matches a known secret pattern is rejected when recorded
  and left out of the index if a hand edit adds one, which also stops its old
  text being served.
- **Opened as text, not rendered.** A note is markdown, and a markdown previewer
  (or an editor with plugins that run templates) can fetch images and run
  embedded content. githints never renders note text as markup, but the files are
  yours to open: do that with notes you trust, and treat a note that arrived from
  a clone like any file from one.
- **Plain files only.** Every read goes through `internal/safefs`, and a note
  that is a symlink, or a `notes/` that is one, is refused (a link inside
  `.githints/` would otherwise let `notes/x.md` read `store.db`). A note with more
  than one hard link is refused too: a second name for a state file such as the
  legacy `.salt` is an ordinary file as far as `Lstat` can tell, so the link count
  is read from the open file (on Windows, from the file handle). Git clones cannot
  carry hard links, so this guards the local directory.
- **The index is not believed.** The search index is a cache in a file anyone who
  can write it can edit. A note is checked again against every rule before it is
  shown in full or exported; a row that fails is dropped and the file read again.
- **Bounded.** Body 32 KiB, 20 tags, 50 related files and changes, 100 links, at
  most 500 notes; a hand-edited file over 128 KiB is skipped. `githints note list`
  says which files were left out and why.

`githints note render` writes `.githints/notes.html`: one offline page to browse,
search and filter notes by tag. It cannot reach the network (a
`default-src 'none'` policy pins its single script and style by hash) and it
shows note text as plain text, not rendered markdown, because rendering would
mean building markup from text another agent wrote. It is refreshed whenever a
note is recorded or deleted and when `note list` notices a hand edit.

Notes are searched through tables in `index.db`, which is rebuilt from the files
at any time and kept even when `index.enabled` is false (the file then holds only
notes). A structural-index reset does not touch them.

### The session brief

`githints brief` prints, and `get_session_context` ends with, one digest in place
of the usual round of lookups. Sections are ranked, and a lower one is dropped
before a higher one is cut:

1. **Attempts that failed, partly worked or were reverted** (outcomes), newest
   first. A change later marked `worked` is not listed.
2. **Uncommitted** records, newest first, with the total.
3. **Recent commits** and the files they touched.
4. **Blast radius**: which files import the packages changed most recently, from
   the structural index (with its age, since it can be stale). Test files are
   skipped; they are not imported. Absent when there is no index.
5. **Files that change together**: other files recorded in at least two of the
   same commits as a recently changed file.
6. **Notes**: the newest few, flagged as not integrity-checked.

The size is capped by a token budget (`-budget=N`, 200 to 8000, default 1500),
estimated as bytes / 4 and enforced in Go, so the digest never exceeds it:
sections are listed whole-line by whole-line, and one is shown only if its
heading and first item fit. Every stored string is flattened to one line,
stripped of terminal escapes and clipped, and the digest says it is data rather
than instructions. It reads the store without re-verifying the chain, so run
`githints verify` when integrity matters.

### Outcomes

A change says what was done; an outcome says how it went. `history`, `recent` and
the per-file hints list every change with a `#id` and, underneath, its newest
outcome:

```text
[2026-10-16T14:02:11Z] #41 internal/auth/token.go (agent, a1b2c3d4): Added a retry loop to Refresh
    outcome: failed — deadlocks when the token endpoint is slow; the lock is held across the retry
```

The states are `worked`, `partial`, `failed`, `reverted` and `unknown`. A note is
required for `partial`, `failed` and `reverted`: "failed" without a reason is the
mistake outcomes exist to stop someone repeating. An outcome is appended, never
edited, so recording a second one supersedes the first in the listing while both
stay in the log. It is signed into the same HMAC chain as every change, and
`githints verify` also checks that it points at an earlier change whose diff is
still the one it was recorded against.

**Reverts are noticed automatically.** When the post-commit hook sees a commit
whose message says `This reverts commit <hash>` (what `git revert` writes), it
marks the recorded changes of that commit `reverted`, with a note naming the
reverting commit and the label `(auto-detected)` so it is not mistaken for
something an agent asserted. A commit message is only a claim, so the hook checks
the tree before believing it: a change is marked only if the reverting commit
touched its file *and* the file now holds exactly what it held before the
reverted commit. A forged message, a conflicted revert, and the files a partial
revert left alone are not marked. The marker is stored under the reverting
commit, so that commit's Merkle anchor covers it, and running the hook twice
records it once.

A revert of a rename is detected: for this check the old and the new path both
count as touched, because git's default rename detection shows only the old one
while the change being undone was recorded under the new.

What it cannot see: `git reset` and force-pushes leave no revert commit, and
amend, rebase and cherry-pick are not followed. If a revert is itself reverted
the original change still reads `reverted`; record a `worked` outcome to correct
it.

Outcome rows are not changes: they do not appear in search, `CHANGES.md`, the
status counts, or the rows a commit claims.

Older githints binaries do not know the outcome fields. Opening a store that has
outcomes with one makes `verify` report those rows as not recomputing, and lists
them as ordinary changes. Upgrade every machine that shares a store.

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
  your git `user.email`, and linked to the previous row. Outcome rows are checked
  too: each must point at an earlier change whose diff is still the one it was
  recorded against.
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
githints index verify          # stale notes, ghost rows, stale import keys, uncovered files (exit 1 on drift)
githints index languages       # what this binary can index, and what is enabled here
githints index facets -facet=route   # framework constructs by role
```

The post-commit hook refreshes the files each commit touched, and its cost
follows the commit rather than the repository: on a 9,000-file C++ tree a
one-file commit takes about 130 ms. A full rebuild happens in one transaction, so
the MCP server never reads a half-written index. Use `githints index --force` to
rebuild past the partial-write and size guards.

During a scan githints runs the garbage collector less often (`GOGC=200`) under
a soft 256 MiB memory limit; a `GOGC` or `GOMEMLIMIT` you set yourself is left
alone.

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

`index.languages` selects from the languages the binary supports. The default,
`["auto"]`, enables all of them, plus any the repository adds in
`.githints/langs/`: each file is matched to a language by its extension, so a
language with no files costs nothing. List names instead to narrow it, for
example `["go", "typescript"]`; files of a supported language left out are
counted under "unsupported files", and `githints index` names the language.

| Language | Extensions | Parser |
| --- | --- | --- |
| `go` | `.go` | `go/parser` (exact) |
| `typescript` | `.ts` `.tsx` `.mts` `.cts` `.js` `.jsx` `.mjs` `.cjs` | heuristic |
| `vue`, `svelte`, `astro` | `.vue` `.svelte` `.astro` | script blocks, via the TypeScript parser |
| `python` | `.py` `.pyi` | spec |
| `rust`, `java`, `csharp`, `php` | `.rs` `.java` `.cs` `.php` | spec |
| `kotlin`, `swift`, `dart`, `ruby` | `.kt` `.kts` `.swift` `.dart` `.rb` `.rake` | spec |
| `dockerfile` | `Dockerfile`, `Containerfile`, `Dockerfile.*`, `*.Dockerfile` | spec |
| `yaml` | `.yml` `.yaml` (lock files such as `pnpm-lock.yaml` are skipped) | spec |
| `terraform` | `.tf` `.tofu` | spec |
| `makefile` | `Makefile`, `makefile`, `GNUmakefile`, `.mk` | spec |
| `sql`, `prisma` | `.sql` `.prisma` | spec |
| `c` | `.c` | spec |
| `cpp` | `.cpp` `.cc` `.cxx` `.c++`, headers `.h` `.hpp` `.hh` `.hxx` `.h++` `.ipp` `.tpp` `.inl` | spec |
| `cmake` | `CMakeLists.txt`, `.cmake` | spec |
| `css`, `scss`, `less` | `.css` `.pcss` `.postcss` (not `*.min.css`), `.scss`, `.less` | spec |

A `.h` file cannot say whether it is C or C++, so headers belong to `cpp`, whose
rules cover C's declarations too. In a C project, enable both (`auto` does).

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
  `app.x`. Relative imports (`from .db import x`) are resolved, and
  `from app.routers import items` links to `app/routers/items.py` when `items` is
  a module rather than a name defined in the package. Without a project file,
  names are repo-relative (`app/service.py` is `app.service`).
- **Java**: `src/main/java` (and its test and Kotlin variants) is stripped in
  every module.
- **PHP**: a class is named by the PSR-4 `autoload` (and `autoload-dev`) map of
  the nearest `composer.json` that maps it, so with `"App\\": "app/"`,
  `app/Models/User.php` is `App\Models\User` and `use App\Models\User;` links
  to it. Grouped `use App\Models\{User, Post};` imports each member. A class
  used from its own namespace without a `use` is not an edge, and a project
  without composer (CodeIgniter 3) has no PHP edges.
- **C#**: a file is named by the namespace it declares (`namespace App.Models;`),
  which `using App.Models;` names; files sharing a namespace are one node, like a
  Go package.
- **Rust**: a file is named by the crate module tree, with the crate's name from
  `Cargo.toml`: `src/handlers/todos.rs` is `todo_api::handlers::todos`.
  `use crate::`, `self::` and `super::` paths (and grouped `use crate::{a, b}`)
  link to the deepest module file that exists. Other crates, including other
  members of a workspace, are not linked.
- **Kotlin**: a file is named by the package it declares, so a package's files
  are one node; `import com.x.data.User` links to package `com.x.data`.
- **Swift**: a file is named by its Swift Package Manager target
  (`Sources/<Target>/`), which `import Target` names; files of one module never
  import each other. Xcode projects without a `Package.swift` have no edges.
- **Dart**: files are named by path. `import 'package:app/x.dart'` resolves
  through the nearest `pubspec.yaml` named `app` to its `lib/x.dart`, and a
  relative import to the importing file's directory.
- **Terraform**: a module is a directory, so its `.tf` files are one node, and
  `source = "./modules/vpc"` links to it.
- **YAML**: a GitHub workflow calling a reusable workflow in the repository
  (`uses: ./.github/workflows/release.yml`) links to it. YAML files, Dockerfiles
  and Makefiles appear in the graph only when such a link connects them, so
  configuration does not crowd out the code.
- **C and C++**: files are named by path. A compiler finds headers in the
  directories the build passes it (`-I`), which only the build knows, so the
  common layouts are searched instead: `#include "x.h"` is looked for beside the
  including file first, then both forms are looked for in each directory from
  the including file's up to the root, and in its `include/` and `src/`. So
  `<mylib/api.h>` from `libs/core/src/api.c` finds
  `libs/core/include/mylib/api.h`, the nearest match winning as it would for a
  compiler. A header found nowhere (`<stdio.h>`, `<QWidget>`, `<gtest/gtest.h>`)
  is external. Functions defined outside their class are named with it
  (`Widget::paint`), so `find_symbol("Widget")` lists the class and its members.
- **CSS, Sass and Less**: a stylesheet is named by its path, so a script's
  `import "./app.css"` or `import styles from "./Button.module.css"` (through
  `tsconfig` aliases too) links to it. `@import`, `@use` and `@forward` resolve
  the way Sass does: the path as written, with an extension, as a partial
  (`_variables.scss`), or as a folder's `_index.scss`. A package
  (`bootstrap/scss/bootstrap`, `tailwindcss`) is external, webpack's `~` dropped,
  and a URL import is not a dependency. Symbols are class and id selectors
  (`.btn`, `#app`, by the first one in a rule), custom properties, `@keyframes`,
  Sass `$variables`, mixins, functions and `%placeholders`, and Less
  `@variables` and mixins. Stylesheets appear in the graph when something links
  them. Compiled CSS a repository commits (a library's `dist/` or theme files)
  is indexed like any stylesheet; leave it out with `.githintsignore`.
- **CMake**: a `CMakeLists.txt` links to the directories it adds with
  `add_subdirectory`.
- **Ruby**: files are named by path without the extension, with `lib/` stripped;
  `require` and `require_relative` link. A Rails app loads `app/` by constant
  name and never requires those files, so they have no edges between them.

### Framework facets

Facets name the *role* a construct plays, independently of the framework:
Django, GORM, Prisma, SQLAlchemy and Eloquent models are all `model`; chi, Flask,
FastAPI, Spring and Laravel routes are all `route`.

| Facet | What it marks |
| --- | --- |
| `route` | An HTTP (or WebSocket) endpoint, with its method as the detail when known |
| `model` | A stored entity: an ORM class or table |
| `schema` | Data validated or shaped but not stored: pydantic models, SQLModel classes without `table=True` |
| `component` | A UI component |
| `controller` | A class grouping routes |
| `service` | An injectable or provider |
| `middleware` | Request middleware |
| `infra` | Infrastructure the project builds or deploys with: Docker base images and build stages, docker-compose services, GitHub Actions and GitLab CI jobs, Kubernetes objects, Terraform resources and modules; the detail says which |
| `style` | What a styling framework is configured with: a Tailwind theme token, custom utility, variant, plugin or component class, a Tailwind or UnoCSS config file, a Bootstrap, Bulma or Foundation variable override, a `--bs-` / `--bulma-` / `--pico-` token, and where a CSS framework is imported; the detail says which |
| `command` | An entry point invoked by name: a Make target or CMake custom target, a console command (Artisan, Symfony, cobra, Django `manage.py`), an Electron IPC handler, a Tauri command |
| `migration`, `job`, `test` | Database migrations, background jobs and scheduled tasks, tests |

Shipped detectors, by language:

| Language | Frameworks | ORMs and schemas |
| --- | --- | --- |
| Python | django (with management commands), django REST framework, flask, fastapi | sqlalchemy, sqlmodel, pydantic, celery |
| JavaScript / TypeScript | react, next, vue, vue-router, nuxt, angular, angular-router, svelte, sveltekit, astro, react-router (routes in code, `routes.ts`, and Remix flat file routes); express, nestjs, fastify, hono, elysia, bun (`Bun.serve` routes), trpc; expo-router (React Native); electron (IPC handlers); styled-components and emotion (`styled.x` components) | typeorm, mikro-orm, sequelize, drizzle, mongoose, zod, knex, prisma, bullmq |
| Go | net/http (Go 1.22 method patterns), chi (nested `Route` prefixes, `Method`, `Handle`, `Mount`, middleware), gin, echo, fiber, gorilla/mux, cobra | gorm, bun |
| Java | spring (routes with the class `@RequestMapping` prefix, controllers, services, repositories, `@Entity` models, `@Scheduled` jobs) | |
| PHP | laravel, symfony, codeigniter (4, and 3 by its `application/` layout) | eloquent, doctrine |
| C# | aspnetcore (controller attributes with the class `[Route]` prefix, minimal APIs with `MapGroup`, controllers, middleware, registered services, background services) | entityframework |
| Rust | axum, actix-web, rocket, tauri (commands), tokio | diesel, sea-orm |
| Kotlin | android (Activities, Fragments, `@Composable` screens, ViewModels, WorkManager, Navigation Compose routes), ktor (nested `route {}` blocks), spring | Room (in android) |
| Swift | swiftui (views, observable state), uikit / AppKit (view controllers, views), vapor (routes with `grouped` prefixes) | swiftdata, Core Data, fluent |
| Dart | flutter (widgets, ChangeNotifier / Cubit / Bloc state), go_router, shelf | drift |
| Dev environment | docker (base images, stages), docker-compose (services), github-actions and gitlab-ci (jobs), kubernetes (objects by kind), terraform (resources, modules), make (targets) | |
| Ruby | rails (`config/routes.rb` with `namespace` / `scope` blocks, models, controllers, Active Job and Sidekiq jobs, migrations, rake tasks), sinatra | Active Record (in rails) |
| C++ | qt (widgets and Qt Quick items, item models, threads), crow, drogon (`ADD_METHOD_TO` and `PATH_ADD` routes, controllers, filters), cpp-httplib, oat++, pistache | |
| CMake | cmake (custom targets as commands, `add_test` tests) | |
| CSS, Sass, Less | tailwind (v4 `@theme` tokens, `@utility`, `@custom-variant`, `@plugin` such as daisyUI, `@layer components` classes, and v3 `tailwind.config.*`), bootstrap, bulma, foundation, pico, unocss (config) | |

They live in `internal/index/lang/packs/<language>/`; see
[Extending the index](extensibility.md). A route's detail is its HTTP method,
always in upper case (`GET`), whatever the framework's own spelling; file-based
routes (Next.js, Nuxt, SvelteKit, Astro) are named by their URL, so
`app/(shop)/products/[id]/page.tsx` is `/products/[id]`.

Detection runs over comment- and string-stripped code, so an example in a
docstring is not reported, and is gated on a file's imports, its path, or a
dependency declared in a `package.json`, `composer.json`, `pyproject.toml`,
`requirements*.txt`, `go.mod`, `Cargo.toml`, `*.csproj`, `Gemfile`, `pubspec.yaml`, `vcpkg.json`,
`conanfile.txt`, `conanfile.py` or `CMakeLists.txt` (`find_package`, `FetchContent_Declare`,
`CPMAddPackage`) at or above it, so a framework is
only claimed where it is actually used. A route split over several lines is
found, and a prefix declared in the same file is joined on: FastAPI's
`APIRouter(prefix="/items")` makes `@router.get("/{id}")` the route
`/items/{id}`, as do Flask's `url_prefix`, Nest's `@Controller("users")`, Symfony's class-level `#[Route]`,
Hono's `basePath`, Elysia's `prefix` and an Express or Hono router mounted in
the same file. Laravel's `Route::prefix(...)->group(...)` and CodeIgniter's
`$routes->group(...)` apply to the routes inside them, and nested groups join.
A prefix added in another file (`include_router(r, prefix=...)`,
`app.use("/api", router)` with the router imported, Fastify's `register`) is
not followed, and prefixes do not chain. After a manifest changes, run `githints index`: the hook only
re-detects files the commit touched, and says so. Facets appear in each note
under `## Framework`.

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
text only.

- **Layers** (the default): importers sit above what they import, each node one
  row below the deepest thing that imports it, so entry points are at the top
  and shared foundations at the bottom. Import cycles share a row. Every edge
  points the way the import goes. **Force** is the alternative, a physical
  layout grouped by folder.
- **Hover** a node to preview its neighbourhood (the rest dims); **click** to
  keep it. What it imports is drawn in ink, what imports it in the accent
  color, matching the two lists in the side panel.
- **Tests** are hidden by default: test files, imports made only by tests (in Go
  a package's `_test.go` files share its node), and helpers only tests import.
  The toggle brings them back, and the header says how many are hidden.
- **Group folders** collapses each folder (below the prefix every file shares,
  so `internal/` is split into `internal/cli/`, `internal/index/`...) into one
  square node; click one and choose Open, or double-click it.
- **Color by** folder, language or framework role. It starts on language only
  when no language covers 80% of the code, and uses three colors that stay
  distinct side by side, including for color-vision deficiency, with the rest in
  a neutral "other"; the legend dims any group.
- Node size is how many places import it. Search with `/`, switch to a sortable
  table view, link to a node with `#<name>`, light and dark themes.

Nodes are files where a file is a module (TypeScript, Python) and packages where
many files share an import path (Go). Graphs past 1500 nodes keep the most
connected ones; narrow with `-focus`, or raise `-max-nodes`.

Other formats go to stdout, or to a file with `-o`:

```sh
githints index graph -format=mermaid -focus=main.go -depth=1   # GitHub issues and READMEs
githints index graph -format=dot | dot -Tsvg > graph.svg       # Graphviz
githints index graph -format=json                              # nodes and edges, for scripts
```

In the JSON, a node or edge with `"test": true` is test code (all of a node's
files are tests; only test files make an edge's import).

Agents get the same graph from `get_dependency_graph`.

## Configuration

Per-repository settings live in `.githints/config.json`. Every key is optional:

```json
{
  "index": {
    "enabled": true,
    "languages": ["auto"],
    "max_bytes": 209715200,
    "max_file_size": 1048576,
    "parse_timeout_ms": 5000,
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
| `index.languages` | Languages to index, or `["auto"]` (the default) for every supported one; see [Languages](#languages). |
| `index.max_bytes` | Size cap for `index.db` (at most 1 GiB). A full rebuild refuses past it; the hook stops at the file that would cross it and says so. |
| `index.max_file_size` | Files larger than this are skipped (at most 64 MiB). |
| `index.parse_timeout_ms` | Per-file parse budget (at most 60 s). |
| `index.graph_html` | Keep `.githints/graph.html` current after `githints index` and every commit. |
| `ollama.*` | Optional local summarization; see below. |

Environment variables override the file: `GITHINTS_INDEX_ENABLED`,
`GITHINTS_INDEX_LANGUAGES` (a comma-separated list that replaces the configured
set), `GITHINTS_INDEX_MAX_BYTES`, `GITHINTS_INDEX_MAX_FILE_SIZE`,
`GITHINTS_INDEX_PARSE_TIMEOUT_MS`, `GITHINTS_INDEX_GRAPH_HTML`, and `GITHINTS_OLLAMA_ENABLED`, `_ENDPOINT`,
`_MODEL`, `_TIMEOUT_MS`, `_MAX_DIFF_BYTES`.

Other variables: `GITHINTS_ROOT` (pin the served repository),
`GITHINTS_PRECOMMIT_BLOCK=1` (make the gate block), `GITHINTS_SALT_DIR` (where
salts live), `GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK=1` (see below).

`config.json` is repository content, so it can arrive in a clone. Its limits are
bounded for that reason, and githints says so on stderr when a repository's own
config turns Ollama on.

### Optional local Ollama summarization

Off by default. When enabled, `get_diff` and `get_recent_changes` accept
`summarize=true`, which asks a local Ollama model for a one-line caption of the
diff (scrubbed of secrets and truncated to `max_diff_bytes` first). If Ollama is
unreachable, slow or returns something unusable, githints returns the plain
result immediately.

The post-commit hook never calls Ollama: a commit does not wait on a model, and
no diff leaves the process on the commit path. Hook-written fallback rows carry
the generic text. Rows recorded as `llm` by earlier versions still verify.

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
.githints/notes/
.githints/notes.html
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
`githints index` is needed. An index from before 0.2.4 has no stored import
keys; the first hook run computes them, so it takes as long as a hook did
before, and the runs after it are faster. MCP clients pick up the new binary when they next
start the server.

**Upgrade every copy of githints that touches a repository, together.** Old
change logs, hints and anchors verify unchanged with a new binary, and nothing
needs migrating. The reverse is not true once outcomes exist: a version that
predates them does not know the new fields, so it reports outcome rows as
`hmac does not recompute`, lists them as ordinary changes, and, if its
post-commit hook runs, claims them for a commit and writes an anchor that the new
version then reports as a problem for good. That happens when a long-running MCP
server, an editor extension, or a hook that names an old binary path is left
behind. Restart MCP servers after upgrading, and run `githints init` so each hook
names the new binary; `githints doctor` shows which binary a hook runs. To go back
to an older version on a store that has outcomes, export first
(`githints export -o DIR`).

## CLI reference

Every command takes `-h`. Exit status: 0 ok, 1 error, 2 usage error, 3 `verify`
found problems.

| Command | Does |
| --- | --- |
| `setup [-clients=a,b\|all] [-dry-run] [-list] [-update] [-share] [-chain]` | `init` if needed, then register the MCP server with clients |
| `init [-chain\|-force] [-share]` | Set up `.githints/`, the hooks, the salt and the managed blocks |
| `remove [-dry-run]` | Stop tracking: remove the git hooks, keep `.githints/` and configs |
| `uninstall [-dry-run] [-force]` | Full removal: hooks, project MCP entries, managed blocks, anchors, `.githints/` and salt |
| `mcp-config CLIENT [-write] [-update]` | Print, or add, one client's MCP entry |
| `doctor` | Check everything githints depends on; prints a fix per problem; changes nothing |
| `status` | Store health, hooks, salt, pending records |
| `serve [-root=PATH]` | Run the MCP stdio server |
| `record -file=F -summary=S [-reason=R] [-agent-id=A]` | Record a change by hand |
| `export -o DIR [-duckdb] [-no-notes]` | Write the log, hints and notes as JSONL, CSV and markdown, with a manifest; see [Exporting](#exporting) |
| `note list\|search\|get\|record\|delete\|render` | Markdown notes under `.githints/notes/`; `githints note help` |
| `outcome -id=N -state=S [-note=T] [-agent-id=A]` | Record how change #N turned out (worked, partial, failed, reverted, unknown) |
| `history -file=F [-limit=N]` | A file's recorded history |
| `brief [-budget=N]` | Session-start digest: failed attempts, uncommitted work, recent commits, blast radius, co-changing files |
| `recent [-limit=N]` | Latest changes across the repository |
| `search -query=Q [-limit=N]` | Full-text search of summaries and reasons |
| `diff -file=F [-hash=H]` | Redacted diff of the working tree, or of one commit |
| `changes -since=T -until=T [-file=F] [-limit=N]` | Changes in a time range (RFC 3339 or Unix seconds) |
| `verify` | Check the HMAC chain, the rendered markdown and the Merkle anchors |
| `render` | Regenerate the change-log markdown from the store |
| `rotate-salt [-force]` | New salt, every row re-signed |
| `salt path\|export [-o FILE]\|import [-force] FILE` | Locate, back up or restore the salt |
| `index [-force]` | Rebuild the structural index |
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

**The index has none of a language's files** ("indexed 0 files", "unsupported
files: N"). That language is not in `index.languages`; `githints index` names it
and `githints index languages` marks what is enabled. Use `["auto"]`, or add the
name, then run `githints index`. After the enabled set grows, `doctor` and the
hook ask for one full `githints index`: the hook only rescans files a commit
touched.

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

There are two ways to stop using githints:

- `githints remove` stops git from tracking new commits (hooks off) but keeps
  the store, salt, agent files, MCP configs and anchors so `githints init`
  starts tracking again instantly.
- `githints uninstall` removes everything githints added to the project. It
  requires `-force` because it deletes the change log and salt.

Both commands are dry-run by default with `-dry-run` and only touch hooks that
were installed by githints; a hook preserved with `init -chain` is restored.
Global MCP configs (Claude Desktop, Windsurf, Cline) are not touched.

```sh
githints remove                   # stop tracking; keep data
githints uninstall -dry-run       # preview full removal
githints uninstall -force         # remove hooks, MCP entries, managed blocks,
                                  # anchors, .githints/ and the salt
```

If you prefer to do it by hand, from the repository root:

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
