# githints: structural index + session context + agent-first hooks — secure implementation plan

**Purpose.** githints today is a *causal* layer: what changed, and why, tamper-evidently
logged. This plan adds a *structural* layer: what the codebase currently looks like, so
an agent can query "what's in this file / who imports it" instead of reading the whole
repo — plus a session-aware nudge that steers the agent to check githints before
raw-reading files, and an optional human-facing Obsidian export.

It deliberately does **not** add: multi-modal ingestion, multiple LLM backends, graph
databases, HTML/JS visualization, clustering, PR triage, or TypeScript plugin files.
Everything stays in Go, inside the existing module, with **zero new dependencies and no
CGO** (see locked decision 1).

---

## Locked decisions

These decisions were resolved in a design interview and **verified against the repository
on 2026-07-28**. They override any conflicting wording in the phase sections and trigger
prompts below. Do not re-litigate them. If a claim in the Evidence column no longer
matches the code, **stop and report the discrepancy** instead of silently adapting to it.

| # | Decision | Outcome | Evidence (verified 2026-07-28) |
|---|---|---|---|
| 1 | Parser strategy | **Option B — pure Go, no CGO.** Go files are parsed with the stdlib `go/parser`. No tree-sitter, no subprocess parser, no new dependency. | `.goreleaser.yml:12` sets `CGO_ENABLED=0`; `go.mod` has zero CGO dependencies; `.github/workflows/ci.yml` runs `go test -race` on ubuntu/macOS/windows |
| 2 | Language set | **Go only** for v1. TypeScript/Python arrive later as one new file under `internal/index/lang/` implementing `LanguageParser`. | the repo is pure Go, so it is its own best fixture via self-tracking |
| 3 | Where symbols are rendered | **Separate index notes: `.githints/index/<path>.md` plus a root `.githints/INDEX.md`.** Symbols are *never* written into per-file hint markdown or `CHANGES.md`. `internal/hint` stays untouched and `VerifyRendered` keeps byte-exact semantics. | `hint.RenderFile` full-overwrites via `os.WriteFile` (`internal/hint/render.go:62`) and runs on every record from `recorder.record` (`internal/recorder/recorder.go:208`) and `recorder.BatchRecord` (`:117`) — an in-file section would be destroyed by every `record_change` and could only survive by breaching the `recorder.go` boundary. `VerifyRendered` (`render.go:170`) hashes store-rendered content against raw disk bytes and iterates only store-derived paths (`st.FilesWithHistory()`), so extra index notes cannot trip verification |
| 4 | Execution order | **0a → 0b → 4 → 1 → 2 → 3 → 5 → 6**, one branch + PR per step, all based on `main`. | Phase 4 has no dependencies; 1→2→3 is a strict chain; `main` is the default branch and the CI-gated target |
| 5 | Session tracking scope | Index tool handlers get `defer MarkToolCalled(...)` **from their first commit**, and `get_session_context` reports index availability + `last_indexed_at` once `index.db` exists. | `store.Count()` exists at `internal/store/store.go:544`; the 7 existing handlers are confirmed below |
| 6 | Config | `index.enabled` (default `true`), `languages: ["go"]`, `max_bytes` 200MB, `max_file_size` 1MB, `parse_timeout_ms` 5000; `GITHINTS_INDEX_*` overrides for scalars only; `validateIndex` gated on `enabled`. Ollama stays optional and untouched. | mirrors the existing `ollama` section pattern in `internal/config/config.go` |
| 7 | Optional scope | `.githintsignore` is **kept**. Phase 6 (Obsidian graph) is **kept** as the final phase. | explicit product requirement: a graphify-style visual that stays secure and lightweight |

### Corrections found during verification

1. **`CHANGES.md` must not receive an index block.** It is integrity-verified against
   `renderChangelogContent()` (`internal/hint/render.go:175`), so appending to it would
   make `githints verify` fail permanently. The index rollup lives in `.githints/INDEX.md`.
2. **Shared mode must ignore `index.db`.** The `sharedBlock` in `main.go` ignores only
   `store.db*`, `.salt`, and `config.json`, so `index.db` would be committed in repos
   initialized with `githints init -share`. Phase 1 adds `.githints/index.db*` to that
   block; pre-existing shared repos need `githints init -share -force` or a manual line.
3. **The `get_diff` handler is named `handleGetDiff`**, not `handleDiff` as Phase 4's
   handler list claims.
4. **CI enforces `gofmt -l .`**, so the exit gate for every step is: `gofmt -l .` (must be
   empty) → `go build ./...` → `go vet ./...` → `go test -race ./...`.
5. **`githints render` was never wired** into the `main.go` subcommand switch even though
   `usage()` advertises it. Fixed in step 0b, independently of the plan phases.
6. **"Files that NEVER change" now includes all of `internal/hint/`** — locked decision 3
   removed the `## Symbols` carve-out.

## How to use this document

1. This file lives at `docs/plans/structural-index-plan.md` and is the single source of
   truth for the work. Read the Locked decisions table before anything else.
