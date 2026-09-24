# Extending the structural index

This document describes how to add languages and frameworks to the structural
index, and the plan for making that cheap enough that anyone can do it.

The index is a derived cache, not part of the integrity-verified changelog. It
may be rebuilt at any time and must never be trusted as an audit record. That
boundary is what makes most of the design below safe: a bad parser or a bad
spec costs a rebuildable cache, never a corrupted history.

## Why this exists

Today every language is bespoke Go. `internal/index/lang/typescript.go` is 631
lines: a hand-written seven-state lexer, eight anchored regexes, and a
brace-depth context stack. Adding Rust, Python, Java, C#, PHP and SQL the same
way is three to four thousand lines of regex-dense Go that nobody can review or
maintain, and it still would not cover frameworks.

The goal is that adding a language becomes adding *data*, and that adding a
framework becomes adding data too.

## What is actually coupled today

An audit of the repository found the code far more language-agnostic than the
documentation is. Outside the four parser files, exactly three non-test Go
locations hardcode a language or extension:

| Location | Hardcoded |
| --- | --- |
| `internal/index/lang/types.go:193` | `case ext == ".go":` in `LocalImportPath` |
| `internal/index/lang/types.go:210` | `filepath.Join(root, "go.mod")` in `readModulePath` |
| `internal/config/config.go:70` | `Languages: []string{"go"}`, the shipped default |

Everything else is registry-driven and needs no change to accept a new
language:

- `scan.go`, `verify.go` and `render.go` dispatch purely through `Registry`.
- `render.go:144` interpolates `sym.Kind` raw. There is no per-kind switch, no
  icon table, no per-language section in the note template.
- `store.go:74-82` declares `kind TEXT` with no enum and no `CHECK`
  constraint.
- `main.go` `usageText`, the `agentsBlock` written by `githints init`, and the
  MCP `instructions` string name no languages.

So the `lang` package doc comment's promise — that a parser can be added as one
new file without touching storage or rendering — is nearly true already. The
real burden is documentation plus two closed switches.

### Known gaps this plan closes

- **`TestRegistry` is a tripwire.** `lang_test.go:111` asserts
  `len(got) != 4` and lists the four names, so it breaks the moment a fifth
  language registers.
- **Unknown languages silently kill the hook path.** `githints index` exits
  non-zero on an unsupported language name, but the post-commit hook only
  warns (`main.go:616`) and the commit succeeds. Because `config.json` ships in
  clones, a repo pinned to a language a teammate's older binary does not know
  would silently stop indexing for them.
- **`GITHINTS_INDEX_LANGUAGES` is documented but does not exist.**
  `AGENTS.md:197` advertises it; `applyEnvOverrides` handles the other five
  index variables and not that one.
- **Nothing can report what the binary supports.** There is no
  `githints index languages`, and `ResolveLanguages` names the bad language
  without listing the valid ones, even though `Registry.Languages()` already
  returns exactly that list.
- **Per-language project config means another global.** Supporting tsconfig
  `paths` required the process-global `activeTSPaths` plus two edits in
  `scan.go` (lines 34 and 313). Python's `sys.path`, PHP's PSR-4, C#'s csproj
  and Java's package roots would each repeat that, breaking the "don't touch
  the scan layer" promise once per language.
- **`Symbol` has no extensible field.** Name, kind, path, lines, signature —
  all typed and consumed. `Signature` is the only free-form string, and
  overloading it would fight the blanking guarantee pinned by
  `TestTypeScriptSignatureBlanksStringContents`. There is also no per-symbol
  `language` column, so "show me all Python symbols" is not expressible.

## Three axes, not one

The request that motivated this — Rust, Go, TypeScript, JavaScript, Java, C#,
Python, SQL, plus Tokio, chi, Laravel, Vue, Svelte, Astro, React, Django,
Flask, FastAPI, Spring Boot, Bun ORM, Entity Framework, Prisma, SQLAlchemy,
GORM and Eloquent — is three different problems wearing one hat.

