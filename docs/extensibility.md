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
[How imports are resolved](usage.md#how-imports-are-resolved) for what the
shipped languages do.

| Piece | Where |
| --- | --- |
| Native parsers (Go, TypeScript, Vue, Svelte, Astro) | `internal/index/lang/*.go` |
| Language specs | `internal/index/lang/specs/*.json` |
| Framework detectors | `internal/index/lang/detectors/*.json` |
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
| `language` | Name used in `index.languages`. No whitespace or commas. |
| `extensions` | File extensions this language claims, with the dot. An extension can belong to one language only. |
| `depth_style` | How nesting is read: `brace` (C family), `indent` (Python family) or `none`. Declarations inside a function body are skipped. |
| `comments.line` | Line-comment openers, e.g. `["//", "#"]`. |
| `comments.block` | Block-comment pairs, e.g. `[["/*", "*/"]]`. |
| `strings` | String rules: `open`, `close`, `escape` (one byte or empty), `multiline`, `continue_on_escape`, and `interp_open` / `interp_close` for interpolation that should be read as code. |
| `regex_literals` | Treat `/.../` as a literal to blank (JavaScript-style). |
| `symbols` | Rules producing symbols. `kind` is one of `func`, `method`, `type`, `const`, `var`; the pattern must capture `name` and may capture `sig`. |
| `imports` | Rules producing imports; the pattern captures `path`. Imports are matched on a view that keeps string contents (a path is usually a string) but still strips comments. |
| `import_path` | How a file is named by importers: `slash` (repo-relative path without extension, TypeScript-style), `dotted` (`a.b.c`, Python- and Java-style), or `""` (none: the language contributes symbols and outbound imports but no inbound edges). |
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

1. `internal/index/lang/specs/<language>.json`.
2. A fixture at `internal/index/testdata/sample.<ext>` exercising every
   construct the spec claims.
3. A table-driven test naming the symbols and imports expected from it. If the
   spec declares `import_path`, add a round-trip test: the key `ImportPath`
   produces for a file must equal what the import rules extract from a file
   importing it (`TestPythonImportPathRoundTrip` is the pattern).
4. A line in `.gitattributes` for the extension, so CRLF checkouts on Windows do
   not skew line numbers.
5. The language table in [docs/usage.md](usage.md#languages).

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
| `when_imports` | The file must import one of these (`*` is a wildcard). |
| `path_globs` | Or the file path must match one of these, for frameworks whose convention is a location. |
| `rules[].facet` | One of `route`, `model`, `component`, `migration`, `job`, `test`. |
| `rules[].requires` | Substring gate, as in language specs. |
| `rules[].pattern` | Captures `name`, optionally `detail`. |
| `rules[].keep_strings` | Match on the view that keeps string contents (routes are usually strings). |

Two rules are enforced at load time:

- **A detector must declare `when_imports` or `path_globs`.** `class X(Model)` is
  the shape of every ORM ever written; attributing it to the wrong framework is
  worse than not detecting it.
- **A `keep_strings` rule must declare `requires`, and the gate is checked
  against the code view.** Otherwise a rule matches its own shape inside any
  string literal: a Go raw string containing `r.Get("/x", h)` was once reported
  as a route.

Rules match lines rather than symbols because most of what is worth finding is
not a declaration: a route is a call, a Django URL pattern is a list entry.
Facets are deduplicated within a file on facet, framework, name and detail.

Add the detector at `internal/index/lang/detectors/<framework>.json`, a fixture
and a test, and the facet list in [docs/usage.md](usage.md#framework-facets).

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

**Facets as a separate axis.** Most framework support collapses into six roles
across languages. Recording roles rather than per-framework concepts is what
makes "every HTTP route in this repository" answerable without knowing the stack.

## Limits

- **Fidelity is lower than a real parse.** Declarations spanning lines are
  handled by joining continuation lines, but a construct identifiable only from
  an adjacent line is invisible to a line matcher (a GORM model recognizable only
  by embedding `gorm.Model` on the next line). Java and C# methods must carry an
  access modifier to be recognized.
- **Per-spec limits:** 32 extensions, 16 string rules, 64 symbol and import rules
  combined, 1000-character patterns. Detectors: 32 rules and 32 `when_imports`.
- **SQL and Prisma have no inbound import key.** A schema file defines many
  tables, so there is no single name for the file. Their foreign keys and
  relations are still recorded as outbound edges.
- **Rust, C# and PHP have no import path yet.** Rust module paths depend on
  `mod` declarations and the crate root; PHP needs PSR-4 maps from
  `composer.json`; C# needs project files. Each is a scan hook away.
- **Name-based matching.** An import names a module, not a project, so two
  projects in one repository exporting the same module name share it.
- **Everything under `.githints/index/` is regenerated** and stale notes are
  pruned, so nothing else can keep files there.