2. Do **one phase per Opencode session/branch**, branched from `main`. Don't let a session
   bleed into the next phase's scope, even if it seems easy while you're in there.
3. For each phase, send Opencode the short **trigger prompt** at the end of that phase's
   section — not the whole plan pasted inline. The trigger prompt tells it to read this
   file plus the existing repo docs first, so it matches your conventions instead of
   inventing new ones.
4. Before merging each phase, run the full exit gate: `gofmt -l .` (output must be empty —
   CI fails otherwise), `go build ./...`, `go vet ./...`, `go test -race ./...`, then read
   the diff for scope creep against that phase's "Out of scope" list.
5. Update `docs/architecture.md` and `AGENTS.md` as part of the phase that touches them
   — don't let docs drift, that's the whole point of this tool existing.

## Security principles across all phases

- **The audit trail (`store.db`) is never modified by index code.** HMAC chains,
  `recorded_at` monotonicity, salt files, and Merkle roots are the responsibility of
  `internal/store` and `internal/integrity`. No phase in this plan touches them.
- **The index is a derived cache, not an audit log.** `index.db` has no HMAC chain.
  Instead, every index MCP tool response includes `last_indexed_at` so the agent can
  assess staleness and decide whether to re-index or read the file directly.
- **Index code never writes to an integrity-verified artifact.** The per-file hint
  markdown and `CHANGES.md` are hashed against store-rendered content by
  `hint.VerifyRendered`. All index output goes to separate notes under
  `.githints/index/` plus `.githints/INDEX.md`, so `githints verify` keeps byte-exact
  semantics and needs no carve-out, strip logic, or exclusion rules.
- **No TypeScript, no Node.js, no plugin files.** All agent nudge logic lives in Go
  inside the MCP server. The `.opencode/` directory is never created or modified by
  githints.
- **Every parser boundary has a guard.** Files are checked for size, type, and
  symlink status before any parser touches them. Parsers have per-file timeouts.
- **All file walking respects `.gitignore` using `git check-ignore`.** A
  `.githintsignore` file may add exclusions but cannot re-include something git has
  already excluded.

---

## Phase 0 — Groundwork (no code) — ✅ COMPLETE

**Status.** Completed and verified on 2026-07-28. Findings are recorded here; the
decisions they drove are in the Locked decisions table above.

| Question | Answer |
|---|---|
| SQLite driver | **`modernc.org/sqlite` — pure Go, no CGO.** `internal/store/store.go:15` blank-imports it; `store.Open` calls `sql.Open("sqlite", path)` at `:136`. `index.db` must use the same driver. |
| MCP tool registration | Handler *factories* return `server.ToolHandlerFunc`; tools are registered inline in `Run()` as `s.AddTool(mcp.NewTool("name", mcp.WithDescription(...), mcp.WithString("arg", mcp.Required(), mcp.Description(...))), handleX(deps))`. Errors are returned as `mcp.NewToolResultError(err.Error()), nil` (28 occurrences, no handler returns a non-nil Go error); success is `mcp.NewToolResultText(...)`. Args come from `requireString` / `optionalString` / `optionalInt` / `optionalBool` helpers, limits via `clampLimit`. |
| Changed files for the hook | `gitutil.ChangedFiles(hash string) ([]string, error)` at `internal/gitutil/gitutil.go:126`, called from `cmdHookRun` at `main.go:380` after `gitutil.LastCommitHash()`. Phase 2 reuses this and must not reimplement it. |
| Existing CGO dependencies | **None.** `go.mod` is CGO-free and `.goreleaser.yml:12` pins `CGO_ENABLED=0` while cross-building linux/darwin/windows × amd64/arm64. |
| Existing MCP tools (7) | `record_change` (`handleRecordChange`), `record_batch` (`handleRecordBatch`), `get_file_history` (`handleFileHistory`), `get_recent_changes` (`handleRecentChanges`), `search_changes` (`handleSearch`), `get_diff` (**`handleGetDiff`**), `get_changes_in_range` (`handleChangesInRange`). |
| CGO recommendation | **Option B.** The project already paid for CGO-freedom by choosing `modernc.org/sqlite`; `CGO_ENABLED=0` is baked into releases and `-race` runs on three operating systems. No README "Prerequisites" section is needed — staying CGO-free is a feature, not a caveat. |

<details>
<summary>Original Phase 0 questionnaire (kept for provenance)</summary>

- Which SQLite driver does `internal/store` use — CGO (`mattn/go-sqlite3`) or pure Go
  (`modernc.org/sqlite`)? Phase 1 must reuse the same one.
- How are MCP tools currently registered in `internal/mcpserver` (the `mark3labs/mcp-go`
  patterns used for `record_change`, `get_file_history`, etc.)? New tools should match
  that shape exactly.
- Where does `hook-run` currently resolve "files changed by this commit," and which
  package owns that (`internal/gitutil`)? Phase 2 reuses this, doesn't reimplement it.
