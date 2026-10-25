# githints structural index

How githints indexes code: what it records, the languages and frameworks it
understands, how imports resolve, and the dependency graph built from them.
Everyday use is in [usage.md](usage.md); adding a language or detector is in
[extensibility.md](extensibility.md).

## Structural index

Alongside the change log, githints keeps a **structural index**: a SQLite cache
(`.githints/index.db`) of the symbols, imports and framework roles in the
repository, a markdown note per file under `.githints/index/`, and a rollup at
`.githints/INDEX.md`. It is a derived cache, not part of the integrity-verified
log, so rebuilding it is always safe.

```sh
githints index                 # full rebuild
githints index status          # counts, languages, last scan
githints index verify          # stale notes, ghost rows, stale import keys, uncovered files (exit 1 on drift)
githints index languages       # what this binary can index, and what is enabled here
githints index facets -facet=route   # framework constructs by role
```

The post-commit hook refreshes the files each commit touched, and its cost
follows the commit rather than the repository: on a 9,000-file C++ tree a
one-file commit takes about 130 ms. A full rebuild happens in one transaction, so
the MCP server never reads a half-written index. Use `githints index --force` to
rebuild past the partial-write and size guards.

During a scan githints runs the garbage collector less often (`GOGC=200`) under
a soft 256 MiB memory limit; a `GOGC` or `GOMEMLIMIT` you set yourself is left
alone.

| Agent question | MCP tool |
| --- | --- |
| What is defined in this file? | `find_symbol(file)` |
| Where is this symbol defined? | `find_symbol(name)` |
| What breaks if I change this file? | `get_dependents(file)` |
| What does its neighbourhood look like? | `get_dependency_graph(file, depth)` |
| Is the index fresh, and what are the hubs? | `get_dependents()` |
| Where are the routes / models / components? | `find_facets(facet)` |

Every response includes `last_indexed_at`. Without the MCP server, read the notes
under `.githints/index/` and `.githints/INDEX.md`.

### Languages

`index.languages` selects from the languages the binary supports. The default,
`["auto"]`, enables all of them, plus any the repository adds in
`.githints/langs/`: each file is matched to a language by its extension, so a
language with no files costs nothing. List names instead to narrow it, for
example `["go", "typescript"]`; files of a supported language left out are
counted under "unsupported files", and `githints index` names the language.

| Language | Extensions | Parser |
| --- | --- | --- |
| `go` | `.go` | `go/parser` (exact) |
| `typescript` | `.ts` `.tsx` `.mts` `.cts` `.js` `.jsx` `.mjs` `.cjs` | heuristic |
| `vue`, `svelte`, `astro` | `.vue` `.svelte` `.astro` | script blocks, via the TypeScript parser |
| `python` | `.py` `.pyi` | spec |
| `rust`, `java`, `csharp`, `php` | `.rs` `.java` `.cs` `.php` | spec |
| `kotlin`, `swift`, `dart`, `ruby` | `.kt` `.kts` `.swift` `.dart` `.rb` `.rake` | spec |
| `dockerfile` | `Dockerfile`, `Containerfile`, `Dockerfile.*`, `*.Dockerfile` | spec |
| `yaml` | `.yml` `.yaml` (lock files such as `pnpm-lock.yaml` are skipped) | spec |
| `terraform` | `.tf` `.tofu`, `terragrunt.hcl` (not other `.hcl`, not `.tfvars`) | spec |
| `shell` | `.sh` `.bash` `.zsh` `.ksh`, `.bashrc`, `.bash_profile`, `.bash_aliases`, `.bash_logout`, `.profile`, `.zshrc`, `.zprofile`, `.zshenv`, `.envrc` | spec |
| `makefile` | `Makefile`, `makefile`, `GNUmakefile`, `.mk` | spec |
| `sql`, `prisma` | `.sql` `.prisma` | spec |
| `c` | `.c` | spec |
| `cpp` | `.cpp` `.cc` `.cxx` `.c++`, headers `.h` `.hpp` `.hh` `.hxx` `.h++` `.ipp` `.tpp` `.inl` | spec |
| `cmake` | `CMakeLists.txt`, `.cmake` | spec |
| `css`, `scss`, `less` | `.css` `.pcss` `.postcss` (not `*.min.css`), `.scss`, `.less` | spec |

