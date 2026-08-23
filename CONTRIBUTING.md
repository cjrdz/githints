# Contributing to githints

Thank you for helping improve githints. This guide covers how to build, test,
and make changes.

## Requirements

- Go 1.25.14 or later. The 1.25 minor comes from `mark3labs/mcp-go` and from
  `os.Root.MkdirAll`; the patch level is the first in that line with the
  standard-library CVEs fixed, and `govulncheck` in CI enforces it.
- Git.
- Linux, macOS, or Windows (Windows requires [Git for Windows](https://gitforwindows.org/), which provides the POSIX sh used by the git hooks).
- A C toolchain if you want to run `go test -race`, which needs cgo. On Windows,
  `winget install BrechtSanders.WinLibs.POSIX.UCRT` provides a self-contained
  mingw-w64; the Go distribution alone is not sufficient.

## Build

```sh
go mod tidy
go build -o githints .
```

## Test

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
```

CI runs all five on Linux, macOS, and Windows.

Some tests create temporary git repositories and call the real `git` binary.
Make sure `git` is on your `PATH` and your user config does not conflict with
what tests expect.

## Project layout

- `main.go` — CLI entry point and command wiring.
- `version.go` — version variable stamped by goreleaser at link time.
- `internal/config` — `.githints/config.json` loader and env overrides.
- `internal/store` — SQLite schema, migrations, and queries.
- `internal/recorder` — write path: validation, secret scanning, insert,
  render trigger.
- `internal/hint` — markdown rendering and markdown integrity checks.
- `internal/integrity` — salt, key derivation, HMAC chain, Merkle root,
  `rotate-salt`.
- `internal/gitutil` — thin wrappers around `git` commands.
- `internal/llm` — local Ollama client and diff scrubbing.
- `internal/secrets` — the single credential-pattern list, shared by the
  recorder's write-path refusal and the diff scrubber.
- `internal/mcpserver` — MCP stdio server and tool handlers. The only package
  that imports `mark3labs/mcp-go`.

## Code style

- Keep package doc comments and comments that explain non-obvious behavior or
  security rationale. Avoid comments that merely restate the next line of code.
- Use `fmt.Errorf("...: %w", err)` for error wrapping.
- Prefer small, focused functions.
- Run `go vet` and `go test -race` before opening a PR.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/) for the
project history. This makes the changelog readable and helps automated tooling.
Examples:

```
feat: add githints render command
fix: reject flag-like hashes in get_diff
docs: clarify threat model in architecture.md
```

## Schema changes

If you need to change the database schema, use additive migrations in
`internal/store/store.go` rather than rewriting the base schema. Existing user
stores must upgrade automatically when they open.

## Adding MCP tools

1. Register the tool in `internal/mcpserver/server.go`.
2. Add a handler function in the same file.
3. Reuse `recorder.Record`/`BatchRecord` for writes so validation and
  rendering stay centralized.
4. Read arguments through the mcp-go helpers (`req.RequireString`,
  `req.GetString`, `req.GetInt`, `req.GetBool`, `req.GetArguments`). Do not
  index `req.Params.Arguments` — it is typed `any`, and the helpers coerce the
  string-encoded numbers some clients send.
5. Bound anything unbounded in Go, not only in the declared schema: clamp
  limits with `clampLimit`, and cap sizes the way `recorder.MaxSummaryLen` and
  `maxDiffResultBytes` do. Schema `minimum`/`maximum`/`maxLength` are advisory;
  clients are not required to enforce them.
6. Add a test in `internal/mcpserver/server_test.go` if the tool has
  non-trivial logic.

## Cutting a release

Work lands on `dev`, then `dev` merges to `main`. A release is cut by pushing an
annotated tag on `main`:

```sh
git checkout main && git pull
git tag -a v0.1.2 -F -   # see below for what the message should contain
git push origin v0.1.2
```

Two things follow from that tag:

- **The tag body becomes the release notes.** `.goreleaser.yml` puts
  `{{ .TagBody }}` at the top of the generated notes, so any upgrade warning
  belongs in the tag message — write it once there rather than fixing the
  release afterwards with `gh release edit`. Everything below it is a changelog
  grouped by Conventional Commit type.
- **The release cannot publish unless CI passes on that commit.** `release.yml`
  calls `ci.yml` as a reusable workflow and the publish job declares
  `needs: checks`, so the six binaries only ship after `build` has succeeded on
  ubuntu, macOS, and Windows and after `lint` and `vulncheck` are green.

To check what a release would produce without publishing anything:

```sh
goreleaser check
goreleaser release --clean --skip=publish,announce,validate
cat dist/CHANGELOG.md
```

`dist/` is gitignored.

There are deliberately **no SBOMs**. A static pure-Go binary already carries its
dependency graph — `go version -m githints` lists every module with its version
and `h1:` hash, and `govulncheck -mode=binary` scans a shipped artifact
directly. The provenance attestation is kept, because build origin is the one
thing the binary cannot self-report:

```sh
gh attestation verify <file> --repo cjrdz/githints
```

## Self-tracking

githints can track its own development. After running `githints init` in the
repo, the hooks will ignore changes under `.githints/`, so re-rendered
markdown files do not create self-referential noise.

One wrinkle: this repo's `AGENTS.md` is hand-written and more detailed than the
generic block `init` installs, so running `init` here appends a redundant
managed block. Delete the block (everything between the
`<!-- >>> githints (managed) -->` markers) after initializing; the hand-written
content is the one to keep. `CLAUDE.md` is the normal case — its managed block
is just the `@AGENTS.md` import, and the repo-specific guidance lives below it.

## Opening issues and PRs

- Use issues for bugs, feature requests, and design questions.
- Keep PRs focused on one change at a time.
- Include tests for new behavior.
- Update the relevant docs in `docs/` or `README.md` if your change affects
  architecture, usage, or contribution workflows.

## License

By contributing, you agree that your contributions will be licensed under the
Apache License 2.0.