- Confirm the CGO tradeoff explicitly: real tree-sitter grammars require CGO (via
  `smacker/go-tree-sitter` — it bundles per-language grammars as sub-packages, no
  external `tree-sitter` CLI or grammar downloads needed). CGO means `go build -o githints .`
  from the README stops being trivially cross-compilable. Decide one of:

  - **Option A: Accept CGO.** Document `CGO_ENABLED=1` + a C toolchain as a
    prerequisite in README. Set up a GitHub Actions CI matrix for cross-compilation.
  - **Option B: Stay pure Go.** Use a pure-Go parser like the standard `go/parser`
    for Go files and a regex-based fallback for other languages. This limits accuracy
    but keeps the build story simple.
  - **Option C: Subprocess isolation.** Ship tree-sitter as a separate binary that
    githints calls via `exec.Command`. Contains parser crashes and avoids CGO in the
    main binary, but adds distribution complexity.

  Document the decision in `README.md` under a new "Prerequisites" section. Do not
  let it surprise you at Phase 1's PR review.
- Confirm whether `go.mod` currently has any CGO dependencies already, so Phase 1
  knows if adding `smacker/go-tree-sitter` changes the build story or not.

**Trigger prompt:**
> Read `docs/plans/structural-index-plan.md`, `docs/architecture.md`, `AGENTS.md`, and
> the `internal/store`, `internal/mcpserver`, and `internal/gitutil` packages. Don't
> write any code yet. Report: (1) which SQLite driver is in use and whether it's CGO or
> pure Go, (2) the exact pattern used to register an MCP tool in `internal/mcpserver`,
> with a code excerpt, (3) which function resolves the list of files changed in a commit
> for the post-commit hook, (4) confirm whether `go.mod` currently has any CGO
> dependencies already, and (5) a recommendation on Option A, B, or C for the CGO
> decision based on the project's current build story and README instructions.

</details>

---

## Phase 1 — Structural index core (full scan)

**Goal.** Local, deterministic AST parsing of the repo into symbols + imports. No LLM
involved. Full scan on demand; incremental comes in Phase 2.

### Design

**New package `internal/index/`:**

- `types.go` — `Symbol{Name, Kind, FilePath, LineStart, LineEnd, Signature}`,
  `Import{FilePath, ImportedPath}`. Also `IndexMeta{LastIndexedAt int64, FileCount int,
  SymbolCount int, LanguageCounts map[string]int}`.
- `lang/` — one file per language. **v1 ships `golang.go` only** (locked decision 2),
  built on the stdlib `go/parser`. Extra languages stay additive: one new file
  implementing `LanguageParser`, registered in the parser registry.
- `parser.go` — a `LanguageParser` interface:

  ```go
  type LanguageParser interface {
      Extensions() []string
      Parse(path string, src []byte) ([]Symbol, []Import, error)
  }
  ```

  This makes adding a language additive, not a rewrite.
- `scan.go` — `FullScan(root string) error`:
  - Walks the tree using `filepath.Walk`.
  - **Security:** rejects symlinks, devices, FIFOs, sockets at the walk level (never
    reaches the parser). Uses `git check-ignore` for `.gitignore` respect. Supports an
    optional `.githintsignore` file layered on top (checked via `git check-ignore
    --no-index` semantics, only ever excludes more, never re-includes).
  - **Security:** enforces `MaxFileSize` (configurable, default 1MB). Files larger than
    this are skipped with a warning.
  - **Security:** enforces a per-file parse timeout (configurable, default 5 seconds).
    If a parser hangs, the file is skipped.
  - Calls `parser.Parse()` for each supported file. Skips unsupported extensions with a
    warning. Never crashes on malformed input.
  - Idempotent: running twice produces identical output.
- `render.go` — writes **separate index notes** and never touches hint markdown
  (locked decision 3):
  - `.githints/index/<file_path>.md` — one note per indexed source file: a `# <path>`
    heading, a `## Symbols` section (name, kind, line range, signature), plus
    `## Imports` and `## Imported by` sections.
  - `.githints/INDEX.md` — the root index rollup: file count, symbol count, language
    breakdown, `last_indexed_at`, and the top N files by import in-degree. This replaces
    the originally-planned `CHANGES.md` index block, which would have permanently broken
    `hint.VerifyRendered`.
  - **Collision guard:** an index note path must never equal a hint path (reachable when a
    source path itself begins with `index/`). On collision, skip that note and warn —
    never clobber an integrity-verified artifact.

**New database `.githints/index.db`** (separate from `store.db`):

- Tables: `symbols`, `imports`, `meta`.
- `meta` stores `last_indexed_at`, `file_count`, `symbol_count`.
- No HMAC chain. The index is a derived cache, not an audit log. Integrity comes from
  being trivially regenerable via `githints index`.
- **Security:** the index has no HMAC chain, but every read has provenance metadata.
  All index MCP tool responses (Phase 3) include a `last_indexed_at` timestamp and an
  `index_staleness` indicator so the agent can assess freshness.

**New CLI subcommands:**

- `githints index` — full scan + render
- `githints index status` — file/symbol counts, last scan time, per-language
  breakdown, skipped/unsupported file count, `index.db` size

**Config in `.githints/config.json`:**

