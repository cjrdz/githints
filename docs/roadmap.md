# githints roadmap

Directions under consideration, and decisions taken against alternatives. Nothing
here is a commitment; what has shipped is described in [usage.md](usage.md).

## Next

### Import resolution for Rust, PHP and C#

These languages index symbols and outbound imports but receive no inbound edges,
because a file's import name does not follow from its path: Rust needs `mod`
declarations and the crate root, PHP needs PSR-4 maps from `composer.json`, C#
needs project files. Each is a `BeginScan` hook plus an `ImportPath`, in the same
shape as the Go and TypeScript resolvers.

### Project-aware matching in monorepos

Imports are matched by name. Two projects in one repository that both expose a
module called `app` share that name, so an import can attach to the wrong one.
Resolving relative to the importing project, as relative imports already are,
would fix it.

### Pushing the Merkle anchors

Each commit's anchor lives in `refs/notes/githints`, which git does not push by
default, so the anchors only protect history on the machine that wrote them
until someone pushes them. Options: push notes from the hook when a remote is
configured, or a CI check that they were pushed.

## Later

### Cross-repository view

Each repository is fully isolated today. A workspace file listing several
repositories, with read-only queries across their stores and a combined graph,
would serve multi-repo systems. It must not merge the integrity chains, which are
per repository by design.

### Shared or remote store

Shared mode commits rendered markdown only. A real team store could be a
replicated SQLite database, store objects on a protected ref, or export and
import commands that merge local stores. Any design has to keep the HMAC chain
semantics and decide who holds the salt (a CI secret, a team keyring).

### Native Windows hooks

The hooks are POSIX sh, which Git for Windows provides. A PowerShell or cmd hook
would remove that dependency for Windows-only teams, at the cost of maintaining
two hook implementations.

## Decided against, for now

### OS keychain for the salt

On headless Linux most keyring backends need a desktop session or D-Bus, which is
fragile in CI and on servers; on macOS and Windows a keyring is readable by the
same user session, so it does not change the same-user threat model. The real
defense against a same-user attacker is an external anchor (pushed or signed
Merkle roots) or a passphrase-derived key. Revisit if passphrase or team keys
arrive.

### A local web server for the graph

The dependency graph ships as a static, offline HTML file instead. A server would
add an attack surface (authentication, DNS rebinding, CSRF) for little gain over
a file that opens anywhere and cannot reach the network.

### tree-sitter

The maintained Go bindings need cgo, which would break the pure-Go,
cross-compiled release. See [extensibility.md](extensibility.md#design-decisions).
