# Contributing to githints

Thank you for helping improve githints. This guide covers building, testing,
conventions and releases. The design is described in
[docs/architecture.md](docs/architecture.md).

## Requirements

- The Go version in `go.mod` (currently 1.26.7) or later. The floor tracks
  standard-library CVEs rather than language features, and `govulncheck` in CI
  enforces it; `os.Root.MkdirAll`, which the file-confinement code depends on,
  arrived in 1.25. CI reads the version from `go.mod`, so bumping it there is the
  only change needed.
- `git` on `PATH`: tests create temporary repositories and run the real binary.
- Linux, macOS or Windows (Windows needs
  [Git for Windows](https://gitforwindows.org/), which provides the POSIX sh the
  hooks run in).
- A C toolchain for `go test -race`, which needs cgo. On Windows,
  `winget install BrechtSanders.WinLibs.POSIX.UCRT` provides a self-contained
  mingw-w64; the Go distribution alone is not enough.
- [golangci-lint](https://golangci-lint.run/) and
  `go install golang.org/x/vuln/cmd/govulncheck@latest`, at the versions pinned in
  `.github/workflows/ci.yml`.

## Build

```sh
go build -o githints .
```

The binary at the repository root is gitignored. `go mod tidy -diff` must print
nothing: CI checks that the committed module graph is tidy, and releases build
exactly what is committed.

## Test

Run the whole gate before claiming a change works:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
```

CI runs it on Linux, macOS and Windows. Tests that need a file symlink skip on
Windows without Developer Mode; directory links fall back to a junction.

Fuzz targets (`FuzzBlankerLineCount`, `FuzzExportersEscape`) run as ordinary
tests over their seed corpus; to fuzz for real:

```sh
go test -run=^$ -fuzz=FuzzExportersEscape -fuzztime=60s ./internal/index/graphexport/
```

## Project layout

The package map, with what each package owns, is in
[docs/architecture.md](docs/architecture.md#package-layout). In short:
`main.go` and the `cli_*.go`, `doctor.go` and `mcpconfig.go` files are the CLI;
`internal/` holds the store, recorder, renderers, integrity, git access, file
and text safety, the structural index and the MCP server.

## Conventions

- Tests use the standard library `testing` package only: no testify, no external
  fixtures. A regression test should fail on the code before the fix; check that
  it does.
- Return errors; do not panic. Code that runs inside a git hook must never crash
  a commit.
- Wrap errors with `fmt.Errorf("...: %w", err)`. A deliberately ignored error is
  written `_ =` (or `x, _ :=`) with a comment saying why.
- Keep comments that explain non-obvious behaviour or security rationale; do not
  restate the next line.
- Every cap is enforced in Go, not only declared in a tool schema.
- githints-managed files are read and written through `internal/safefs`; text
  from recorded data, file names or symbols is displayed through
  `internal/textsafe`. Do not add a second escaper or credential list.
- Nothing but JSON-RPC may reach stdout in `serve` mode; diagnostics go to
  stderr.

[CLAUDE.md](CLAUDE.md) lists the invariants that must not regress, each with the
test that guards it.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/): the release
changelog is grouped by type.

```
feat(index): resolve imports per package in monorepos
fix(security): confine githints file I/O so links cannot escape the repo
docs: reorganize the usage guide
```

## Common changes

### Schema changes

Use additive migrations in `internal/store/store.go` rather than rewriting the
base schema; existing stores upgrade when they open. The index database is a
cache and can change freely (it is rebuilt), but bump
`index.ResolverVersion` when a change makes stored import keys disagree with
newly computed ones.

### Adding an MCP tool

1. Register it in `internal/mcpserver/server.go` with a description that is
   accurate: for some clients it is the only guidance they show.
2. Read arguments through the mcp-go helpers (`RequireString`, `GetString`,
   `GetInt`, `GetBool`), never by indexing `Params.Arguments`; the helpers accept
   the string-encoded numbers some clients send.
3. Clamp limits with `clampLimit` and cap sizes in Go.
4. Reuse `recorder.Record` / `BatchRecord` for writes.
5. Add a CLI twin if it is a read tool, and a test.
6. Mention it in the server `instructions` string and in `docs/usage.md`.

### Adding a language or framework

See [docs/extensibility.md](docs/extensibility.md). Usually a JSON file, a
fixture and a test.

### Adding an MCP client to `setup`

Add an entry to `mcpClientList` in `mcpconfig.go`, working from the client's own
documentation (and source, where the docs are silent): the file, the top-level
key, the exact entry shape, whether the server starts in the project directory
(if not documented, pin the root), and how to detect the client. Add its shape to
`TestClientEntryShapes` and a row to the client table in `docs/usage.md`.

## Self-tracking

This repository tracks its own development with githints, in CLI mode: the
binary is built at the repository root and run as `./githints`. Record each
change with `./githints record` as [AGENTS.md](AGENTS.md) describes.

`AGENTS.md` here is hand-written and more detailed than the generic block
`githints init` installs, so running `init` in this repository appends a
redundant managed block to it; delete that block afterwards (everything between
the `<!-- >>> githints (managed) -->` markers). `CLAUDE.md` is the normal case:
its managed block is the `@AGENTS.md` import, and the development guidance lives
below it.

Do not commit `.githints/`, the binary, or the MCP client configs `setup` writes
for this repository (they name this machine's paths); `.gitignore` covers them.

## Cutting a release

Work lands on `dev`, then `dev` merges to `main`. A release is an annotated tag
on `main`:

```sh
git checkout main && git pull
git tag -a v0.2.0 -F -   # the message becomes the top of the release notes
git push origin v0.2.0
```

- **The tag body becomes the release notes.** `.goreleaser.yml` puts
  `{{ .TagBody }}` at the top; put upgrade notes there (for example, "run
  `githints index` once"). Below it is a changelog grouped by commit type.
- **The release cannot publish unless CI passes on that commit.** `release.yml`
  runs `ci.yml` as a reusable workflow and the publish job needs it.
- Releases build exactly the committed module graph (no `go mod tidy` at release
  time), with pinned tool versions, without a restored build cache, and with
  build-provenance attestations.

To check what a release would produce without publishing:

```sh
goreleaser check
goreleaser release --clean --skip=publish,announce,validate
cat dist/CHANGELOG.md
```

There are deliberately no SBOMs: a static Go binary carries its dependency graph
(`go version -m githints`), and `govulncheck -mode=binary` scans a shipped
artifact directly. The provenance attestation is kept because build origin is
the one thing the binary cannot report about itself:

```sh
gh attestation verify <file> --repo cjrdz/githints
```

## Issues and pull requests

- Use issues for bugs, feature requests and design questions; report security
  problems privately as described in [SECURITY.md](SECURITY.md).
- Keep pull requests focused on one change, with tests for new behaviour.
- Update the relevant docs (`README.md`, `docs/`, `AGENTS.md`) when behaviour
  changes.

## License

By contributing, you agree that your contributions will be licensed under the
Apache License 2.0.