```json
{
  "index": {
    "enabled": true,
    "languages": ["go"],
    "max_bytes": 209715200,
    "max_file_size": 1048576,
    "parse_timeout_ms": 5000
  }
}
```

Follow the existing `ollama` section conventions in `internal/config/config.go`: defaults
in `Default()`, `GITHINTS_INDEX_*` env overrides for the scalar keys only (`ENABLED`,
`MAX_BYTES`, `MAX_FILE_SIZE`, `PARSE_TIMEOUT_MS` — not `languages`), and a `validateIndex`
that runs only when `enabled` is true and fails closed on non-positive limits, an empty
language list, or a language with no registered parser.

Phase 1 must also add `.githints/index.db*` to the **shared-mode** gitignore block in
`main.go`, so `githints init -share` never commits the derived cache.

### Acceptance criteria

- `githints index` on a clean repo produces symbol/import rows for every supported file
  and writes one `.githints/index/<path>.md` note per file plus `.githints/INDEX.md`.
- Running it twice is idempotent (second run produces identical output, no duplicate
  rows).
- An unsupported or malformed file is skipped with a warning, never a crash.
- A symlink, device file, or FIFO in the tree is silently skipped, never parsed.
- A file exceeding `max_file_size` is skipped with a warning.
- **`githints verify` still reports `rendered markdown: OK` after a full index run** —
  proof that the index never touches integrity-verified artifacts.
- `.githints/index.db*` appears in the shared-mode gitignore block.
- Unit tests per language using small fixture files under `internal/index/testdata/`.

### Out of scope

- Call-graph resolution, cross-file symbol resolution, anything beyond defs + imports.
- Any language beyond Go (locked decision 2).
- Incremental updates (Phase 2).
- MCP tools (Phase 3).
- Any modification to `store.db`, `internal/store`, `internal/integrity`,
  `internal/recorder`, or `internal/hint`.

**Trigger prompt:**
> Implement Phase 1 of `docs/plans/structural-index-plan.md`. Read that file in full
> first — especially the Locked decisions table — plus `docs/architecture.md` and
> `internal/hint`. Create `internal/index/` with `types.go`, `parser.go`, `lang/golang.go`,
> `scan.go`, and `render.go`. Parsing is **pure Go via the stdlib `go/parser`, Go files
> only** (locked decisions 1 and 2): no CGO, no tree-sitter, no new dependency. Create
> `.githints/index.db` as a separate SQLite database (same `modernc.org/sqlite` driver)
> with `symbols`, `imports`, and `meta` tables. Render to **separate index notes**
> (`.githints/index/<path>.md` + `.githints/INDEX.md`) — do NOT touch per-file hint
> markdown, `CHANGES.md`, `internal/hint`, or `internal/recorder` (locked decision 3).
> Add `githints index` and `githints index status` CLI commands, plus the `index` config
> section and `.githints/index.db*` in the shared-mode gitignore block. Implement the
> security guards (no symlinks, no devices, max file size, parse timeout, `.gitignore`
> via `git check-ignore`, `.githintsignore` layered on top) and the index-note collision
> guard. Do NOT implement Phase 2, Phase 3, or anything beyond Phase 1's acceptance
> criteria. Add tests, and assert `githints verify` still passes after indexing. Update
> `docs/architecture.md`'s package layout section.

---

## Phase 2 — Incremental re-index on commit

**Goal.** Reuse the existing post-commit hook's changed-file list instead of doing a
full rescan every commit.

### Design

- In `hook-run`, after the existing `changes`-table logic runs, call
  `index.IncrementalScan(root, changedFiles)` with the list `cmdHookRun` already got from
  `gitutil.ChangedFiles(hash)` (`main.go:380`) — don't recompute it a second way.
- **Gated on `cfg.Index.Enabled`.** When indexing is disabled the hook skips the step and
  logs a single verbose-only line (never silently, so a stale index is always explainable);
  `githints index status` states "indexing disabled in config" outright.
- `IncrementalScan` works as follows:
  - Deletes existing symbol/import rows for each changed file path.
  - Re-parses just that file using the same parser from Phase 1.
  - Re-inserts the new rows.
  - Re-renders only that file's `.githints/index/<path>.md` note.
  - Refreshes `.githints/INDEX.md` hub ranking (a sort over the import table, cheap).
- Handle deletions: a file removed in the commit drops its rows and deletes its
  `.githints/index/<path>.md` note.
- Handle new files: a file added in the commit is parsed for the first time.
- Update `meta.last_indexed_at` to the current time after the batch completes.
- **Security:** the incremental scan uses the same size/timeout/symlink guards as
  Phase 1. Even though the file list comes from git (trusted), each file is still
  checked before parsing.

### Acceptance criteria

- A commit touching 2 of 10 tracked files re-parses only those 2. Verify by checking
  `meta.last_indexed_at` per file or by asserting untouched files' rows are
  byte-identical before/after.
- A commit deleting a file removes its symbols/imports rows and its index note.
- A commit adding a new file parses and indexes it.
- Full `githints index` still works standalone for the initial scan or a manual
  rebuild.
