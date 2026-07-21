# githints roadmap

This document captures future directions and decisions that are intentionally
out of scope for the current release.

## Short term

### CI linting

Add `golangci-lint` to the CI workflow once the project has settled. This is
kept out of the initial CI pass to avoid blocking PRs on linter nits while the
codebase is still taking shape.

### Dependency risk tracking

`mark3labs/mcp-go` is the de facto Go MCP SDK but is currently pre-1.0
(v0.27.0). Track its releases and be ready for API changes when upgrading.

## Medium term

### Shared / remote store

The current shared-history mode commits rendered markdown only. A true team
store could be:

- A remote SQLite database (e.g. litestream-replicated, or a small server).
- A per-repo `store.db` committed to a separate protected branch or
  `refs/notes/githints`-style git objects.
- Export/import commands that let teams merge independent local stores at
  release time.

Any shared-store design must preserve the HMAC chain semantics and decide who
holds the salt (likely a CI secret or team keyring).

### Merkle root distribution

Today the per-commit Merkle root is stored only in the local
`refs/notes/githints` git note. Future work could:

- Push notes to the remote so CI and other clones can verify them.
- Store the root in commit messages or as a signed tag for stronger
  cross-machine guarantees.

### Key storage hardening

We evaluated moving the salt into the OS keychain and rejected it for now:

- On headless Linux, most keyring backends require a desktop session or
  dbus/Secret Service, which is fragile in CI/server contexts.
- On macOS and Windows, a keyring is readable by the same OS user session,
  so it does not materially change the same-user-agent threat model.
- The real defense against a malicious same-user agent is an external,
  out-of-band anchor (e.g. signed Merkle roots in a protected remote) or a
  passphrase-derived key.

If the project later supports passphrase-protected keys or team-wide keys,
revisit keychain integration as a convenience layer.

## Long term

### Native Windows hooks

The current hooks are POSIX sh scripts because Git for Windows provides sh.
A native PowerShell/cmd hook path could remove that dependency for Windows-only
shops, at the cost of maintaining two hook implementations.

### Web / read-only dashboard

A small HTTP server that serves the rendered markdown and a timeline view would
make githints useful for teammates who do not run an MCP client.
