# Extending the structural index

How to teach githints a new language or a new framework. Most of the time this
means adding a JSON file, not writing Go.

The index is a derived cache, not part of the integrity-verified change log: it
can be rebuilt at any time and is never an audit record. That boundary is what
makes the design below safe. A bad parser or a bad spec costs a rebuildable
cache, never corrupted history.

- [How the index is built](#how-the-index-is-built)
- [Adding a language](#adding-a-language)
- [Adding a framework detector](#adding-a-framework-detector)
- [Repository-supplied languages](#repository-supplied-languages)
- [Native parsers and scan hooks](#native-parsers-and-scan-hooks)
- [Testing](#testing)
- [Design decisions](#design-decisions)
- [Limits](#limits)

## How the index is built

For each file in an enabled language, a **parser** produces symbols (functions,
types, constants...) and imports. A second pass of **framework detectors**
produces facets (routes, models, components...). Both run over *blanked* lines:
comments removed and string contents emptied, with line numbers preserved, so a
pattern never matches inside a comment or a string.

Imports become edges in the dependency graph when the target resolves to a file
in the repository. Each language decides how one of its files is named by the
files that import it (its **import path**); see
[How imports are resolved](index.md#how-imports-are-resolved) for what the
shipped languages do.

| Piece | Where |
| --- | --- |
| Native parsers (Go, TypeScript, Vue, Svelte, Astro) | `internal/index/lang/*.go` |
| Language specs | `internal/index/lang/packs/<language>/language.json` |
| Framework and ORM detectors | `internal/index/lang/packs/<language>/{frameworks,orm}/**.json` |
| Sample projects with expected results | `internal/index/lang/packs/<language>/testdata/<case>/` |
| Repository-supplied specs | `.githints/langs/*.json` in a tracked repository |

## Adding a language

A language spec is JSONC (comments and trailing commas allowed), embedded in the
binary and validated when it loads. A minimal example:

```jsonc
{
  "language": "python",
  "extensions": [".py", ".pyi"],
  "depth_style": "indent",
  "comments": { "line": ["#"] },
  "strings": [
    { "open": "\"\"\"", "close": "\"\"\"", "escape": "\\", "multiline": true },
    { "open": "\"", "close": "\"", "escape": "\\" }
  ],
  "symbols": [
    { "kind": "func", "requires": "def",
      "pattern": "^\\s*(?:async\\s+)?def\\s+(?P<name>\\w+)\\s*\\((?P<sig>[^)]*)\\)" },
    { "kind": "type", "requires": "class", "pattern": "^\\s*class\\s+(?P<name>\\w+)" }
  ],
  "import_path": "dotted",
  "import_path_index_names": ["__init__"],
  "imports": [
    { "requires": "import", "pattern": "^\\s*from\\s+(?P<path>[.\\w]+)\\s+import\\b" },
    { "requires": "import", "pattern": "^\\s*import\\s+(?P<path>[\\w.]+)" }
  ]
}
```

### Fields

| Field | Meaning |
| --- | --- |
| `language` | Name used in `index.languages`. No whitespace or commas, and not `auto`. |
| `extensions` | File extensions this language claims, with the dot. An extension can belong to one language only. |
| `depth_style` | How nesting is read: `brace` (C family), `indent` (Python family) or `none`. Declarations inside a function body are skipped. A body whose brace is on a line below the header (Allman style, or after a C++ initializer list) is found too. |
| `comments.line` | Line-comment openers, e.g. `["//", "#"]`. |
| `comments.block` | Block-comment pairs, e.g. `[["/*", "*/"]]`. |
| `filenames` | Base names, or base-name globs, the language takes whatever their extension: `Dockerfile`, `Dockerfile.*`, `Makefile`, `.bashrc`, `terragrunt.hcl`. A spec needs `extensions` or `filenames`. |
| `exclude_filenames` | Base-name globs not taken even with one of the language's extensions: `pnpm-lock.yaml`. |
| `graph` | `linked` makes the language's files dependency-graph nodes only when an import links them (YAML, Dockerfile, Makefile, shell). |
| `comments.not_line` | Sequences that start like a line comment but are code, e.g. PHP's `["#["]` for attributes while `#` stays a comment. Each must extend a `line` entry. |
| `strings` | String rules: `open`, `close`, `escape` (one byte or empty), `multiline`, `continue_on_escape`, and `interp_open` / `interp_close` for interpolation that should be read as code. |
| `regex_literals` | Treat `/.../` as a literal to blank (JavaScript-style). |
| `symbols[].keep_strings` | Match a symbol rule on the view that keeps strings, for names written as quoted labels (Terraform's `resource "aws_s3_bucket" "logs"`). |
| `ignore_names` | Names never indexed as symbols, for languages where a declaration has no keyword: Dart's `if (x) {` has a function's shape. A rule that captures a `type` group is also dropped when the type is one of these: C's `return f(x);` has the shape of `int f(x);`. |
| `symbols` | Rules producing symbols. `kind` is one of `func`, `method`, `type`, `const`, `var`; the pattern must capture `name` and may capture `sig`. |
| `imports` | Rules producing imports; the pattern captures `path`, and may capture `members` (Python's `from pkg import a, b`, PHP's grouped `use`). `top_level: true` skips lines inside a block (a PHP trait's `use`); `relative: true` joins the path to the importing file's directory (Ruby's `require_relative`). Imports are matched on a view that keeps string contents (a path is usually a string) but still strips comments. |
| `import_path` | How a file is named by importers: `slash` (repo-relative path without extension, TypeScript-style), `dotted` (`a.b.c`, Python- and Java-style), `psr4` (PHP: the class name composer's PSR-4 map gives the file), `declared` (the file names itself; `import_path_declared` is a pattern capturing `path`, run outside comments: C#'s `namespace App.Models;`), `rust` (the crate module tree), `spm` (Swift: the `Sources/<Target>/` module), `dart` (repository paths; `package:` URIs resolve through `pubspec.yaml`), `directory` (the file's directory: a Terraform module), `include` (C and C++: the repository path; an include is looked for beside the file when the rule has `relative`, then in each directory above it and its `include/` and `src/`), `stylesheet` (CSS, Sass and Less: the repository path; an import is resolved from the importing file's directory as written, with each of the language's extensions and `.css`, as a `_partial`, and as a folder's `_index` or `index`; a package keeps its name without `~`, and a URL is dropped), or `""` (none: the language contributes symbols and outbound imports but no inbound edges). |
| `import_path_index_names` | File stems that stand for their directory (`__init__`, `index`). |
| `import_path_strip_prefixes` | Source roots that are not part of the name (Java's `src/main/java`); stripped wherever they occur, so every module of a multi-module build is covered. |
| `import_path_root_markers` | Files that mark a project inside the repository (`pyproject.toml`); names are taken relative to the nearest marked directory. |
| `import_path_root_subdirs` | Source directories inside a marked project (`src`). |

Every rule has a `requires` substring that must appear on the line before the
regular expression runs. It is required, not an optimization detail: matching
every rule of every language against every line is what would make a scan slow.

A `dotted` language also has relative imports (`from ..models import X`)
resolved against the importing file during a scan.

### Checklist

1. `internal/index/lang/packs/<language>/language.json`.
2. A fixture at `internal/index/testdata/sample.<ext>` exercising every
   construct the spec claims.
3. A table-driven test naming the symbols and imports expected from it. If the
   spec declares `import_path`, add a round-trip test: the key `ImportPath`
   produces for a file must equal what the import rules extract from a file
   importing it (`TestPythonImportPathRoundTrip` is the pattern).
4. A line in `.gitattributes` for the extension, so CRLF checkouts on Windows do
   not skew line numbers.
5. The language table in [docs/usage.md](index.md#languages).

No Go code and no registry edit is needed; the registry loads every embedded
spec.

## Adding a framework detector

A detector names the role constructs play, independent of the framework. Django,
GORM and Eloquent models are all `model`, so an agent can ask for every model in
a repository without knowing which ORM it uses.

```jsonc
{
  "framework": "django",
  "languages": ["python"],
  "when_imports": ["django.*"],
  "rules": [
    { "facet": "model", "requires": "class",
      "pattern": "^\\s*class\\s+(?P<name>\\w+)\\s*\\([^)]*\\bmodels\\.Model\\b" },
    { "facet": "route", "requires": "path(", "keep_strings": true,
      "pattern": "\\b(?:re_)?path\\(\\s*[\"'](?P<name>[^\"']*)[\"']\\s*,\\s*(?P<detail>[\\w.]+)" }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `framework` | Name shown in results. No `:` or `,`. |
| `languages` | Languages whose files the detector reads. A language without a parser makes the detector inert, and a test fails if one is named. |
| `when_imports` | Gate: the file imports one of these (`*` is a wildcard; a submodule matches its package). |
| `when_dependencies` | Gate: a `package.json`, `composer.json`, `pyproject.toml`, `requirements*.txt`, `go.mod`, `Cargo.toml` or `*.csproj` (its `Sdk` and `PackageReference`s), `Gemfile`, `pubspec.yaml`, `vcpkg.json`, `conanfile.txt`, `conanfile.py` or `CMakeLists.txt` (`find_package`, `FetchContent_Declare`, `CPMAddPackage`) at or above the file declares one of these. For frameworks a file need not import: React with the automatic JSX runtime, Nuxt's auto-imports, Next.js pages. Python names are lower-cased with `_` read as `-`. |
| `unless_imports` | Veto: the detector does not run on a file importing any of these, whatever the gates say. Go's `net/http` stands down in a chi or gin file, which share `.HandleFunc(`. |
| `path_globs` | Gate: the file path matches one of these. A glob may start at any directory, so `app/**/page.tsx` finds `apps/web/app/x/page.tsx`; `**` spans directories, `{a,b}` is an alternative. |
| `prefixes[]` | Route prefixes declared in the same file; see below. A prefix takes `requires`, `pattern` (capturing `prefix`, optionally `var` and `parent`), `keep_strings`, `lookahead`, `name_style: "segments"`, and `scope`: `statement` (the call's parentheses), `block` (the braces opened on its line: Ktor's `route("/api") { }`) or `indent` (the lines indented under it: Rails' `namespace :admin do`). A `parent` names the variable a router was derived from, so Gin's `v1 := api.Group("/v1")` joins `api`'s prefix. |
| `rules[].inside` | Match only in the block indented under a line matching this pattern (a compose file's `services:`); with `inside_children`, only that block's direct children. |
| `rules[].detail` | The detail of a match that captured none: `service`, `ci job`, `stage`. |
| `rules[].name_exclude` | Drop a match whose name matches: CodeIgniter 3's `default_controller` lives in the route array. |
| `rules[].facet` | One of `route`, `model`, `schema`, `component`, `controller`, `service`, `middleware`, `migration`, `job`, `test`, `command`, `infra`, `style`. |
| `rules[].requires` | Substring gate, checked on the code view of the lines the match spans. |
| `rules[].requires_any` | Gate of alternatives, checked the same way: one rule for `.get(`, `.post(`... |
| `rules[].within` | Only statements whose code contains this: the routes object of `Bun.serve(`. |
| `rules[].lookahead` | Extend the match over up to this many following non-blank statements (at most 8), so a decorator can reach the class it decorates. A match must begin in the statement the window starts from; one beginning in a joined statement is found from there instead. |
| `rules[].path_exclude` | Skip files whose path matches: every `.vue` file is a component, except under `pages/`. |
| `rules[].pattern` | Captures `name`, optionally `detail` and `var` (a router variable, for prefixes). |
| `rules[].keep_strings` | Match on the view that keeps string contents (routes are usually strings). |
| `rules[].path_pattern` | Run the rule only on files whose path matches; a `name` (and `detail`) captured here names the facet. Without `pattern`, a matching file yields one facet at line 1. |
| `rules[].name_style` | `segments` reads quoted path segments as a path (Vapor's `app.get("users", ":id")` is `/users/:id`). `route` turns a captured path into a URL: `(group)` and `@slot` segments dropped, a final `index` names its directory, a leading `/`. `flat` reads a React Router / Remix flat-route file name: `users.$id.edit` is `/users/:id/edit`, `_index` the index, `_auth.` a pathless layout, `$` a splat, `($lang)` optional, `[.]` a literal dot. |

A detector runs on a file when **any** of its gates holds; `path_pattern` then
narrows individual rules.

**Statements, not lines.** A rule matches a *statement*: a line plus the lines
after it while a parenthesis it opened is still open (up to 64 lines), joined
with newlines. Patterns are compiled in multi-line mode, so `^` and `$` still
mean a line and `\s*` crosses into the next, and a decorator or call written
over several lines matches like one written on a single line. Each match is
reported at the line it starts on, and `requires` is checked on the lines the
match covers, not the rest of the statement.

**Decorated classes.** A decorator closes its parenthesis before the class
begins, so `@Component({...})` and `export class UserCard` are two statements.
`lookahead` joins the following ones:

```jsonc
{ "facet": "service", "requires": "@Injectable", "lookahead": 4,
  "pattern": "@Injectable\\((?s:.)*?\\)\\s*(?:@\\w+(?:\\((?s:.)*?\\))?\\s*)*export\\s+class\\s+(?P<name>\\w+)" }
```

**Route details.** A route's `detail` that names an HTTP method is upper-cased,
so every framework reports `GET`.

**Routes from paths.** File-based routing is a `path_pattern` with
`name_style: "route"`:

```jsonc
{ "facet": "route", "name_style": "route",
  "path_pattern": "(?:^|/)app(?P<name>(?:/[^/]+)*)/route\\.ts$",
  "pattern": "^export\\s+async\\s+function\\s+(?P<detail>GET|POST|PUT|PATCH|DELETE)\\b" }
```

`app/(admin)/users/route.ts` exporting `GET` and `POST` yields `/users GET` and
`/users POST`.

**Prefixes.** A prefix rule captures `prefix`. With a `var` group it belongs to a
router variable and applies to routes whose own `var` group names it (FastAPI's
`router = APIRouter(prefix="/items")`, Flask's `Blueprint(url_prefix=...)`).
Without one it applies to every route after it, until the next (Nest's
`@Controller("users")`, Symfony's class-level `#[Route]`, which uses `lookahead`
to require that a `class` follows). With `scope: "statement"` it applies only
inside the call it was found in, which is where a group's closure is: Laravel's
`Route::prefix("admin")->group(function () {...})`, CodeIgniter's
`$routes->group(...)`; nested groups join, outermost first. A route matched with no path, such as `path("", view)`
or a bare `@Get()`, is the root of the prefix in force. A prefix added in another
file is not followed.

Two rules are enforced at load time:

- **A detector must declare a gate**: `when_imports`, `when_dependencies` or
  `path_globs`. `class X(Model)` is the shape of every ORM ever written;
  attributing it to the wrong framework is worse than not detecting it.
- **A `keep_strings` rule or prefix must declare `requires` (a rule may use
  `requires_any`), and the gate is checked against the code view.** Otherwise a rule matches its own shape inside
  any string literal: a Go raw string containing `r.Get("/x", h)` was once
  reported as a route.

Rules match statements rather than symbols because most of what is worth finding
is not a declaration: a route is a call, a Django URL pattern is a list entry.
Facets are deduplicated within a file on facet, framework, name and detail.

### Checklist

1. The detector at `internal/index/lang/packs/<language>/frameworks/<name>.json`,
   or `orm/` for an ORM or schema library. Folders are for people: what a
   detector applies to comes from its `languages` and gates. A new folder needs
   adding to the `go:embed` line in `packs.go`; `TestEveryPackFileIsEmbedded`
   fails until it is.
2. A sample project at `packs/<language>/testdata/<case>/`: a few files shaped
   like a real one, including what should *not* be detected (a route-shaped
   string, a class that only looks like a model), plus `expected.json` listing
   every facet (`facet framework file name [detail]`) and every edge
   (`from -> to`). `TestPackSamples` scans it and compares both as exact sets,
   so a detector is judged on what it invents as well as what it misses.
   `GITHINTS_UPDATE_PACKS=1 go test ./internal/index -run TestPackSamples`
   writes `expected.json` from the current output; read every line of the diff
   before committing it.
3. The detector list in [docs/usage.md](index.md#framework-facets).

## Repository-supplied languages

A repository can add languages of its own, without rebuilding githints, by
putting specs in `.githints/langs/`. They load on the next scan and
`githints index languages` marks them `[from .githints/langs]`.

- A repository spec can add a language but never redefine one the binary
  provides, so `go` means the same thing in every checkout. A spec that clashes
  on a language name or an extension is reported on stderr and skipped.
- A spec that fails to parse or validate is reported and skipped, so a bad file
  cannot fail a commit or stop the other languages from indexing.
- At most 32 files of 64 KiB each, plus the per-spec limits below.

This is safe to allow because Go's `regexp` is RE2 (no catastrophic
backtracking), the limits are enforced in Go, and the result only ever feeds the
rebuildable index. Framework detectors cannot be supplied by a repository.

## Native parsers and scan hooks

A language that needs real parsing fidelity, or a region-extraction step a line
matcher cannot express, ships as Go in `internal/index/lang/` implementing
`LanguageParser` (`Language`, `Extensions`, `Parse`) and registered in
`NewRegistry()`. Vue, Svelte and Astro are examples: they keep the `<script>`
regions of a file and hand them to the TypeScript parser.

Two optional interfaces let any parser, native or spec, join the rest of the
system:

```go
// ImportPath names a file the way importers do, so it can receive edges.
interface{ ImportPath(root, file string) (string, error) }

// BeginScan installs per-scan state (project configs, caches) and returns
// its teardown. The scan layer calls it once per parser per scan.
interface{ BeginScan(root string) func() }
```

`BeginScan` is how Go caches the nearest `go.mod` per directory and how the
TypeScript family loads per-directory `tsconfig.json` paths and workspace
packages, without the scan layer knowing about either. State shared by several
parsers (TypeScript, Vue, Svelte and Astro share one project) is
reference-counted so it is built once and torn down after the last scan ends.

## Testing

`TestPackSamples` is the accuracy check for every pack. For speed,
`BenchmarkPackSamples` scans every sample in one repository (all languages,
all detectors), and `TestScanCostModel` fails if a change makes a scan start
git per directory or rewrite unchanged notes. A detector costs nothing for a
file its gates reject, so a gate is also a performance feature: gate on
imports, dependencies or paths, cheapest first, and the file is never blanked.

The project gate applies to every change:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
```

Tests use the standard library `testing` package only. They shell out to the
real `git`, so it must be on `PATH`.

Two properties are worth testing for any new language:

- **Blanking preserves one output line per input line.** Everything downstream
  maps a matched line back to the source by index. `FuzzBlankerLineCount` exists
  because four separate bugs once let an escape swallow a newline.
- **Two scans of the same tree produce byte-identical notes.** Rendered notes are
  committed in shared mode, so unstable ordering would produce spurious diffs.

## Design decisions

**Specs instead of hand-written parsers.** Adding Rust, Java, C#, PHP and SQL as
bespoke Go would have been thousands of lines of regex-dense code nobody could
review. A declarative spec over a shared blanking lexer makes a language a
reviewable data file.

**No tree-sitter.** The maintained Go bindings require cgo, and githints is pure
Go with two direct dependencies; cgo would break the cross-compiled release and
make `-race` painful on Windows. The pure-Go runtimes available are not mature
enough to build on. The spec path accepts lower fidelity in exchange (see
[Limits](#limits)); Go keeps `go/parser` and stays exact.

**Facets as a separate axis.** Most framework support collapses into ten roles
across languages. Recording roles rather than per-framework concepts is what
makes "every HTTP route in this repository" answerable without knowing the stack.

## Limits

- **Fidelity is lower than a real parse.** Declarations and detector statements
  spanning lines are handled by joining while a parenthesis is open, but a
  construct identifiable only from an adjacent statement is invisible (a GORM model recognizable only
  by embedding `gorm.Model` on the next line). Java and C# methods must carry an
  access modifier to be recognized.
- **Per-spec limits:** 32 extensions, 16 string rules, 64 symbol and import rules
  combined, 1000-character patterns. Detectors: 32 rules, 8 prefixes, and 32
  each of `when_imports`, `when_dependencies`, `path_globs` and `requires_any`;
  `lookahead` up to 8 statements.
- **Some projects are recognized only at a conventional root.** Next.js,
  SvelteKit, Astro and Nuxt route directories are matched at the repository
  root or under `apps/<x>` or `packages/<x>`; a project elsewhere is gated by
  its dependency but its routes are not named.
- **Auto-imports are not edges.** Nuxt pages use components and composables
  without importing them, so the graph does not link them.
- **Inheritance is not followed.** A pydantic `ItemCreate(ItemBase)` is not a
  schema to a pattern that looks for `BaseModel`.
- **SQL and Prisma have no inbound import key.** A schema file defines many
  tables, so there is no single name for the file. Their foreign keys and
  relations are still recorded as outbound edges.
- **Rust and C# have no import path yet.** Rust module paths depend on `mod`
  declarations and the crate root; C# needs project files. PHP's needs
  `composer.json` and so does not cover a project without one.
- **PHP heredocs and docblock annotations are not read.** A heredoc body is
  treated as code, and `@Route` in a docblock is a comment; Symfony's
  attributes are what is detected.
- **Shell is read line by line.** A string rule cannot express a
  here-document, whose terminator is chosen per use, so its body is read as
  code (a `name() {` line inside one would be a symbol). Strings end at the
  end of a line, so an apostrophe in a here-document cannot hide the rest of
  the file. A comment begins at any `#`, so `$#` and `foo#bar` end the line
  for the matcher; `${...}` is a literal, so `${#items[@]}` does not. A
  script without an extension (`bin/deploy`) is not recognised: a file name
  cannot show its shebang.
- **A file that records nothing is not a graph node.** An Ansible task file is
  a bare list, with no top-level key to be a symbol; one inside a role is a
  role facet and so is linked, one elsewhere is not. A shell script that only
  runs commands is linked only through what it sources.
- **Helm templates are partly YAML.** An object in a chart's `templates/`
  whose name is a template (`{{ include "app.fullname" . }}`) is reported by
  the `helm` detector under its chart, and one with a written-out name by
  `kubernetes`, so each object is reported once; an object whose `kind` is
  itself templated is not reported. A `templates/` directory of an Ansible
  role holds Jinja, which looks the same, and is excluded.
- **Name-based matching.** An import names a module, not a project, so two
  projects in one repository exporting the same module name share it.
- **Everything under `.githints/index/` is regenerated** and stale notes are
  pruned, so nothing else can keep files there.