- No changes to `store.db`, `internal/store`, or `internal/integrity`.

### Out of scope

- Watch-mode / live re-indexing outside of git commits (file watchers, debouncing).
- Any modification to the HMAC chain or integrity code.

**Trigger prompt:**
> Implement Phase 2 of `docs/plans/structural-index-plan.md`. Read that file, plus the
> current `hook-run` implementation in `main.go` and whatever Phase 0 found about where
> changed files are resolved. Wire `index.IncrementalScan` into the existing
> post-commit hook using that same changed-file list — do not write a second
> implementation of "which files changed in this commit." Handle file addition,
> modification, and deletion. Use the same parser and security guards from Phase 1
> (symlink rejection, size cap, timeout). Add a test that commits a change touching a
> subset of files and asserts only that subset was re-indexed. Do NOT touch `store.db`,
> `internal/store`, or `internal/integrity` at all.

---

## Phase 3 — MCP tools for the index

**Goal.** Expose the index the same way the existing changelog tools are exposed.

### New tools in `internal/mcpserver`

All tools follow the exact registration pattern identified in Phase 0 (same input
validation, same error handling, same JSON shape conventions).

- **`list_symbols(file string)`** — returns symbols defined in a file.
  - Response includes `file`, `last_indexed_at`, and each symbol's `name`, `kind`,
    `line_start`, `line_end`, `signature`.
- **`find_symbol(name string)`** — exact + prefix match across the repo.
  - Returns `file`, `line`, `kind` for each match.
  - Response includes `last_indexed_at` so the agent can assess freshness.
- **`get_dependents(file string)`** — reverse lookup: which files import this one.
  - The "blast radius" question before editing something.
  - Response includes `file`, `dependents: [{file, import_line}]`, `last_indexed_at`.
- **`get_index_summary(limit int)`** — top-N files by import in-degree, plus totals.
  - Returns `total_files`, `total_symbols`, `last_indexed_at`, `languages`, and the top
    N files by import count (the "hub" files).

**Security:** every response includes `last_indexed_at`. This is the provenance
metadata that lets the agent decide "this index is 2 hours stale, I should re-index or
read the file directly." Without this, the agent has no way to distinguish fresh data
from stale data.

### AGENTS.md update

Add a rule alongside the existing "check history before editing unfamiliar files" one,
matching its tone and format exactly:

> **Rule: use the structural index before editing unfamiliar files**
>
> Before making non-trivial changes to a file you haven't touched this session, call
> `list_symbols(file="...")` to see what's defined in it. If you're considering
> refactoring or deleting a file, call `get_dependents(file="...")` to check what
> depends on it first — this is your "blast radius" check.

### Acceptance criteria

- Each tool follows the exact registration pattern Phase 0 identified.
- `docs/architecture.md`'s MCP server tool list is updated to include the four new
  tools.
- Each response includes `last_indexed_at`.
- Manual or automated MCP call for each tool returns correct results against a small
  fixture repo.
- All four handlers open with `defer MarkToolCalled("<toolname>")` — the Phase 4 session
  tracker is already merged by the time Phase 3 runs (locked decisions 4 and 5).
- `get_session_context` additionally reports index availability and `last_indexed_at`, so
  session-start orientation can flag a stale index.

**Trigger prompt:**
> Implement Phase 3 of `docs/plans/structural-index-plan.md`. Read that file and the
> existing MCP tool implementations in `internal/mcpserver` first, and match their
> exact style — parameter validation, error wrapping, response shape. Add
> `list_symbols`, `find_symbol`, `get_dependents`, and `get_index_summary`. Every
> response must include `last_indexed_at` from the index meta table. Update
> `AGENTS.md` with one new rule about using the structural index before editing
> unfamiliar files, matching the tone of the existing rules. Update
> `docs/architecture.md`'s MCP server section. Do not add any tool beyond these four.
> The Phase 4 session tracker is already merged: add `defer MarkToolCalled("<toolname>")`
> as the first line of each new handler, and extend `GetSessionContextJSON` with index
> availability and `last_indexed_at`.

---

## Phase 4 — Session context + agent-first nudge (Go native)

**Goal.** Give the agent a way to orient itself at session start, and track which
githints tools have been used. This is the "agent goes to githints first" behavior —
implemented entirely in Go, inside the existing MCP server. No TypeScript, no plugin
files, no Node.js dependency.

### Why Go native instead of a TypeScript plugin

The original plan suggested a TypeScript plugin for Opencode. This was reconsidered for
several reasons:

- **Attack surface:** a TypeScript plugin file (`.opencode/plugin/plugin.ts`) is
  writable by anyone who can write to the repo. An attacker who modifies it can
  intercept, modify, or block any MCP tool call — including `record_change` and
  `record_batch`. This lives outside githints' Go-based security model (HMAC chains,
  salt files, integrity verification).
- **Dependency drift:** introduces a Node.js/Deno runtime dependency into a pure Go
  project. Two languages to maintain, two build systems.