| Axis | Items | Needs |
| --- | --- | --- |
| File formats | rust, python, java, c#, php, sql, `.vue`, `.prisma` | A language parser |
| Frameworks and ORMs | tokio, chi, laravel, django, flask, fastapi, spring, react, vue, GORM, Bun, EF, Prisma, SQLAlchemy, Eloquent | Patterns over already-parsed symbols |
| Import resolution | Python `sys.path`, PHP PSR-4, C# csproj, Java package roots | Per-ecosystem resolvers |

The middle bucket is seventeen of the twenty-five items, and it collapses
hard. Django models, GORM structs, Entity Framework entities, Eloquent models,
SQLAlchemy classes and Prisma schema are all **`model`**. chi, Flask, FastAPI,
Spring and Laravel routes are all **`route`**. React, Vue, Svelte and Astro are
all **`component`**. Seventeen integrations become roughly six facet kinds
recognized by data-driven rules keyed on imports plus symbol shape.

That is also where the payoff is. "Every HTTP route in this repo" or "every
database model", answered uniformly across languages, is far more useful to an
agent than another symbol count.

## Why not tree-sitter

Tree-sitter is the standard answer and it does not fit. The maintained Go
bindings (`tree-sitter/go-tree-sitter`, `smacker/go-tree-sitter`) are cgo.
githints is pure Go with only two direct dependencies, and CLAUDE.md already
documents how painful cgo plus `-race` is on Windows; adopting it would break
the release pipeline.

The pure-Go runtimes that claim to solve this do not survive inspection.
`malivvan/tree-sitter` (wazero plus a WASM build) has three commits, one
contributor and no releases. `drummonds/gotreesitter` ("206 grammars, pure Go")
has no stars and no releases. Neither is a foundation for a core subsystem.

So the design works without a real grammar, and accepts the fidelity cost that
implies. See *Limits* below.

## Design

Four pieces. No new dependencies: specs are JSONC parsed with the existing
`stripJSONC` (`tsconfig.go:136`) and embedded with `go:embed` from the standard
library.

### 1. Spec-driven language parsers

`LanguageParser` (`types.go:45`) is already the right abstraction — three
methods, no language assumptions. It does not change. What is added is one
generic *implementation* of it, driven by a declarative spec, so adding a
language means adding data:

```jsonc
{
  "language": "python",
  "extensions": [".py"],
  "depth_style": "indent",
  "comments": { "line": "#", "block": ["\"\"\"", "\"\"\""] },
  "symbols": [
    { "kind": "func", "requires": "def",
      "pattern": "^\\s*(?:async\\s+)?def\\s+(?<name>\\w+)\\s*\\((?<sig>[^)]*)\\)" },
    { "kind": "type", "requires": "class",
      "pattern": "^\\s*class\\s+(?<name>\\w+)" }
  ],
  "imports": [
    { "pattern": "^\\s*from\\s+(?<path>[\\w.]+)\\s+import" },
    { "pattern": "^\\s*import\\s+(?<path>[\\w.]+)" }
  ]
}
```

The spec is a Go struct with JSON tags, so in-repo languages are
compile-checked while user-supplied specs unmarshal into the same type.
Native parsers coexist: Go keeps `go/parser`, which is exact and free.

`requires` is a cheap `strings.Contains` gate evaluated before the regex runs.
Without it, matching is O(lines x patterns) across every registered language
and would undo the indexing performance work.

### 2. A generalized blanking lexer

`cleanTSLines` (`typescript.go:469`) is the most valuable existing asset in the
package. It blanks string, template and regex bodies and strips comments while
preserving line count and ordering, and it re-enters `${...}` as code so braces
stay balanced. Every language needs exactly that pass before line regexes are
safe.

Generalize it into a `Blanker` parameterized by comment delimiters and string
rules (escape byte, multiline, escape-continuation, interpolation). After that,
no future language re-solves "don't match inside a string".

