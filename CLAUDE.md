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
- **All markdown writes go through `os.Root`** (`hint.writeUnder`).
  `recorder.ValidateFilePath` is a lexical check and cannot see a symlink or a
  Windows junction.
- **Every `hash` argument is checked with `IsValidCommitish`** before reaching
  argv. In `ChangedFiles` and `DiffStat` the value lands *before* the `--`
  separator, so an unchecked one is parsed as a git option.
- **Caps stay enforced in Go**, not just declared in the tool schema:
  `recorder.MaxSummaryLen`, `MaxReasonLen`, `MaxBatchSize`, `clampLimit`,
  `maxSearchQueryLen`, `maxDiffResultBytes`, `gitutil.MaxOutputBytes`.
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

`mcp-go` is at `v1.0.0`. It was on a pre-release for a long time and its API
moved between them, so treat an upgrade as a real change with its own commit
rather than a routine bump: build it, run the gate, and start the server to
confirm `tools/list` still answers.

`go.mod` requires Go 1.26.7. The floor exists to keep the standard-library CVEs
out of release binaries — CI builds from this directive, so lowering it would
ship against a vulnerable stdlib. `os.Root.MkdirAll`/`WriteFile`, which
`hint.writeUnder` depends on, arrived in 1.25.

### Cross-client support

githints must work in Claude Code, opencode, Codex CLI, and any other MCP
client. When changing the server, remember:

- **`AGENTS.md` is the canonical file.** Claude Code, opencode, Codex and
  Gemini CLI all read it. The workflow rules live there and nowhere else.
- `githints init` still writes a managed block in `CLAUDE.md` importing
  `AGENTS.md`. Claude Code reads `AGENTS.md` directly now, so that block is
  belt-and-braces for older versions rather than the mechanism; keep writing it
  until dropping it is worth a release note.
- Anything Claude-specific, or specific to developing githints itself, belongs
  in this file below the managed block. Anything about *using* githints belongs
  in `AGENTS.md`, where every agent will see it.
- Tool descriptions and the `instructions` string are the only guidance some
  clients ever show. Keep them accurate when behavior changes.
- Don't enable `server.WithInputSchemaValidation`: it would reject the
  string-encoded numbers the `cast`-based helpers exist to tolerate.