- **Subagent blind spot:** Opencode plugin hooks don't reliably fire for subagent tool
  calls, making the nudge inconsistent.
- **MCP already has a channel:** the MCP server communicates directly with the agent.
  There's no need to intercept file reads when you can make the MCP tools visible and
  useful enough that the agent chooses to call them first.

The Go native approach adds ~120 lines of Go inside `internal/mcpserver/`, one new MCP
tool, and no new dependencies. The agent calls `get_session_context()` at session start
and gets tailored suggestions. Every existing tool automatically marks itself as
"called" via a one-line `defer`. No file system writes, no second language, no attack
surface beyond what the MCP server already has.

### Design

New file `internal/mcpserver/session.go`:

- `SessionTracker` struct with:
  - `sessionStartTime time.Time`
  - `toolsCalled map[string]bool` — tool name → whether called this session
  - `indexAvailable bool` — whether the store has any data
  - `sync.Mutex` for concurrent access
- `InitSession(st *store.Store)`:
  - Sets `sessionStartTime` to `time.Now()`
  - Initializes `toolsCalled` map
  - Calls `st.Count()` to check if the store has data
- `MarkToolCalled(name string)`:
  - Sets `toolsCalled[name] = true`
- `GetSessionContextJSON() string`:
  - Returns a human-readable string with:
    - Session start time
    - Whether githints tools have been used
    - Index availability
    - Dynamically generated suggestions: only suggest tools that have NOT been called
      yet. If all tools have been used, suggest "Continue with `record_change` after
      edits."

Modification to `internal/mcpserver/server.go`:

- At the top of `Run()`, after the store is received, call `InitSession(st)`.
- In EVERY existing tool handler, add `defer MarkToolCalled("<toolname>")` as the first
  line. Exact tool names:
  - `handleRecordChange` → `"record_change"`
  - `handleRecordBatch` → `"record_batch"`
  - `handleFileHistory` → `"get_file_history"`
  - `handleRecentChanges` → `"get_recent_changes"`
  - `handleSearch` → `"search_changes"`
  - `handleGetDiff` → `"get_diff"` (note: `handleGetDiff`, not `handleDiff`)
  - `handleChangesInRange` → `"get_changes_in_range"`

New MCP tool `get_session_context`:

```go
s.AddTool(
    mcp.NewTool("get_session_context",
        mcp.WithDescription("Returns session tracking state: whether githints tools "+
            "have been used this session, what's been called, and suggested first steps. "+
            "Call this at the START of any session to orient yourself before reading files."),
    ),
    handleGetSessionContext,
)
```

The handler calls `GetSessionContextJSON()` and returns the result as text.

AGENTS.md update — add a new section at the top of the existing rules:

> **Rule: start every session with `get_session_context`**
>
> Before reading any file or making any edit, call `get_session_context()`. It returns:
> - Whether this is a new session or a continuation
> - Which githints tools have already been used this session
> - Suggested next steps tailored to what you haven't done yet
>
> This replaces the need to guess what context is available. If it suggests calling
> `get_recent_changes` or `get_file_history`, do those before reading files directly —
> the history will save you from re-litigating past decisions.

### Security notes

- No TypeScript, no Node.js, no plugin files. Everything is Go.
- Session is per-process. If the MCP server restarts, state resets. This is correct
  behavior. Documented in a comment at the top of `session.go`.
- The MCP server is single-session. If two agents share one stdio, they share session
  state. This is an inherent MCP limitation. Documented in the package comment.
- No new imports beyond stdlib (`sync`, `time`, `fmt`) and `githints/internal/store`
  (already a dependency).
- No changes to `store.go`, `recorder.go`, `hint.go`, `integrity/`, or `gitutil/`.
- This phase does not depend on Phases 1–3. It can be implemented independently and
  works with or without the structural index. When Phases 1–3 add index MCP tools,
  their handlers will also get `defer MarkToolCalled(...)` lines, and the session
  tracker will automatically incorporate them.

### Acceptance criteria

- `get_session_context` returns a useful response when called immediately after server
  start (no tools called yet, session just started).
- After calling any existing tool (e.g., `get_recent_changes`), `get_session_context`
  reflects that tool was called and adjusts suggestions accordingly.
- The response includes session start time, tool usage status, index availability, and
  dynamic suggestions.
- `go build ./...`, `go vet ./...`, `go test -race ./...` all pass.

**Trigger prompt:**
> Read `docs/plans/structural-index-plan.md` Phase 4 section, plus the current
> `internal/mcpserver/server.go`, `AGENTS.md`, and `docs/architecture.md`. Implement
> the session tracker entirely in Go: create `internal/mcpserver/session.go` with
> `SessionTracker`, `InitSession`, `MarkToolCalled`, and `GetSessionContextJSON`. In
> `server.go`, call `InitSession` at the start of `Run()`, add
> `defer MarkToolCalled(...)` to every existing handler, and register the new
> `get_session_context` MCP tool. Update `AGENTS.md` with the session start rule at
> the top. Update `docs/architecture.md` to add `session.go` and the new tool. Do NOT
> add TypeScript, do NOT add a plugin file, do NOT add any dependency outside the
> existing Go module. Run `go build ./...`, `go vet ./...`, and `go test -race ./...`
> before finishing.

