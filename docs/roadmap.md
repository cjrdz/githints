# githints roadmap

Directions under consideration, and decisions taken against alternatives. Nothing
here is a commitment; what has shipped is described in [usage.md](usage.md).

## Next

### Index follow-ups

Import resolution reaches Rust, PHP, C#, Kotlin, Swift, Dart, Ruby, C and C++
since 0.2.4. Left: prefixes added in another file (`include_router`,
`app.use("/api", router)`, Drogon's `METHOD_ADD`), Rails' nested resources and
Zeitwerk autoloading (Rails files have no edges between them), Swift projects
without a `Package.swift`, C and C++ include paths only the build knows
(`target_include_directories`, `compile_commands.json`), and reusing the
parser's blanked views for detection, the next cost on a large tree.

### Outcome follow-ups

Outcomes are recorded by agents and, for `git revert`, by the post-commit hook.
Left for later: a revert that is itself reverted leaves the original reading
`reverted`; `git reset`, amend, rebase and cherry-pick leave no revert commit to
notice; and nothing yet ranks past failures by how often they recur.

### Project-aware matching in monorepos

Imports are matched by name. Two projects in one repository that both expose a
module called `app` share that name, so an import can attach to the wrong one.
Resolving relative to the importing project, as relative imports already are,
would fix it.

### Pushing the Merkle anchors

Each commit's anchor lives in `refs/notes/githints`, which git does not push by
default, so the anchors only protect history on the machine that wrote them
until someone pushes them. Options: push notes from the hook when a remote is
configured, or a CI check that they were pushed. This is the highest-value item
here for integrity, and a prerequisite for any shared store.

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

### DuckDB or embeddings inside githints

`githints export -duckdb` writes a script for exploring a pack in DuckDB, which
stays a place to look at the data, not something githints runs or depends on.
A bundled database engine or an embeddings index would add a runtime dependency
(and cgo for most of them) to a tool whose point is to stay small, local and
pure Go; FTS5 in the existing SQLite covers note search.

### Per-agent Ollama settings in the repository

The Ollama endpoint, model and timeout can be overridden per process with
`GITHINTS_OLLAMA_*` environment variables. A tracked file of per-agent settings
would arrive in clones, which is exactly the place a hostile repository should
not get to steer where diff content is sent. Ollama is also no longer called from
the post-commit hook; only the `summarize` flags use it.

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

### A parse cache for full rebuilds

Unchanged files could skip parsing on `githints index` by content hash. A full
rebuild mostly runs after an upgrade or a configuration change, exactly when such
a cache would have to be thrown away, and the hook already rescans only what a
commit touched, so the cache would rarely pay for itself.

### Faster but unsafe SQLite settings

`synchronous=OFF` would remove an occasional checkpoint sync from a full index,
but SQLite documents that it can corrupt the database on power loss; `NORMAL` in
WAL mode stays consistent, and the index already rebuilds in one transaction.

### tree-sitter

The maintained Go bindings need cgo, which would break the pure-Go,
cross-compiled release. See [extensibility.md](extensibility.md#design-decisions).
