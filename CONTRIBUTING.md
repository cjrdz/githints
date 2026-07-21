# Contributing to githints

Thank you for helping improve githints. This guide covers how to build, test,
and make changes.

## Requirements

- Go 1.23 or later.
- Git.
- Linux, macOS, or Windows (Windows requires [Git for Windows](https://gitforwindows.org/), which provides the POSIX sh used by the git hooks).

## Build

```sh
go mod tidy
go build -o githints .
```

## Test

```sh
go vet ./...
go test -race ./...
```

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
- `internal/mcpserver` — MCP stdio server and tool handlers.

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
4. Add a test in `internal/mcpserver/server_test.go` if the tool has
  non-trivial logic.

## Self-tracking

githints can track its own development. After running `githints init` in the
repo, the hooks will ignore changes under `.githints/`, so re-rendered
markdown files do not create self-referential noise.

## Opening issues and PRs

- Use issues for bugs, feature requests, and design questions.
- Keep PRs focused on one change at a time.
- Include tests for new behavior.
- Update the relevant docs in `docs/` or `README.md` if your change affects
  architecture, usage, or contribution workflows.

## License

By contributing, you agree that your contributions will be licensed under the
Apache License 2.0.