---

## Phase 5 — Guardrails and hardening

**Goal.** The defensive-engineering items that are cheap to build in from the start but
expensive to retrofit later.

### Items

1. **Partial-write guard.** If `githints index` is interrupted (Ctrl+C, crash) or a
   walk fails partway through, the index could be left in an inconsistent state with
   fewer rows than the previous complete scan. On the next `githints index` (without
   `--force`), refuse to overwrite a larger existing index with a smaller one:

   ```go
   if newRowCount < existingRowCount && !force {
       return fmt.Errorf("partial write detected: %d new rows vs %d existing; use --force to overwrite",
           newRowCount, existingRowCount)
   }
   ```

   If `--force` is passed, overwrite regardless.

2. **Configurable size cap for `index.db`.** Add a config key `index.max_bytes`
   (default 200MB). Before inserting new rows, check the estimated size. If inserting
   would exceed the cap, refuse with a message pointing to the config key. Allow
   `--force` to override.

3. **`.githintsignore` merge semantics — verified in tests.** The Phase 1
   implementation uses `git check-ignore` for `.gitignore` respect and a second
   `git check-ignore` pass for `.githintsignore`. Write tests that assert:
   - A file excluded by `.gitignore` is not walked.
   - A file excluded by `.gitignore` cannot be re-included by `.githintsignore`.
   - A file not excluded by `.gitignore` can be excluded by `.githintsignore`.
   - A file excluded by `.githintsignore` cannot be re-included by a nested
     `.githintsignore`.

   Use the `git check-ignore` approach (via `internal/gitutil`), not a
   reimplementation.

4. **Symlink/devices/FIFO rejection — verified in tests.** Write tests that assert
   symlinks, device files, and FIFOs in the walk tree are silently skipped. Use
   `os.MkdirTemp` + `os.Symlink` + `os.Mknode` (or mock `os.FileInfo`).

### Acceptance criteria

- A test that simulates an interrupted scan (fewer rows than the existing index)
  refuses to overwrite without `--force`, and succeeds with it.
- A test asserting `.githintsignore` patterns exclude correctly and can't re-include
  something `.gitignore` already excluded.
- Read the existing `internal/recorder/` validation patterns (`ValidateFilePath`) for
  style consistency before implementing.

**Trigger prompt:**
> Implement Phase 5 of `docs/plans/structural-index-plan.md`: (1) partial-write guard
> in `internal/index/scan.go` that refuses to overwrite a larger index with a smaller
> one unless `--force` is passed, (2) configurable size cap for `index.db` via
> `index.max_bytes` in config, (3) tests confirming `.githintsignore` merge semantics
> match `.gitignore`-then-override behavior using `git check-ignore`, (4) tests
> confirming symlinks, device files, and FIFOs are silently skipped. Read the existing
> `internal/recorder/` validation patterns first for style consistency. Nothing here
> should touch the MCP tools, the hook wiring, the session tracker, or any code outside
> `internal/index/` and `internal/gitutil/`.

---

## Phase 6 — Optional: Obsidian-viewable export

**Goal.** A human-facing visual, using Obsidian's built-in graph view — no plugin, no
LLM, no third-party dependency. Lighter than graphify's `graph.html` because you ship no
JS/CSS at all; you just change markdown link syntax.

### Design

- Opt-in flag: `githints index --obsidian` (or config key
  `"index.obsidian_wikilinks": true`).
- When enabled, the renderer changes how cross-references are emitted **in the index notes
  under `.githints/index/`** (never in per-file hint markdown — locked decision 3).
  Instead of relative markdown links, emit `[[wikilinks]]` between files based on the
  import/dependent data from the index. Because the graph edges live purely in the derived
  layer, the Obsidian view can never affect integrity-verified artifacts.
- **File-level only, not per-symbol.** One node per function would produce thousands of
  notes on a real codebase — the same "too big to render" problem graphify explicitly
  warns about. File-level nodes stay legible and map to what you already track in the
  changes log.
- **Security: escaping.** Obsidian wikilinks use `[[` and `]]`. A file name containing
  these characters could break the link syntax. Use pipe-syntax wikilinks with
  URL-encoded targets:

  ```
  [[file-with-brackets-.md|file-with-[brackets]]]
  ```

  The pipe separates the target from the display text. Square brackets in the display
  text (the display portion) are safe because they don't affect the link boundary. The
  target portion uses URL-encoding for `[`, `]`, and `|`.
- Reuse the existing markdown-escaping logic from `internal/hint` for the display text,
  but do NOT backslash-escape `[` and `]` when rendering wikilinks.
- Output location: follows the same shared/private convention as the rest of
  `.githints/` — gitignored by default, committed only in shared mode
  (`githints init -share`).

### Acceptance criteria

- Opening the `.githints/` folder directly as an Obsidian vault shows a graph view with
  file-level nodes and edges. (Any folder of `.md` files with `[[wikilinks]]` works; no
  special Obsidian format is needed.)
