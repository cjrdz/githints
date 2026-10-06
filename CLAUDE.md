<!-- >>> githints (managed) -->

@AGENTS.md

<!-- <<< githints (managed) -->

## Working on githints itself

The rules above are for *using* the githints tools; they apply here too, since
the repo tracks its own development. This section is about changing the code.

See `CONTRIBUTING.md` for project layout, code style, and commit conventions
(Conventional Commits), and `docs/architecture.md` for the data model and
integrity design.

### Verification gate

Run all of these before claiming a change works:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
```

`-race` requires cgo, which requires a C toolchain. On Windows the Go
distribution alone is not enough and neither is clang plus the Windows SDK —
`vcruntime.h` ships with the MSVC toolset, and cgo wants a GCC-style driver.
A self-contained mingw-w64 works:

```sh
winget install BrechtSanders.WinLibs.POSIX.UCRT
```

Tests shell out to the real `git`, so it must be on `PATH`. Tests that need a
file symlink skip on Windows without Developer Mode; directory links fall back
to a junction (`mklink /J`), which needs no elevation.

### Invariants that must not regress

Each of these is load-bearing and easy to undo by accident. There is a test for
every one; if you find yourself deleting or weakening one of those tests, stop.

- **Nothing but JSON-RPC on stdout in `serve` mode.** A stray `fmt.Println`
  anywhere reachable from `cmdServe` corrupts the framing for every client.
  Diagnostics go to stderr.
- **`get_diff` scrubs unconditionally.** `llm.ScrubDiff` must run on the
  default path, not only when `summarize=true` — Ollama is off by default, so
  the summarize path is the rare one.
- **`clock_tamper_warning` is inside the HMAC payload.** That is why
  `store.CheckClockTamper` runs in `recorder.prepare` *before* the row is
  signed, rather than inside `Insert`. Move it back and clearing the flag in
  the database becomes invisible to `githints verify`.
- **All githints-managed file I/O goes through `internal/safefs`.**
  `recorder.ValidateFilePath` is a lexical check and cannot see a symlink or a
  Windows junction. Hints are written through an `os.Root` opened on
  `.githints/` itself, not the repository root: anchored at the root, a
  committed relative link `.githints/up -> ..` let `record_change` overwrite
  `CLAUDE.md`. The incremental scan `Lstat`s through an `os.Root` (plain
  `os.Stat` followed links and indexed files outside the repo).
- **`verify` checks the Merkle anchors.** The HMAC chain cannot see deleted
  tail rows or a log re-signed with the salt; the notes in `refs/notes/githints`
  are the check, and nothing read them until v2 anchors. The per-commit root
  covers `commit_hash`; the whole-log root must not (pending rows are stamped
  later). Neither covers `hmac`/`prev_hmac`, so a salt rotation keeps anchors
  valid.
- **One escaper and one sanitizer: `internal/textsafe`.** Recorded text, file
  names and symbol names reach markdown, terminals, MCP output and the graph
  viewer only through it. Record-time sanitizing runs *before* the secret scan;
  a zero-width space used to hide a key from it.
- **State from a clone is refused.** A git-tracked `store.db`, `index.db` or
  legacy `.salt` is rejected and a tracked `repo-id` ignored; both databases
  open with `trusted_schema=OFF` through the DSN (an `Exec` reaches only one
  pooled connection).
- **git never runs a repository's drivers.** Diff and show calls pass
  `--no-ext-diff --no-textconv`. The hook script keeps the binary path in a
  single-quoted variable; Go's `%q` is not shell quoting.
- **The pre-commit hook fails a commit only for `errPrecommitBlocked`.** Any
  internal error is printed and the commit proceeds.
- **Only `init` creates `.githints/`.** Every other command goes through
  `openInitialized` and refuses in an uninitialized repository.
- **Every `hash` argument is checked with `IsValidCommitish`** before reaching
  argv. In `ChangedFiles` and `DiffStat` the value lands *before* the `--`
  separator, so an unchecked one is parsed as a git option.
- **Caps stay enforced in Go**, not just declared in the tool schema:
  `recorder.MaxSummaryLen`, `MaxReasonLen`, `MaxAgentIDLen`, `MaxBatchSize`,
  `clampLimit`, `maxSearchQueryLen`, `maxSymbolNameLen`, `maxDiffResultBytes`,
  `maxGraphNodesMCP`, `gitutil.MaxOutputBytes`, the `config.json` index ceilings.
- **`BatchRecord` is one transaction.** A partial batch leaves the caller
  unable to tell what landed.
- **The `commands` table in `main.go` is the single source of dispatch.**
  `TestUsageMatchesCommandTable` keeps it in sync with `usageText`; that test
  exists because `githints render` was advertised while being unreachable.
- **Blanking preserves one output line per input line.** Everything downstream
  maps a matched line back to the source by index. Four separate bugs let an
  escape swallow a newline and shift every symbol below it;
  `FuzzBlankerLineCount` exists to catch the fifth.
- **An unknown language is skipped on the hook path, not fatal.**
  `config.json` travels in clones, so a repo naming a language an older binary
  lacks used to abort the incremental scan — and the hook only warns, so the
  commit succeeded while the index silently stopped updating forever.
  `FullScan` and `VerifyIndex` stay strict; both call sites say why.
- **A `keep_strings` detector rule must declare a `requires` gate, and the
  gate is evaluated against the _code_ view.** Otherwise a rule matches its own
  shape inside any string literal: a Go raw string containing `r.Get("/x", h)`
  was reported as a route.
- **Framework detection is gated on imports or path.** `class X(Model)` is the
  shape of every Python ORM; attributing one to the wrong framework is worse
  than not detecting it. A detector declaring no gate is rejected at load.
- **Batched and per-path ignore checks must agree.** `resolveIgnored` replaced
  one `git check-ignore` per path (1.3ms each, three quarters of a full scan)
  with one pair of invocations. The two passes stay separate — that ordering is
  what keeps `.githintsignore` subtract-only — and a differential test pins the
  two implementations against each other.
- **`setup` touches only githints' own entry.** Other servers are kept, an
  existing githints entry changes only with `-update`, JSONC is never rewritten,
  Codex's TOML is only appended to (and carries no `type`: Codex rejects unknown
  fields), and global configs are written only when the client is named, under
  a per-repository name, after a one-time backup.
- **The graph viewer cannot run file names or reach the network.** Its CSP is
  `default-src 'none'` with the inline script and style pinned by hash, graph
  data is a non-executed JSON block, and the viewer uses `textContent` only.
  `FuzzExportersEscape` covers the HTML and Mermaid outputs.
- **Bump `index.ResolverVersion` when stored import keys change meaning.** The
  hook rescans only changed files, so old and new keys would mix silently.
- **Scan results are merged in walk order.** Parsing runs across a worker pool;
  each worker writes to its own slot and nothing is appended concurrently. The
  read paths sort in SQL, so notes were never at risk, but insertion order is,
  and a test asserts rows were written in walk order.

### Conventions specific to this codebase

- Tests use the standard library `testing` only — no testify, no fixtures,
  despite testify appearing in `go.sum` as a transitive dependency.
- Return errors; don't panic. `integrity.ComputeHMAC` and `MerkleRoot` return
  errors precisely because they also run inside the commit hook, where a panic
  is an uncaught crash mid-commit.
- Deliberately ignored errors are written `_ =` or `x, _ :=` with a comment
  saying why. `.golangci.yml` excludes only the idiomatic `defer Close()` and
  `fmt.Fprint*` cases.
- `internal/secrets` holds the single credential-pattern list. It exists
  because that list used to be duplicated in `recorder` and `llm`; do not
  reintroduce a second copy.
- `internal/mcpserver` is the only package that imports `mark3labs/mcp-go`.
  Keep it that way, and read arguments through the library's
  `req.RequireString` / `GetString` / `GetInt` / `GetBool` / `GetArguments`
  helpers rather than indexing `Params.Arguments` — it is typed `any`, and the
  helpers coerce string-encoded numbers that some clients send.
- `internal/index` is a derived cache, not part of the integrity-verified
  changelog. It may be rebuilt at any time and must never be trusted as an
  audit record.

### Dependency pins

`mcp-go` is at `v1.1.x` (see `go.mod`). It was on a pre-release for a long time and its API
moved between them, so treat an upgrade as a real change with its own commit
rather than a routine bump: build it, run the gate, and start the server to
confirm `tools/list` still answers.

`go.mod` requires Go 1.26.7. The floor exists to keep the standard-library CVEs
out of release binaries — CI builds from this directive, so lowering it would
ship against a vulnerable stdlib. `os.Root.MkdirAll`/`WriteFile`, which
`internal/safefs` depends on, arrived in 1.25.

### Cross-client support

githints must work in Claude Code, opencode, Codex CLI, and any other MCP
client. When changing the server, remember:

- **`AGENTS.md` is the canonical file for agent rules.** opencode, Codex,
  Gemini CLI and most other agents read it. `githints init` also writes a
  managed `@AGENTS.md` import into `CLAUDE.md`, which is how Claude Code gets the
  same rules; keep writing both.
- Where things belong: rules every agent working in a repo must follow go in
  `AGENTS.md`; Claude-specific guidance and guidance for developing githints
  itself go in this file below the managed block; documentation for people using
  githints goes in `docs/usage.md`, and design in `docs/architecture.md`.
- `setup` / `mcp-config` write each client's config from `mcpClientList` in
  `mcpconfig.go`, verified against vendor docs. When a client changes its
  format, change the table and `TestClientEntryShapes` together.
- Tool descriptions and the `instructions` string are the only guidance some
  clients ever show. Keep them accurate when behavior changes.
- Don't enable `server.WithInputSchemaValidation`: it would reject the
  string-encoded numbers the `cast`-based helpers exist to tolerate.