A `.h` file cannot say whether it is C or C++, so headers belong to `cpp`, whose
rules cover C's declarations too. In a C project, enable both (`auto` does).

`githints index languages` is authoritative for the binary you are running. A
repository can add languages of its own as JSON specs in `.githints/langs/`; see
[Extending the index](extensibility.md).

### How imports are resolved

An import becomes an edge in "Imported by", hub rankings and the dependency
graph only when it resolves to a file in the repository. Resolution is per
package, so a monorepo connects across its packages:

- **Go**: the module path of the nearest `go.mod` at or above each file. Every
  module of a multi-module repo (with or without `go.work`) resolves.
- **TypeScript family**: relative imports; `paths` aliases from the nearest
  `tsconfig.json`/`jsconfig.json` that defines them, following relative
  `extends`; and workspace packages by their `package.json` name (`@acme/ui`,
  `@acme/ui/button`), mapped to source files that exist (a `main` pointing at
  `dist/` is tried as `src/`). Everything else, like `react`, is an external
  package.
- **Python**: modules are named from the nearest project root (`pyproject.toml`,
  `setup.py`, `setup.cfg`) and its `src/`, so `services/api/src/app/x.py` is
  `app.x`. Relative imports (`from .db import x`) are resolved, and
  `from app.routers import items` links to `app/routers/items.py` when `items` is
  a module rather than a name defined in the package. Without a project file,
  names are repo-relative (`app/service.py` is `app.service`).
- **Java**: `src/main/java` (and its test and Kotlin variants) is stripped in
  every module.
- **PHP**: a class is named by the PSR-4 `autoload` (and `autoload-dev`) map of
  the nearest `composer.json` that maps it, so with `"App\\": "app/"`,
  `app/Models/User.php` is `App\Models\User` and `use App\Models\User;` links
  to it. Grouped `use App\Models\{User, Post};` imports each member. A class
  used from its own namespace without a `use` is not an edge, and a project
  without composer (CodeIgniter 3) has no PHP edges.
- **C#**: a file is named by the namespace it declares (`namespace App.Models;`),
  which `using App.Models;` names; files sharing a namespace are one node, like a
  Go package.
- **Rust**: a file is named by the crate module tree, with the crate's name from
  `Cargo.toml`: `src/handlers/todos.rs` is `todo_api::handlers::todos`.
  `use crate::`, `self::` and `super::` paths (and grouped `use crate::{a, b}`)
  link to the deepest module file that exists. Other crates, including other
  members of a workspace, are not linked.
- **Kotlin**: a file is named by the package it declares, so a package's files
  are one node; `import com.x.data.User` links to package `com.x.data`.
- **Swift**: a file is named by its Swift Package Manager target
  (`Sources/<Target>/`), which `import Target` names; files of one module never
  import each other. Xcode projects without a `Package.swift` have no edges.
- **Dart**: files are named by path. `import 'package:app/x.dart'` resolves
  through the nearest `pubspec.yaml` named `app` to its `lib/x.dart`, and a
  relative import to the importing file's directory.
- **Terraform**: a module is a directory, so its `.tf` files are one node, and
  `source = "./modules/vpc"` links to it. A `terragrunt.hcl` links to the module
  its `source` names (`"../../modules//vpc"`) and to the units its
  dependencies' `config_path` names. Other `.hcl` files (Packer, Nomad, Vault,
  `.terraform.lock.hcl`) are not indexed, nor are `.tfvars`, which assign
  values and declare nothing.
