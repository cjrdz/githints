# githints

![CI](https://github.com/cjrdz/githints/actions/workflows/ci.yml/badge.svg)
[![GitHub release](https://img.shields.io/github/v/release/cjrdz/githints?sort=semver)](https://github.com/cjrdz/githints/releases)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cjrdz/githints)](https://goreportcard.com/report/github.com/cjrdz/githints)

Local change tracking for AI coding agents.

githints keeps a small, tamper-evident SQLite journal of what changed in a
repository and **why**, plus a structural index of its code, and serves both to
any agent over MCP or from the command line. The next agent (or person) to open
the repository can catch up on intent instead of re-deriving it from diffs.

Everything stays on your machine: no service, no account, no network access
unless you opt in to a local Ollama model.

## Quick start

Install a prebuilt binary (it is checksum-verified before installing):

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/cjrdz/githints/main/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/cjrdz/githints/main/install.ps1 | iex
```

Then, inside any repository you want tracked:

```sh
githints setup
```

`setup` installs the git hooks, creates `.githints/`, writes the agent
instructions into `AGENTS.md` and `CLAUDE.md`, and registers the MCP server with
every client it detects: Claude Code, opencode, Codex, VS Code (Copilot),
Cursor, Zed, Kiro, Gemini CLI, JetBrains Junie and Continue. Run
`githints setup -list` to see what it found, or `githints doctor` afterwards to
check everything.

Native packages (Arch, Debian, Fedora, Alpine), building from source, and manual
client setup are in the [usage guide](docs/usage.md).

## What it does

- **Records why code changed.** Agents call `record_change` after editing; the
  post-commit hook ties each record to its commit and writes a fallback entry for
  anything left undescribed. A pre-commit gate can warn or block when a staged
  file has no record.
- **Keeps the record honest.** Rows are HMAC-chained, timestamps are checked for
  clock tampering, and each commit anchors a Merkle root in git notes that
  `githints verify` checks, so edited, deleted or re-signed history is detected.
- **Indexes the code.** A regenerable index of symbols, imports and framework
  roles (routes, models, components...) across Go, TypeScript/JavaScript, Vue,
  Svelte, Astro, Python, Rust, Java, C#, PHP, SQL and Prisma, with imports
  resolved per package so monorepos connect.
- **Shows the dependency graph.** `githints index graph` writes an offline,
  interactive HTML viewer; Mermaid, Graphviz and JSON exports are one flag away.
- **Works with any MCP client**, and has CLI equivalents for every read tool.

## Documentation

| Document | For |
| --- | --- |
| [Usage guide](docs/usage.md) | Installing, setting up clients, daily workflow, the index and graph, configuration, what to commit, troubleshooting |
| [Architecture](docs/architecture.md) | Data model, write paths, integrity and security design, package layout |
| [Extending the index](docs/extensibility.md) | Adding a language or a framework detector |
| [Roadmap](docs/roadmap.md) | What is planned and what was decided against |
| [Contributing](CONTRIBUTING.md) | Building, testing, conventions, releases |
| [Security policy](SECURITY.md) | Reporting vulnerabilities, threat model |
| [AGENTS.md](AGENTS.md) | The rules agents follow in this repository |

## CLI at a glance

```sh
githints setup                  # set up the repo and register MCP clients
githints doctor                 # check the whole setup; prints a fix for each problem
githints status                 # store health, hooks, salt, pending records

githints record -file=F -summary=S [-reason=R]   # record a change by hand
githints history -file=F        # why a file looks the way it does
githints recent | search -query=Q | diff -file=F | changes -since=T -until=T

githints verify                 # check the HMAC chain, rendered markdown and Merkle anchors
githints index                  # rebuild the structural index
githints index graph            # offline dependency-graph viewer (.githints/graph.html)

githints help                   # every command; any command takes -h
```

The [usage guide](docs/usage.md#cli-reference) has the full reference.

## Platforms

Linux, macOS and Windows (with [Git for Windows](https://gitforwindows.org/)),
on amd64 and arm64. `git` must be on `PATH`.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
