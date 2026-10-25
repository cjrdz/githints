# Changelog

All notable changes to githints. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Releases before this
file are described on the [releases page](https://github.com/cjrdz/githints/releases).

Versions are tagged from `main`; see [CONTRIBUTING.md](CONTRIBUTING.md#cutting-a-release).

## Unreleased

## [0.3.0] - 2026-10-24

Risk-scaled review comes to githints: it says how deep a review a staged change
needs, from what the change touches rather than how big it is, and keeps a
signed, one-time receipt of the review. The MCP tools and CLI commands are
consolidated, the optional Ollama integration is gone (githints opens no network
connections), the pre-commit hook scans what a commit adds for credentials, the
Merkle anchors can be pushed, and the index understands shell, Helm, Ansible
and Terragrunt.

### Upgrade notes

- Run `githints index` once so existing YAML and Terraform files gain the new
  Ansible and Terragrunt links, and shell scripts are indexed.
- **MCP tools and CLI commands were consolidated; the old names keep working for
  one release.** `query_changes(file, query, since, until)` replaces
  `get_file_history`, `get_recent_changes`, `search_changes` and
  `get_changes_in_range`, and its arguments combine (a search within one file's
  history, after a date). `record_change(changes=[...])` replaces
  `record_batch`, `find_symbol(file=...)` replaces `list_symbols`, and
  `get_dependents()` with no file replaces `get_index_summary`. On the command
  line, `githints log` replaces `history`, `recent`, `search` and `changes`;
  `doctor` replaces `status` (and now counts the log); `salt rotate` replaces
  `rotate-salt`; `uninstall -keep-data` replaces `remove`; `setup -print
  -clients=NAME` replaces `mcp-config`. Run `githints init` to refresh the
  managed block in `AGENTS.md`, and restart MCP servers. The tool list an agent
  reads every session is about 10% smaller now and 25% smaller once the old
  names go.
- **The optional Ollama summarization is removed.** The `summarize` argument of
  `get_diff` and `get_recent_changes` is ignored, the `ollama` block of
  `config.json` is ignored (a file that still has one loads as before), and the
  `GITHINTS_OLLAMA_*` variables do nothing. githints opens no network
  connections. Diffs are still redacted on every path.

### Added

- **Risk-scaled review.** `githints review` and the `review_status` MCP tool
  say how deep a review the staged change (or the paths given) needs — low
  (none), medium (one lens) or high (risk, resilience, readability and
  reliability) — and why. The level comes from what changed, not how much: the
  path's class (docs and tests; code; security, CI, infrastructure, migrations,
  hooks, dependency manifests), the framework facets in it, how many files
  import it (15 or more is high), and whether a recent change to it failed or
  was reverted. Files that usually change with these but are not staged are
  listed to read first. `review.rules` in `config.json` can mark more paths as
  a class; it cannot lower a level. githints never runs the review or calls a
  model.
- **Review receipts.** `record_review` and `githints review record` sign a
  receipt (approved, escalated or inconclusive, with findings and the lenses
  covered) for exactly the staged tree into the change log. The commit of that
  tree uses it up and its Merkle anchor covers it; a used receipt never applies
  again, and `verify` checks each one against its commit. `review.gate` set to
  `warn` or `block` makes the pre-commit hook warn about, or stop, a high-risk
  commit without an approval that covers its level; `GITHINTS_REVIEW=0` turns
  review off. The brief lists receipts not yet used. Export packs carry the
  three new columns and a `reviews` view.

- **`githints anchors push|fetch [REMOTE]`** carries the Merkle anchors in
  `refs/notes/githints` to and from a configured remote (default `origin`),
  never forcing. Until they are pushed they protect history only on the
  machine that wrote them; `githints doctor` now warns about that, without
  contacting the remote.

- **The pre-commit hook looks for credentials in what a commit adds.** Each
  added line is checked against the credential shapes `record_change` refuses,
  after removing invisible characters that could split a key; the hook names
  the file and line, never the value. It warns by default;
  `GITHINTS_SECRET_BLOCK=1` stops the commit.

- **Why is this line here?** `query_changes(file, line)` and `githints log
  -file=F -line=N` name the commit that last changed the line (following
  renames), what was recorded for the file in it with its outcome, and the
  review receipt that commit used; a line changed in the working tree shows
  what is recorded for it so far.

- **Infrastructure in the index.** A `shell` language (`.sh`, `.bash`, `.zsh`,
  `.ksh`, `.bashrc`/`.zshrc`/`.envrc`...): functions and capitalised globals
  are symbols, and `source`/`.` of a relative path, or one built from `dirname
  "$0"` or `BASH_SOURCE`, links to the sourced file. Helm and Ansible detectors
  report `infra` facets for charts (`Chart.yaml`), templated objects in a
  chart's `templates/`, and Ansible plays and roles; `import_playbook`,
  `import_tasks` and `include_tasks` link the files they name. Terraform reads
  `terragrunt.hcl` and links its `source` and dependency `config_path`. These
  facets feed risk scoring, so a change to a chart or playbook is high risk.

### Changed

- The search query cap (500 bytes) is enforced inside the store's search
  itself rather than by each caller.
- `-limit` outside 1–500 is refused by every CLI command; `index facets`
  clamped it silently and `changes` had no maximum.
- `record_batch` and the post-commit hook look up the current branch once
  instead of once per row.

### Security

- The review gate checks a receipt's signature before trusting it: an approval
  written into `store.db` by hand is refused at commit time, not only reported
  by `verify` afterwards.
- The pre-commit hook printed staged file names to the terminal as they were;
  a name with an escape sequence could rewrite the screen.
- `get_dependents` and the index summary printed file and import paths from
  the index without neutralizing control characters; they now go through the
  same escaping as every other listing.

### Fixed

- `githints index` (and `index status`, `verify`, `facets`) created
  `.githints/` in a repository where `githints init` never ran. They now refuse,
  like every other command but `init`.
- `githints rotate-salt` read the change log before the transaction that
  re-signs it, so a change recorded in between (by a running MCP server) kept
  the old key and failed `verify` afterwards. A refused rotation also no longer
  leaves a staged `.salt.new` behind.
- **Security:** language specs in `.githints/langs/` were read through the
  directory without checking it: a clone committing `.githints/langs` as a link
  loaded specs from wherever it pointed. The directory, and `.githints/`, must
  now be plain directories.
- The post-commit hook gave every file in a repository's first commit an empty
  diff stat. It now reads the stats of the whole commit with one git process
  instead of one per file.

## [0.2.4] - 2026-10-17

The index now covers the common web, mobile and desktop stacks out of the box
(Python, JavaScript/TypeScript, PHP, Java, Kotlin, Swift, Dart, Ruby, C#, Rust,
C, C++, CSS, Sass and Less, and their frameworks), and the post-commit hook that
keeps it current costs about half what it did on a large repository.

### Upgrade notes

- **Run `githints index` once after upgrading.** New languages are indexed,
  import keys changed for Python, PHP, C# and Rust (`ResolverVersion` 5), and
  declarations whose body brace sits on the next line get their real line
  ranges. `githints doctor` and the hook say so until you do. Restart MCP servers
  so they serve the new binary.
- `index.languages` now defaults to `["auto"]`, every language the binary
  supports, instead of `["go"]`. A repository with no `config.json` indexes its
  Python, TypeScript, Rust and other files from now on. A `config.json` that
  lists languages is unchanged; a C project that lists them needs both `c` and
  `cpp`, since `.h` headers count as `cpp`.
- Run `githints index` again whenever a `package.json`, `composer.json`,
  `pyproject.toml`, `requirements*.txt`, `go.mod`, `Cargo.toml`, `*.csproj`,
  `Gemfile`, `pubspec.yaml`, `vcpkg.json`, `conanfile.txt`, `conanfile.py` or
  `CMakeLists.txt` changes: frameworks are detected from them, and the hook says
  so.
- **Obsidian output removed.** `githints index -obsidian`, the
  `index.obsidian_wikilinks` setting and `GITHINTS_INDEX_OBSIDIAN_WIKILINKS` are
  gone; use `githints index graph` (or `"graph_html": true`) for the graph. Drop
  `-obsidian` from any script: the flag is now an error. A `config.json` that
  still sets the key loads as before and the key is ignored. A
  `.githints/.obsidian/` folder left by an earlier version is no longer written
  and can be deleted.
- Facet changes that affect anything filtering `find_facets` results:
  - A route's detail is its HTTP method in upper case for every framework (`GET`,
    not `get` from FastAPI, Flask or chi).
  - Laravel routes are reported under the framework `laravel` (they were under
    `eloquent`, which now reports only models).
  - Spring models are named by their `@Entity` class (they were named by
    `@Table(name)`), and a class-level `@RequestMapping` is a prefix joined onto
    its methods' mappings instead of a route of its own.
  - chi's `r.Route("/x", func(r chi.Router) {...})` prefixes the routes inside it
    instead of being reported as a route (`r.Route("/x", namedFunc)` still is),
    and `r.Handle` / `r.HandleFunc` routes carry no method (they answer every
    one) instead of the detail `Handle`.
  - New facets: `controller`, `middleware`, `service`, `schema`, `command`,
    `infra` and `style`.