- **YAML**: a GitHub workflow calling a reusable workflow in the repository
  (`uses: ./.github/workflows/release.yml`) links to it, and an Ansible
  `import_playbook`, `import_tasks` or `include_tasks` to the file it names,
  relative to the including file. A task file outside a role links only if the
  index records something in it (role task files are role facets). YAML files,
  Dockerfiles, Makefiles and shell scripts appear in the graph only when such a
  link connects them, so configuration does not crowd out the code.
- **Shell**: a sourced file is an import, resolved from the sourcing script's
  directory: `source ./lib/log.sh`, `. ../common.sh`, and the idioms that name
  the script's own directory (`"$(dirname "$0")/lib.sh"`,
  `"${BASH_SOURCE%/*}/lib.sh"`). An absolute path (`/etc/os-release`) is
  external. A path built from another variable (`"$DIR/lib.sh"`), or a bare name
  the shell would look up on `PATH`, is not followed. Symbols are functions, in
  both spellings, and globals assigned in capitals at the start of a line
  (`export KUBECONFIG=...`). Here-document bodies are read as code, `$#` and a
  `#` inside a word end the line for the matcher as a comment would, and a
  script without an extension is not recognised.
- **C and C++**: files are named by path. A compiler finds headers in the
  directories the build passes it (`-I`), which only the build knows, so the
  common layouts are searched instead: `#include "x.h"` is looked for beside the
  including file first, then both forms are looked for in each directory from
  the including file's up to the root, and in its `include/` and `src/`. So
  `<mylib/api.h>` from `libs/core/src/api.c` finds
  `libs/core/include/mylib/api.h`, the nearest match winning as it would for a
  compiler. A header found nowhere (`<stdio.h>`, `<QWidget>`, `<gtest/gtest.h>`)
  is external. Functions defined outside their class are named with it
  (`Widget::paint`), so `find_symbol("Widget")` lists the class and its members.
