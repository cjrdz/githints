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

`mcp-go` is pinned to a **pre-release** (`v1.0.0-beta.1`, the 2026-07-28 spec).
Expect the API to move before v1.0.0 final; treat an upgrade as a real change
with its own commit, not a routine bump. `go.mod` requires Go 1.25.14: the 1.25
minor is mcp-go's floor and where `os.Root.MkdirAll`/`WriteFile` arrived, and
.14 is the first patch in that line with the standard-library CVEs fixed. CI
builds from this directive, so lowering it would ship release binaries against
vulnerable stdlib.

### Cross-client support

githints must work in Claude Code, opencode, Codex CLI, and any other MCP
client. When changing the server, remember:

- Claude Code reads `CLAUDE.md`; opencode, Codex, and Gemini CLI read
  `AGENTS.md`. `githints init` writes managed blocks in both, and the workflow
  rules live in `AGENTS.md` only — `CLAUDE.md` imports it.
- Tool descriptions and the `instructions` string are the only guidance some
  clients ever show. Keep them accurate when behavior changes.
- Don't enable `server.WithInputSchemaValidation`: it would reject the
  string-encoded numbers the `cast`-based helpers exist to tolerate.