- Run `githints init` in each tracked repository to refresh the managed
  `AGENTS.md` block, which now tells agents to ask the index before reading
  files (MCP clients get the same line from the server's instructions).
- An index built by an earlier version has no stored import keys. The first
  post-commit hook after upgrading computes and stores them, so that one run
  takes as long as before; later runs look them up.

### Added

- **Framework packs.** Language specs and framework detectors live together in
  `internal/index/lang/packs/<language>/` (`language.json`, `frameworks/`, `orm/`),
  each language with sample projects whose expected facets and import edges are
  checked as exact sets. The detector engine gained:
  - a `when_dependencies` gate on the packages a manifest at or above the file
    declares;
  - multi-line matching: a decorator or route call split over lines is found and
    reported at the line it starts on;
  - path rules (`path_pattern`, `name_style: "route"`) for file-based routing;
  - route prefixes declared in the same file, joined onto each route;
  - `lookahead`, `within`, `requires_any`, `path_exclude`, `name_exclude`,
    `unless_imports`, `inside` / `inside_children`, a static `detail`, prefix
    scopes (`statement`, `block`, `indent`) with `parent` chaining, and the
    `flat` and `segments` name styles.
- **Python frameworks.** FastAPI routes include their `APIRouter(prefix=...)`, and
  `api_route`, `add_api_route`, WebSocket routes and `add_middleware` are found;
  Flask routes include their `Blueprint(url_prefix=...)`; pydantic models and
  SQLModel classes without `table=True` are `schema`, SQLModel tables are `model`;
  Django REST Framework, Celery tasks and Django management commands.
- **JavaScript and TypeScript frameworks.** Next.js (app and pages routers,
  route handlers, middleware), Nuxt (pages, server routes, middleware), Vue
  (every `.vue` file is a component; Vue Router routes), Angular (components,
  directives, services, routes), Svelte and SvelteKit, Astro, React without an
  `import React`, React Router (in code, `routes.ts` and Remix flat routes), Expo
  Router, Electron IPC handlers, Express, NestJS, Fastify, Hono, Elysia,
  `Bun.serve` routes and tRPC procedures; TypeORM, MikroORM, Sequelize, Drizzle,
  Mongoose, Zod, Knex migrations, Prisma Migrate and BullMQ workers.
- **PHP frameworks.** Laravel (routes with `Route::prefix`/`group` nesting,
  resources, controllers, service providers, form requests and API resources,
  queued jobs, middleware, migrations, Artisan commands), Symfony (`#[Route]`
  with the class-level prefix, controllers, form types, Messenger handlers,
  console commands), CodeIgniter 4 and 3; Eloquent and Doctrine. PHP files get
  dependency-graph edges from composer's PSR-4 map.
- **Go, Java, C# and Rust frameworks.** Go: `net/http` (Go 1.22 method patterns),
  Gin, Echo, Fiber, gorilla/mux, chi `Route` groups, cobra commands. Java: Spring
  class prefixes, controllers, services, repositories, `@Entity` models,
  `@Scheduled` jobs. C#: ASP.NET Core controller and minimal-API routes
  (`MapGroup` prefixes), controllers, middleware, registered services,
  background services, Entity Framework. Rust: Axum, Actix Web (with
  `web::scope`), Rocket, Tauri commands, Diesel and SeaORM. C# files get graph
  edges by namespace and Rust files by crate module path.
- **Kotlin, Swift, Dart and Ruby.** Four new languages with graph edges: Kotlin by
  declared package, Swift by Swift Package Manager module, Dart through
  `pubspec.yaml` package imports, Ruby through `require` and `require_relative`.
  Frameworks: Android (Activities, Fragments, Compose screens, ViewModels, Room,
  WorkManager, Navigation routes), Ktor, Spring in Kotlin; SwiftUI, UIKit/AppKit,
  SwiftData, Core Data, Vapor and Fluent; Flutter (widgets, Cubit/Bloc and
  ChangeNotifier state), GoRouter, shelf, Drift; Rails (routes with `namespace`
  and `scope`, models, controllers, Active Job and Sidekiq jobs, migrations, rake
  tasks) and Sinatra.
- **C, C++ and CMake.** Headers link by `#include`, searched beside the
  including file and in each directory above it and its `include/` and `src/`,
  so the usual layouts connect without the build's `-I` flags. Functions,
  prototypes, structs, typedefs, macros with a value, classes, enums, aliases,
  concepts, and members defined outside their class (named `Widget::paint`).
  CMake targets, options and functions, with `add_subdirectory` edges.
  Frameworks: Qt (widgets, item models, threads), Crow, Drogon, cpp-httplib,
  oat++ and Pistache routes, Drogon and oat++ controllers, and CMake custom
  targets and tests. `vcpkg.json`, `conanfile.txt`, `conanfile.py` and
  `CMakeLists.txt` count as manifests.
- **CSS, Sass and Less.** Stylesheets are indexed: class and id selectors,
  custom properties, `@keyframes`, Sass `$variables`, mixins, functions and
  placeholders, Less variables and mixins. `@import`, `@use` and `@forward`
  resolve as Sass does (partials, `_index`), and a script's
  `import "./app.css"` or CSS module import links to the stylesheet, through
  `tsconfig` aliases too. A new `style` facet records what a CSS framework is
  configured with: Tailwind v4 `@theme` tokens, `@utility`, `@custom-variant`,
  `@plugin` (daisyUI) and `@layer components` classes, Tailwind v3 and UnoCSS
  config files; Bootstrap, Bulma and Foundation `$variable` overrides in the
  file that imports them (and Bootstrap 3's Less `@variables`); `--bs-`,
  `--bulma-` and `--pico-` theme tokens; and where each framework is imported.
  styled-components and Emotion `styled.x` definitions are `component`s.
- **Dev-environment files.** Dockerfiles (base images and build stages),
  docker-compose services, GitHub Actions and GitLab CI jobs, Kubernetes objects,
  Terraform resources and modules (with module-directory edges), and Make targets
  are indexed, under the new `infra` facet (Make targets are `command`s). These
  files join the dependency graph only when an import links them; YAML lock files
  are skipped.
- **Graph viewer.** A layered layout (importers above what they import, cycles
  sharing a row, arrows on every edge) is the default, with the force layout one
  switch away; hovering previews a node's neighbourhood; test code is hidden by
  default (test files, test-only imports, and helpers only tests import); folders
  can be collapsed into one node each and opened in place; the header counts
  indexed files, what is shown, and what is hidden. The graph JSON marks test
  nodes and edges with `"test": true`.
- `githints index verify` reports stored import keys that no longer match the
  repository (`stale import keys`), as drift.

### Changed

- **The post-commit hook does work in proportion to the commit, not the
  repository.** On Qt's source (9,300 indexed files) a one-file commit went from
  about 270 ms to 130 ms over this release's development; on this repository it
  takes 58 ms, against 72 ms with 0.2.3. Each file's
  import key is stored when it is indexed and looked up, instead of every key
  being recomputed on each commit (a commit that changes a `go.mod`,
  `Cargo.toml`, `composer.json`, `pyproject.toml` or similar file still
  recomputes them all); language rules and framework detectors are compiled the
  first time a file of their language is seen, instead of all of them on every
  run; and the TypeScript workspace is walked only when a workspace package
  import is resolved.
- **A full index is about nine times faster.** This repository, indexing the
  languages 0.2.3 supported: 1.14 s with 0.2.3, 0.12 s now. Qt's source (9,300
  files, which 0.2.3 could not index): 4.2 s, with peak memory down from about
  480 MB to 270 MB over this release's development. Ignored paths come from one `git ls-files` per pass instead of
  a `git check-ignore` per directory; the database is rebuilt in one transaction
  with its indexes created after the rows go in, 100 rows per statement; notes
  render from the scan's results while the database is written, and unchanged
  notes are not rewritten; framework detection blanks a file and reads manifests
  only when a detector's gates pass; comment and string stripping checks for a
  delimiter only at bytes one can start with; Go files are parsed without object
  resolution. During a scan the garbage collector runs half as often and a soft
  256 MiB memory limit applies, unless you set `GOGC` or `GOMEMLIMIT` yourself.
- **A full rebuild is atomic.** A reader such as the MCP server could see an
  empty or half-written index between the old rows' removal and the new rows'
  commit, and a crash in between left one behind.
- Agents are told to ask the index before reading files to find code: the
  managed `AGENTS.md` block and the MCP server's instructions name
  `find_facets`, `find_symbol` and `get_dependency_graph` for it, and
  `find_symbol`'s description says how C++ members are named.
- `index.languages` accepts `"auto"` and defaults to it. A Python (FastAPI)
  repository used to index nothing, reporting only "unsupported files: N".
  `githints index` now names each supported language it skipped because it is
  not enabled, and `index languages` marks what `auto` enables.
- Removed the Obsidian wikilink mode for index notes and the
  `.githints/.obsidian/graph.json` preset, which every full scan wrote even with
  the mode off. The built-in graph viewer replaces both.

### Fixed

- A declaration whose body brace is on the line below its header (Allman style:
  the C# default, PSR-12 PHP classes and methods, and C++ after an initializer
  list) ended at the header, so its line range was one line long and a statement
  in its body shaped like a declaration could be indexed as one.
- A TypeScript or JavaScript side-effect import (`import "./polyfills"`,
  `import "./app.css"`, `import "bootstrap/dist/css/bootstrap.css"`) was not
  recorded: the import pattern required a name, brace or star after `import`.
- `from app.routers import items` linked only to the `app.routers` package, so the
  usual FastAPI wiring had no edge to `app/routers/items.py`. A member that is a
  module now gets its own edge, including over a parenthesized multi-line import.
- A route written over several lines (`@router.post(\n    "/",`) was not found.
- A route whose path is empty (`path("", views.index)`, `@app.get("")`) was dropped
  instead of being reported as `/`.
- chi reported any `.Get("...")` in a file importing chi as a route, so
  `r.Header.Get("Authorization")`, `r.URL.Query().Get("page")` and
  `http.Get("https://...")` showed up as routes; a chi path always starts with
  `/`, and now has to. `r.Method("GET", "/x", h)` was reported as a route named
  `GET`; it is now `/x` with the method, as is `r.MethodFunc`, including
  `http.MethodGet`-style constants. chi's `Query` verb is reported (as `QUERY`),
  and `r.Use(...)` as `middleware`. Checked against the REST example in chi's
  README and every route in chi's own `_examples`.
- A detector rule with `lookahead` could report a match a second time from the
  statement before it, cut short: Crow's `CROW_ROUTE(...)` with `.methods(...)`
  on the next line also came out as a GET route.
- `INDEX.md` printed the last-indexed time as a raw Unix timestamp.
- PHP 8 attributes (`#[Route(...)]`, `#[ORM\Entity]`) were blanked as `#`
  comments, so nothing could detect them.
- A PHP trait's `use HasFactory;` inside a class was recorded as an import.
- The Vue detector never ran on `.vue` files: it was declared for TypeScript only,
  and single-file components are their own language.
- A React component in a file that does not import React (the automatic JSX
  runtime, standard since React 17) was not detected, nor one wrapped in `memo` or
  `forwardRef<...>(...)`.

### Security

- `githints index verify` printed the names of stale notes and ghost files
  without escaping them, so a file name carrying terminal escape sequences
  reached the terminal as-is. They now go through the same sanitizer as every
  other name.

## [0.2.3] - 2026-10-16

### Upgrade notes

- **Upgrade every copy of githints that touches a repository, together.** Existing
  change logs, hints and Merkle anchors verify unchanged with the new version, and
  nothing needs migrating. The reverse does not hold once outcomes exist: a version
  that predates them reports outcome rows as `hmac does not recompute`, lists them
  as ordinary changes, and, if its post-commit hook runs, can claim them and write
  an anchor the new version then flags for good. Restart MCP servers after
  upgrading, run `githints init` so each hook names the new binary (`githints
  doctor` shows which one a hook runs), and export before going back to an older
  version.
- Run `githints init` in shared-mode repositories to update the managed
  `.gitignore` block: it now ignores `.githints/notes/` and `.githints/notes.html`.
- `notes/` is now a reserved directory for hints, like `index/`: a source file under
  a repository directory named `notes/` is still recorded, but its hint is skipped
  with a warning.
- Files with non-ASCII or unusual names are now recorded under their real names.
  Earlier versions stored git's quoted form (`"\303\251.txt"`), so agent records for
  such files were never attached to their commit. Existing rows are left as they are.
- MCP gains three tools (17 in total): `record_outcome`, `record_note` and
  `search_notes`.

### Added

- **Outcomes.** `record_outcome` and `githints outcome` record whether an earlier
  change worked, partly worked, failed or was reverted. Changes are listed with a
  `#id`, and the newest outcome shows beneath each change in history, the
  per-file hints and the brief. Outcomes are signed into the same HMAC chain and
  checked by `githints verify` (the target must exist, come earlier and have the
  same diff hash).
- **Automatic revert detection.** The post-commit hook marks changes `reverted`
  (labelled auto-detected) when a `git revert` really restored their files. The
  commit message is only a pointer: the tree must agree, so a forged message marks
  nothing.
- **Session brief.** `githints brief` and the end of `get_session_context` give a
  ranked, token-budgeted digest: failed attempts, uncommitted work, recent commits,
  blast radius from the index, co-changing files and notes.
- **Notes.** Markdown notes in `.githints/notes/` with a small front matter subset,
  `[[wikilinks]]`, FTS search, `record_note` / `search_notes`, `githints note`, and
  an offline `notes.html` viewer. Private and not integrity-checked.
- **Export pack.** `githints export -o DIR [-duckdb] [-no-notes]` writes
  `changes.jsonl`, `changes.csv`, `CHANGES-full.md`, per-file hints, notes, a
  `manifest.json` (sha256 of every file, the whole-log Merkle root and the verify
  status) and an optional DuckDB script.

### Changed

- The post-commit hook no longer calls Ollama. The `summarize` flags of `get_diff`
  and `get_recent_changes` are the only users; `source = 'llm'` rows from earlier
  versions still verify.
- `record_change` and `githints record` print the new change's id.
- `githints verify` counts changes and outcomes separately.
- Dependencies: `modernc.org/libc` 1.77.1 to 1.78.0 (indirect), and
  `goreleaser/goreleaser-action` 7.2.3 to 7.3.0 in the release workflow.

### Fixed

- Concurrent writers (the MCP server, the hook and the CLI) could sign against the
  same predecessor and fork the HMAC chain, and could be reported as a backwards
  clock. Signing and inserting are now one transaction that takes the write lock
  first, and `busy_timeout` now applies to every pooled connection.
- The legacy `source` column rebuild ran on every open, so a writer that landed in
  between saw a table with no `source` column. It now runs only on a store that still
  has the old constraint, inside one transaction.
- Concurrent MCP requests indexing new notes failed with `SQLITE_BUSY`.
- A commit message could forge a `reverted` outcome on a file with a non-ASCII name.

### Security

- **A link is never written through.** Every write into `.githints/` refused to
  follow a symlink or junction in its directory but not at the target, so a link
  such as `CHANGES.md -> store.db` (which a clone can ship in shared mode) destroyed
  the change log on the next record. The target and every parent are now checked.
  The same applied to hints, `graph.html` and `notes.html`.
- Notes are read as plain files only: symlinks, hard links (so a second name for the
  legacy `.salt` is not served), junctions and unsafe or secret-shaped names are
  refused, and a note is re-checked against the index before it is shown or exported.
- Exports are built from queries and never copy `.githints/`; they refuse non-empty
  or linked destinations, including a Windows junction that points into `.githints`,
  and CSV cells that would run as a spreadsheet formula are neutralized.