The `depth_style` of `brace`/`indent`/`none` originally planned here moved to
Phase 3. Blanking does not need it -- the Blanker tracks braces only to match
interpolation -- and the consumer is `SpecParser`, which needs to know whether
a declaration is top-level. Adding the enum before anything read it would have
been speculative.

### 3. Framework detectors and facets

A second registry, running after the language parse, over symbols and imports.
Also pure data:

```jsonc
{
  "framework": "django",
  "when_imports": ["django.db"],
  "facets": [
    { "facet": "model", "kind": "type", "extends": "models.Model" }
  ]
}
```

This requires the one schema change in the plan: a `facets` table and a
`language` column on `symbols`. That is cheap here — `index.db` has no schema
version and needs none, because it is gitignored, regenerable, and `FullScan`
calls `Clear()` before every rebuild. Schema evolution is "delete and rescan",
not a migration.

### 4. Scan hooks and import resolution

Two optional interfaces, both type-asserted off the parser:

```go
// BeginScan lets a parser install per-scan state (project config, alias maps)
// and returns its teardown. The scan layer calls it once per parser in
// ParserSet and defers the result.
interface{ BeginScan(root string) func() }

// ImportPath inverts Parse's ImportedPath so a file can be found by the key
// other files import it under.
interface{ ImportPath(root, file string) (string, error) }
```

`BeginScan` replaces the `activeTSPaths` pattern with one loop in the scan
layer, so Python, PHP, C# and Java each get the same seam instead of adding a
global apiece. `ImportPath` opens the closed switch in `LocalImportPath`, which
currently returns `unsupported language for import path resolution` for
anything that is not Go or TypeScript — meaning a new language cannot appear in
"Imported by", cannot be linked from another note's "Imports", and cannot rank
as a hub.

## Contributing a language

Once the foundation lands, adding a language is:

1. `internal/index/lang/specs/<name>.json` — the spec.
2. `internal/index/testdata/sample.<ext>` — a fixture exercising every
   construct the spec claims.
3. A table-driven test case naming the symbols and imports expected.
4. A line in `.gitattributes` for the new extension, or CRLF checkouts on
   Windows will skew line-number assertions.

No Go, no registry edit, no `scan.go` change. A language needing real parsing
fidelity can still ship a native Go parser instead; the interface is unchanged.

## User-supplied specs

A repository may drop specs in `.githints/langs/` and `.githints/frameworks/`
to extend its own index without rebuilding githints.

This is defensible here for two reasons that are already true. Go's `regexp` is
RE2, so a hostile pattern cannot cause catastrophic backtracking. And the index
is a derived cache, so the blast radius of a bad spec is a rebuildable file.
Caps still apply, enforced in Go: pattern count, pattern length, compiled-size
budget, and per-file scan time.

## Implementation plan

Phases are ordered so each is independently shippable and the risky work lands
last. One task at a time; each task is its own commit.

### Phase 1 — Close the gaps (no behavior change)

Small, verified fixes that must exist before anything else adds a language.

1. Make `TestRegistry` property-based against `Registry.Languages()` instead of
   asserting `len == 4` and a literal name list.
2. Add `githints index languages`, and include the supported set in
   `ResolveLanguages`'s error message.
3. Implement `GITHINTS_INDEX_LANGUAGES`, which `AGENTS.md:197` already
   documents.
4. Make an unknown language skip-with-warning rather than fail the whole scan,
   so a config naming a future language cannot silently kill a teammate's
   incremental index.

### Phase 2 — Generalized lexer

5. Extract `cleanTSLines` into a delimiter-parameterized `Blanker` with
   `brace`/`indent`/`none` depth styles.
6. Re-point `TypeScriptParser` at it and prove byte-identical output on the
   existing fixtures.

### Phase 3 — Spec-driven parsers

7. The `Spec` type plus a JSONC loader reusing `stripJSONC`, including the
   `depth_style` deferred from Phase 2. *(done)*
8. `SpecParser`, a generic `LanguageParser` driven by a `Spec`. *(done)*
9. Registry loads embedded specs from a `go:embed` FS alongside native parsers.
   *(done)*