- **CSS, Sass and Less**: a stylesheet is named by its path, so a script's
  `import "./app.css"` or `import styles from "./Button.module.css"` (through
  `tsconfig` aliases too) links to it. `@import`, `@use` and `@forward` resolve
  the way Sass does: the path as written, with an extension, as a partial
  (`_variables.scss`), or as a folder's `_index.scss`. A package
  (`bootstrap/scss/bootstrap`, `tailwindcss`) is external, webpack's `~` dropped,
  and a URL import is not a dependency. Symbols are class and id selectors
  (`.btn`, `#app`, by the first one in a rule), custom properties, `@keyframes`,
  Sass `$variables`, mixins, functions and `%placeholders`, and Less
  `@variables` and mixins. Stylesheets appear in the graph when something links
  them. Compiled CSS a repository commits (a library's `dist/` or theme files)
  is indexed like any stylesheet; leave it out with `.githintsignore`.
- **CMake**: a `CMakeLists.txt` links to the directories it adds with
  `add_subdirectory`.
- **Ruby**: files are named by path without the extension, with `lib/` stripped;
  `require` and `require_relative` link. A Rails app loads `app/` by constant
  name and never requires those files, so they have no edges between them.

### Framework facets

Facets name the *role* a construct plays, independently of the framework:
Django, GORM, Prisma, SQLAlchemy and Eloquent models are all `model`; chi, Flask,
FastAPI, Spring and Laravel routes are all `route`.

| Facet | What it marks |
| --- | --- |
| `route` | An HTTP (or WebSocket) endpoint, with its method as the detail when known |
| `model` | A stored entity: an ORM class or table |
| `schema` | Data validated or shaped but not stored: pydantic models, SQLModel classes without `table=True` |
| `component` | A UI component |
| `controller` | A class grouping routes |
| `service` | An injectable or provider |
| `middleware` | Request middleware |
| `infra` | Infrastructure the project builds or deploys with: Docker base images and build stages, docker-compose services, GitHub Actions and GitLab CI jobs, Kubernetes objects, Helm charts and their templated objects, Ansible plays and roles, Terraform resources and modules; the detail says which |
| `style` | What a styling framework is configured with: a Tailwind theme token, custom utility, variant, plugin or component class, a Tailwind or UnoCSS config file, a Bootstrap, Bulma or Foundation variable override, a `--bs-` / `--bulma-` / `--pico-` token, and where a CSS framework is imported; the detail says which |
| `command` | An entry point invoked by name: a Make target or CMake custom target, a console command (Artisan, Symfony, cobra, Django `manage.py`), an Electron IPC handler, a Tauri command |
| `migration`, `job`, `test` | Database migrations, background jobs and scheduled tasks, tests |

Shipped detectors, by language:

| Language | Frameworks | ORMs and schemas |
| --- | --- | --- |
| Python | django (with management commands), django REST framework, flask, fastapi | sqlalchemy, sqlmodel, pydantic, celery |
| JavaScript / TypeScript | react, next, vue, vue-router, nuxt, angular, angular-router, svelte, sveltekit, astro, react-router (routes in code, `routes.ts`, and Remix flat file routes); express, nestjs, fastify, hono, elysia, bun (`Bun.serve` routes), trpc; expo-router (React Native); electron (IPC handlers); styled-components and emotion (`styled.x` components) | typeorm, mikro-orm, sequelize, drizzle, mongoose, zod, knex, prisma, bullmq |
| Go | net/http (Go 1.22 method patterns), chi (nested `Route` prefixes, `Method`, `Handle`, `Mount`, middleware), gin, echo, fiber, gorilla/mux, cobra | gorm, bun |
| Java | spring (routes with the class `@RequestMapping` prefix, controllers, services, repositories, `@Entity` models, `@Scheduled` jobs) | |
| PHP | laravel, symfony, codeigniter (4, and 3 by its `application/` layout) | eloquent, doctrine |
| C# | aspnetcore (controller attributes with the class `[Route]` prefix, minimal APIs with `MapGroup`, controllers, middleware, registered services, background services) | entityframework |
| Rust | axum, actix-web, rocket, tauri (commands), tokio | diesel, sea-orm |
| Kotlin | android (Activities, Fragments, `@Composable` screens, ViewModels, WorkManager, Navigation Compose routes), ktor (nested `route {}` blocks), spring | Room (in android) |
| Swift | swiftui (views, observable state), uikit / AppKit (view controllers, views), vapor (routes with `grouped` prefixes) | swiftdata, Core Data, fluent |
| Dart | flutter (widgets, ChangeNotifier / Cubit / Bloc state), go_router, shelf | drift |
| Dev environment | docker (base images, stages), docker-compose (services), github-actions and gitlab-ci (jobs), kubernetes (objects by kind), helm (charts from `Chart.yaml`, and objects in a chart's `templates/` whose name is templated, by kind), ansible (plays by name or hosts, roles by their `tasks/` files), terraform (resources, modules), make (targets) | |
| Ruby | rails (`config/routes.rb` with `namespace` / `scope` blocks, models, controllers, Active Job and Sidekiq jobs, migrations, rake tasks), sinatra | Active Record (in rails) |
| C++ | qt (widgets and Qt Quick items, item models, threads), crow, drogon (`ADD_METHOD_TO` and `PATH_ADD` routes, controllers, filters), cpp-httplib, oat++, pistache | |
| CMake | cmake (custom targets as commands, `add_test` tests) | |
| CSS, Sass, Less | tailwind (v4 `@theme` tokens, `@utility`, `@custom-variant`, `@plugin` such as daisyUI, `@layer components` classes, and v3 `tailwind.config.*`), bootstrap, bulma, foundation, pico, unocss (config) | |

They live in `internal/index/lang/packs/<language>/`; see
[Extending the index](extensibility.md). A route's detail is its HTTP method,
always in upper case (`GET`), whatever the framework's own spelling; file-based
routes (Next.js, Nuxt, SvelteKit, Astro) are named by their URL, so
`app/(shop)/products/[id]/page.tsx` is `/products/[id]`.

Detection runs over comment- and string-stripped code, so an example in a
docstring is not reported, and is gated on a file's imports, its path, or a
dependency declared in a `package.json`, `composer.json`, `pyproject.toml`,
`requirements*.txt`, `go.mod`, `Cargo.toml`, `*.csproj`, `Gemfile`, `pubspec.yaml`, `vcpkg.json`,
`conanfile.txt`, `conanfile.py` or `CMakeLists.txt` (`find_package`, `FetchContent_Declare`,
`CPMAddPackage`) at or above it, so a framework is
only claimed where it is actually used. A route split over several lines is
found, and a prefix declared in the same file is joined on: FastAPI's
`APIRouter(prefix="/items")` makes `@router.get("/{id}")` the route
`/items/{id}`, as do Flask's `url_prefix`, Nest's `@Controller("users")`, Symfony's class-level `#[Route]`,
Hono's `basePath`, Elysia's `prefix` and an Express or Hono router mounted in
the same file. Laravel's `Route::prefix(...)->group(...)` and CodeIgniter's
`$routes->group(...)` apply to the routes inside them, and nested groups join.
A prefix added in another file (`include_router(r, prefix=...)`,
`app.use("/api", router)` with the router imported, Fastify's `register`) is
not followed, and prefixes do not chain. After a manifest changes, run `githints index`: the hook only
re-detects files the commit touched, and says so. Facets appear in each note
under `## Framework`.

### Excluding files

The index skips everything `.gitignore` ignores. A `.githintsignore` at the
repository root (same syntax) excludes more, such as generated code or fixtures.
It can only subtract: it cannot re-include an ignored file.

Signatures stored in the index have string and template contents blanked, so
source strings never reach the cache.

## Dependency graph

```sh
githints index graph                          # -> .githints/graph.html
githints index graph -focus=src/api/user.ts   # a file's neighbourhood (-depth=2 by default)
githints index graph -external                # include stdlib and third-party packages
```

The page is a single offline file: no external scripts or styles, no network
requests (its Content-Security-Policy forbids them), and file names on screen as
text only.

- **Layers** (the default): importers sit above what they import, each node one
  row below the deepest thing that imports it, so entry points are at the top
  and shared foundations at the bottom. Import cycles share a row. Every edge
  points the way the import goes. **Force** is the alternative, a physical
  layout grouped by folder.
- **Hover** a node to preview its neighbourhood (the rest dims); **click** to
  keep it. What it imports is drawn in ink, what imports it in the accent
  color, matching the two lists in the side panel.
- **Tests** are hidden by default: test files, imports made only by tests (in Go
  a package's `_test.go` files share its node), and helpers only tests import.
  The toggle brings them back, and the header says how many are hidden.
- **Group folders** collapses each folder (below the prefix every file shares,
  so `internal/` is split into `internal/cli/`, `internal/index/`...) into one
  square node; click one and choose Open, or double-click it.
- **Color by** folder, language or framework role. It starts on language only
  when no language covers 80% of the code, and uses three colors that stay
  distinct side by side, including for color-vision deficiency, with the rest in
  a neutral "other"; the legend dims any group.
- Node size is how many places import it. Search with `/`, switch to a sortable
  table view, link to a node with `#<name>`, light and dark themes.

Nodes are files where a file is a module (TypeScript, Python) and packages where
many files share an import path (Go). Graphs past 1500 nodes keep the most
connected ones; narrow with `-focus`, or raise `-max-nodes`.

Other formats go to stdout, or to a file with `-o`:

```sh
githints index graph -format=mermaid -focus=main.go -depth=1   # GitHub issues and READMEs
githints index graph -format=dot | dot -Tsvg > graph.svg       # Graphviz
githints index graph -format=json                              # nodes and edges, for scripts
```

In the JSON, a node or edge with `"test": true` is test code (all of a node's
files are tests; only test files make an edge's import).

Agents get the same graph from `get_dependency_graph`.