- The `--obsidian` flag is fully optional. Default `githints index` behavior is
  unchanged.
- A file named `file-with-[brackets].go` produces a valid `[[...]]` link that Obsidian
  renders correctly.

### Out of scope

- LLM entity extraction, embeddings, fixed ontologies, separate runtime dependencies.
- Per-symbol nodes.
- Any change to the default (non-Obsidian) rendering behavior.

**Trigger prompt:**
> Implement Phase 6 of `docs/plans/structural-index-plan.md`: an opt-in `--obsidian`
> flag for `githints index` that changes cross-reference rendering in
> `internal/index/render.go` to use `[[wikilinks]]` at file-level granularity only. Use
> pipe-syntax wikilinks with URL-encoded targets for files containing `[`, `]`, or `|`.
> Reuse existing escaping logic for the display text but do NOT escape `[` and `]` in
> wikilink output. Do NOT add any new dependency, LLM call, or per-symbol nodes.
> Default behavior must be unchanged when the flag isn't passed.

---

## Quick reference

| Phase | Adds | Security hardening | New deps |
|---|---|---|---|
| 0 | Nothing (recon + CGO decision) | Documents CGO/prerequisites decision | none |
| 1 | `internal/index/`, `.githints/index.db`, index notes + `INDEX.md`, `githints index` CLI | Symlink/devices/FIFO rejection, max file size, parse timeout, `git check-ignore`, index-note collision guard, integrity-verified files never written | **none** — stdlib `go/parser` (Option B) |
| 2 | Incremental re-index on commit | Reuses Phase 1 guards for every parsed file | none |
| 3 | 4 MCP tools (`list_symbols`, `find_symbol`, `get_dependents`, `get_index_summary`), AGENTS.md rule | Every response includes `last_indexed_at` for staleness assessment | none |
| 4 | `internal/mcpserver/session.go`, `get_session_context` MCP tool, AGENTS.md rule | Pure Go, no TypeScript/plugin attack surface, no new deps | none |
| 5 | Partial-write guard, size cap, ignore-file tests | Prevents silent data loss from interrupted scans | none |
| 6 | `--obsidian` wikilink export | Safe escaping for `[[wikilinks]]` with pipe syntax | none |

## Dependency order

```
Phase 0 (recon, no code)
  │
  ▼
Phase 4 (session tracker) ───── can be done independently at any time
  │
  ├──► Phase 1 (index core) ──► Phase 2 (incremental) ──► Phase 3 (MCP tools)
  │                                                                 │
  │                                                                 ▼
  │                                                   Session tracker auto-integrates
  │                                                   with new index tools via
  │                                                   MarkToolCalled in handlers
  │
  ├──► Phase 5 (guardrails) ── can be done after Phase 1
  │
  └──► Phase 6 (Obsidian) ──── can be done after Phase 1
```

Phase 4 has no dependencies on any other phase. Implement it first for immediate value.
Phases 1→2→3 are a strict dependency chain. Phases 5 and 6 depend on Phase 1 but can be
done in parallel with 2, 3, or 4.

## Files that change (cumulative)

| File | Phase | Change |
|---|---|---|
| `internal/mcpserver/session.go` | 4 | New — session tracker |
| `internal/mcpserver/server.go` | 4 | Wire session init, deferred `MarkToolCalled`, register tool |
| `internal/index/types.go` | 1 | New — `Symbol`, `Import`, `IndexMeta` types |
| `internal/index/parser.go` | 1 | New — `LanguageParser` interface |
| `internal/index/lang/golang.go` | 1 | New — Go parser (stdlib `go/parser`) |
| `internal/index/store.go` | 1 | New — `index.db` schema + queries (`modernc.org/sqlite`) |
| `internal/index/scan.go` | 1, 2, 5 | New — `FullScan` + `IncrementalScan` + guards |
| `internal/index/render.go` | 1, 6 | New — index notes + `INDEX.md` + wikilink export |
| `internal/mcpserver/server.go` | 3 | Add 4 index MCP tools |
| `internal/config/config.go` | 1 | Add `index` section, defaults, env overrides, validation |
| `AGENTS.md` | 3, 4 | Add session start rule + index rules |
| `docs/architecture.md` | 1, 3, 4 | Package layout + tool list updates |
| `main.go` | 0b, 1, 2 | Wire missing `render` case; add `githints index` / `index status`; `.githints/index.db*` in shared gitignore block; hook wiring |

## Files that NEVER change

- `internal/store/store.go` — the audit trail is off-limits
- `internal/integrity/` — HMAC chain, salt, Merkle root code is off-limits
- `internal/recorder/recorder.go` — the single write path for changes is off-limits
- `internal/hint/` — **entirely** off-limits (locked decision 3 moved symbols and
  wikilinks into separate index notes, so the `## Symbols` carve-out no longer exists)
- `.githints/<path>.md` and `.githints/CHANGES.md` — integrity-verified artifacts; index
  code never writes to them
- `internal/gitutil/gitutil.go` — only used via existing function calls, no
  modifications