10. ~~Port the 48-line Astro parser to a spec as proof~~ — **ship Python
    instead.** *(done)* Astro extracts frontmatter and `<script>` blocks and
    delegates to the TypeScript parser: inverted blanking, keep a region and
    discard the rest, which a line-oriented spec cannot express. Porting it
    would have meant contorting the format or weakening the parser. Python
    exercises the same path honestly and is a language that was asked for.
11. Load user specs from `.githints/langs/`, with caps. *(done)*

Two things surfaced during the work and are now part of the design. Imports
are matched against a second blanked view that keeps string contents, because
an import path is almost always inside a string literal and the fully blanked
view shows an empty one; comments are still stripped there, so a commented-out
import is not recorded as a dependency. And repository specs register last, so
they can add a language but never redefine one the binary provides.

### Phase 4 — Import resolution and scan hooks

12. `BeginScan` optional interface; move `activeTSPaths` install/teardown onto
    it and delete the duplicated edits in both scan entry points. *(done)*
13. `ImportPath` optional interface; `LocalImportPath` consults the registry
    instead of switching on extension. *(done)*

Spec-driven languages join the graph by declaring `import_path`: `slash` for
the TypeScript-style file key, `dotted` for Python- and Java-style module
names, with `import_path_index_names` for stems that stand for their directory
(`__init__`, `index`). A spec that declares none simply does not implement the
interface, rather than inventing a key nothing would match.

`ImportPath` is the inverse of a language's import rules, and nothing enforces
that the two agree, so a language implementing it should be tested against its
own extraction. `TestPythonImportPathRoundTrip` is the pattern.

### Phase 5 — Facets

14. Schema: `language` column on `symbols`, with schema versioning. *(done)*
    The `facets` table moved to task 15 so it would land with the code that
    writes it, rather than sitting empty.
15. Framework detector spec type and registry, plus the `facets` table.
    *(done)* Detectors match **lines, not symbols**: a route is usually a call
    rather than a declaration, so hanging facets off symbols would have covered
    models and missed routes entirely.
16. MCP tool and CLI for querying facets. *(done)* `find_facets` and
    `githints index facets`, plus a `## Framework` section in each note and a
    breakdown in `index status`.
17. Detector specs for the listed frameworks and ORMs.

### Phase 6 — Languages

18. One task per language: python, rust, java, c#, php, sql, vue, prisma.

## Verification

The project gate, all of it, for every task:

```sh
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
```

Tests use the standard library `testing` only — no testify, no fixtures.
Tests shell out to real `git`, so it must be on `PATH`.

Per-phase, additionally:

- **Phase 2** must prove the generalized lexer produces byte-identical output
  to `cleanTSLines` on every existing fixture, or the TypeScript parser has
  silently regressed.
- **Phase 3** must keep the Astro tests passing unchanged after the port; that
  is the proof the spec path is equivalent for a real language.
- **Phase 5**'s schema change is validated by deleting `index.db` and
  rescanning, not by a migration test.
- Two scans of the same tree must still produce byte-identical notes.
  Rendered notes are committed in `-share` mode, so unstable ordering would
  produce spurious diffs in every consumer's repository.

## Limits

- **Fidelity drops.** Regex over blanked lines gets roughly 85-95% symbol
  recall against a real parse. Go keeps `go/parser` and stays exact. Treat the
  spec path as the reach tier, not a replacement.
- **SQL and Prisma have no import graph.** They populate symbols and facets but
  contribute no edges, so "Imported by" will not work for them.
- **Language names are constrained.** `EncodeLanguageCounts` (`types.go:393`)
  rejects `:` in a name and uses `,` as its record separator, so both are
  illegal in a spec's `language` field. Validate on load.
- **Auxiliary notes get pruned.** `pruneStaleNotes` (`render.go:71`) deletes
  every `.md` under `.githints/index/` not produced by the current render, so a
  detector cannot emit side files without changing that.
