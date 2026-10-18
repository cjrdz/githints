package lang

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// A Spec is a language described as data rather than as Go code: what hides
// code from the matcher (comments, strings), and what a declaration looks like
// once it is visible. Adding a language this way is adding a file, not a
// parser.
//
// Specs are parsed as JSONC, so they may carry comments explaining why a
// pattern is shaped the way it is -- which, for a regex describing a language,
// is usually the most valuable line in the file.
//
// The limits below are enforced on every spec, not only repository-supplied
// ones. A spec that trips one is a spec that would be unreadable anyway, and
// enforcing uniformly means the in-repo specs prove the limits are livable.
const (
	MaxSpecExtensions = 32
	MaxSpecStrings    = 16
	MaxSpecRules      = 64 // symbol and import rules combined
	MaxSpecPatternLen = 1000
)

// ImportPathStyle says how a file maps back to the key other files import it
// by. Declaring one is what lets a spec-driven language take part in the
// dependency graph rather than only contributing symbols.
type ImportPathStyle string

const (
	// ImportPathNone: the language has no file-to-import-path mapping, or one
	// that cannot be derived from the path alone. Files still index symbols.
	ImportPathNone ImportPathStyle = ""
	// ImportPathSlash: repo-relative path with the extension removed, and a
	// trailing index segment dropped, as TypeScript resolves "./dir".
	ImportPathSlash ImportPathStyle = "slash"
	// ImportPathDotted: repo-relative path with the extension removed and
	// separators replaced by dots, as Python and Java name modules.
	ImportPathDotted ImportPathStyle = "dotted"
	// ImportPathPSR4: PHP's composer autoload map. The nearest composer.json
	// maps a namespace prefix to a directory ("App\\": "app/"), so
	// app/Models/User.php is App\Models\User, as `use` statements name it.
	ImportPathPSR4 ImportPathStyle = "psr4"
	// ImportPathDeclared: the file names itself. ImportPathDeclared's
	// pattern, run over the file outside comments, captures path: C#'s
	// `namespace App.Models;`. Files declaring the same name share a node,
	// as a Go package's files do.
	ImportPathDeclared ImportPathStyle = "declared"
	// ImportPathRust: the crate module tree. src/lib.rs and src/main.rs are
	// the crate (named from Cargo.toml), src/a/b.rs and src/a/b/mod.rs are
	// crate::a::b under that name. `use crate::`, `self::` and `super::`
	// paths resolve to the deepest module file that exists.
	ImportPathRust ImportPathStyle = "rust"
	// ImportPathSPM: a Swift Package Manager target. Files under
	// Sources/<Target>/ (or Tests/<Target>/) next to a Package.swift are the
	// module <Target>, which `import Target` names; files of one module never
	// import each other, so the module is the node.
	ImportPathSPM ImportPathStyle = "spm"
	// ImportPathDart: the repository path. `import 'package:app/x.dart'`
	// resolves through the nearest pubspec.yaml naming that package to its
	// lib/x.dart, and a relative import to the importing file's directory.
	ImportPathDart ImportPathStyle = "dart"
	// ImportPathDirectory: the file's directory. A Terraform module is a
	// directory, which `source = "./modules/vpc"` names.
	ImportPathDirectory ImportPathStyle = "directory"
	// ImportPathInclude: the repository path, as C and C++ name a header.
	// `#include "x.h"` (a rule with relative) is looked for beside the
	// including file first; then both forms are looked for in each
	// directory from the including file's up to the root, and in its
	// include/ and src/. A name found nowhere is a system or third-party
	// header and keeps its spelling.
	ImportPathInclude ImportPathStyle = "include"
	// ImportPathStylesheet: the repository path, as CSS, Sass and Less name
	// a stylesheet. An @import, @use or @forward is resolved against the
	// importing file's directory the way Sass does: the path as written, with
	// each of the language's extensions, as a partial (_name), and as a
	// directory's _index or index. One found nowhere names a package
	// (bootstrap/scss/bootstrap, tailwindcss) and keeps its spelling, with
	// webpack's ~ dropped. A stylesheet a script imports (import
	// "./app.css") meets it under the same key.
	ImportPathStylesheet ImportPathStyle = "stylesheet"
)

// DepthStyle says how a language marks nesting, so a rule can ask to match
// only where a declaration is top-level.
type DepthStyle string

const (
	DepthNone   DepthStyle = "none"   // no usable nesting signal
	DepthBrace  DepthStyle = "brace"  // C family: { }
	DepthIndent DepthStyle = "indent" // Python family: leading whitespace
)

// SpecComments lists the comment delimiters of a language.
type SpecComments struct {
	Line  []string    `json:"line"`
	Block [][2]string `json:"block"`

	// NotLine lists sequences that start like a line comment but are code:
	// PHP's "#[" opens an attribute (#[Route("/x")]), while "#" alone is a
	// comment. Each must begin with one of Line.
	NotLine []string `json:"not_line"`
}

// SpecString is the JSON form of a StringRule. Escape is a string because a
// JSON document has no byte type; it must be empty or a single byte.
type SpecString struct {
	Open             string `json:"open"`
	Close            string `json:"close"`
	Escape           string `json:"escape"`
	Multiline        bool   `json:"multiline"`
	ContinueOnEscape bool   `json:"continue_on_escape"`
	InterpOpen       string `json:"interp_open"`
	InterpClose      string `json:"interp_close"`
}

// SpecRule matches one declaration or import on a blanked line.
//
// Requires is a plain substring that must appear in the line before the regex
// is run. It is not an optimization detail: matching every rule of every
// enabled language against every line is O(lines x rules), and this gate is
// what keeps a spec-driven scan from being slower than the hand-written
// parsers it replaces.
type SpecRule struct {
	Kind     SymbolKind `json:"kind"`
	Requires string     `json:"requires"`
	Pattern  string     `json:"pattern"`

	// TopLevel confines an import rule to lines outside any block. PHP's
	// `use HasFactory;` inside a class body is a trait, not an import.
	TopLevel bool `json:"top_level"`

	// KeepStrings matches a symbol rule against the view that keeps string
	// contents, for languages that name things with quoted labels:
	// Terraform's resource "aws_s3_bucket" "logs".
	KeepStrings bool `json:"keep_strings"`

	// Relative marks an import rule whose path is relative to the importing
	// file's directory, as Ruby's require_relative is: it is joined to that
	// directory and named the way ImportPath names a file.
	Relative bool `json:"relative"`
}

// Spec is a complete language description.
type Spec struct {
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`

	// Filenames are base names, or base-name globs, the language takes
	// whatever their extension: Dockerfile, Dockerfile.*, Makefile.
	Filenames []string `json:"filenames"`
	// ExcludeFilenames are base-name globs not taken even with one of the
	// language's extensions: pnpm-lock.yaml is generated, not written.
	ExcludeFilenames []string `json:"exclude_filenames"`

	// Graph "linked" keeps the language's files out of the dependency graph
	// unless an import links them: every YAML file as a lone node would bury
	// the code. Empty means every file is a node, as for code.
	Graph         string       `json:"graph"`
	DepthStyle    DepthStyle   `json:"depth_style"`
	Comments      SpecComments `json:"comments"`
	Strings       []SpecString `json:"strings"`
	RegexLiterals bool         `json:"regex_literals"`
	Symbols       []SpecRule   `json:"symbols"`
	Imports       []SpecRule   `json:"imports"`

	// ImportPath describes the inverse of the import rules: how one of this
	// language's files is named by the files that import it.
	ImportPath ImportPathStyle `json:"import_path"`

	// ImportPathIndexNames are file stems that stand for their directory, so
	// pkg/__init__.py is imported as "pkg" rather than "pkg.__init__".
	ImportPathIndexNames []string `json:"import_path_index_names"`

	// ImportPathStripPrefixes are repository path prefixes that are not part
	// of the import path. Java's src/main/java is the clearest case: the file
	// is at src/main/java/com/x/Foo.java and imported as com.x.Foo.
	ImportPathStripPrefixes []string `json:"import_path_strip_prefixes"`

	// ImportPathRootMarkers are files that mark a project root inside the
	// repository (pyproject.toml, setup.py). A file's import path is taken
	// relative to the nearest marked directory instead of the repository
	// root, which is what a monorepo of several projects needs.
	ImportPathRootMarkers []string `json:"import_path_root_markers"`

	// ImportPathRootSubdirs are source directories inside a marked project
	// ("src" for the src layout): a file under one is named relative to it.
	ImportPathRootSubdirs []string `json:"import_path_root_subdirs"`

	// ImportPathDeclaredPattern is the pattern for the declared style.
	ImportPathDeclaredPattern string `json:"import_path_declared"`

	// IgnoreNames are never symbols, whatever a rule matches, and a rule
	// that captures a type group is dropped when the type is one: C's
	// `return f(x);` has the shape of `int f(x);`. In Dart a
	// function has no keyword, so `if (x) {` has a function's shape.
	IgnoreNames []string `json:"ignore_names"`
}

// knownSymbolKinds is the closed set a spec may use. Rendering interpolates
// Kind verbatim, so an unchecked value would reach index notes and the MCP
// tools as-is; new kinds should be added here deliberately.
var knownSymbolKinds = []SymbolKind{KindFunc, KindMethod, KindType, KindConst, KindVar}

// LoadSpec parses and validates a JSONC spec.
func LoadSpec(data []byte) (Spec, error) {
	spec, err := decodeSpec(data)
	if err != nil {
		return Spec{}, err
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// decodeSpec parses and normalizes a spec without validating it. The
// embedded specs load this way: validating compiles every pattern, which is
// deferred to first use, and TestEmbeddedSpecsAreValid validates them all.
func decodeSpec(data []byte) (Spec, error) {
	var spec Spec
	if err := json.Unmarshal(stripJSONC(data), &spec); err != nil {
		return Spec{}, fmt.Errorf("parse spec: %w", err)
	}
	if spec.DepthStyle == "" {
		spec.DepthStyle = DepthNone
	}
	spec.Language = strings.ToLower(strings.TrimSpace(spec.Language))
	for i, ext := range spec.Extensions {
		spec.Extensions[i] = strings.ToLower(strings.TrimSpace(ext))
	}
	return spec, nil
}

// LoadSpecFile reads and parses a spec from disk.
func LoadSpecFile(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read spec: %w", err)
	}
	spec, err := LoadSpec(data)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

// Validate reports the first problem that would make a spec unusable. It is
// strict on purpose: a spec is loaded once and then applied to every file, so
// a mistake caught here is cheap and one caught later is a silently wrong
// index.
func (s Spec) Validate() error {
	if s.Language == "" {
		return fmt.Errorf("spec has no language name")
	}
	// Language counts are serialized as "name:count" joined by commas, so a
	// name carrying either separator would corrupt the index meta row.
	if strings.ContainsAny(s.Language, ":,") {
		return fmt.Errorf("language %q must not contain ':' or ','", s.Language)
	}
	// "auto" in index.languages means every language, so a spec of that name
	// could never be selected on its own.
	if strings.EqualFold(s.Language, AutoLanguages) {
		return fmt.Errorf("language %q is reserved", s.Language)
	}

	switch s.DepthStyle {
	case DepthNone, DepthBrace, DepthIndent:
	default:
		return fmt.Errorf("language %s: unknown depth_style %q (want none, brace or indent)", s.Language, s.DepthStyle)
	}

	switch s.ImportPath {
	case ImportPathNone, ImportPathSlash, ImportPathDotted, ImportPathPSR4, ImportPathRust, ImportPathSPM, ImportPathDart, ImportPathDirectory, ImportPathInclude, ImportPathStylesheet:
	case ImportPathDeclared:
		if s.ImportPathDeclaredPattern == "" || len(s.ImportPathDeclaredPattern) > MaxSpecPatternLen {
			return fmt.Errorf("language %s: import_path declared needs an import_path_declared pattern of at most %d bytes", s.Language, MaxSpecPatternLen)
		}
		re, err := regexp.Compile(s.ImportPathDeclaredPattern)
		if err != nil {
			return fmt.Errorf("language %s: import_path_declared does not compile: %w", s.Language, err)
		}
		if !hasGroup(re, "path") {
			return fmt.Errorf("language %s: import_path_declared must capture (?P<path>...)", s.Language)
		}
	default:
		return fmt.Errorf("language %s: unknown import_path %q (want slash, dotted, psr4, declared, rust, spm, dart, directory, include or stylesheet)", s.Language, s.ImportPath)
	}

	if len(s.Extensions) == 0 && len(s.Filenames) == 0 {
		return fmt.Errorf("language %s claims no extensions or filenames, so no file could select it", s.Language)
	}
	if len(s.Filenames) > MaxSpecExtensions || len(s.ExcludeFilenames) > MaxSpecExtensions {
		return fmt.Errorf("language %s: filenames and exclude_filenames are limited to %d each", s.Language, MaxSpecExtensions)
	}
	for _, g := range append(append([]string(nil), s.Filenames...), s.ExcludeFilenames...) {
		if g == "" || strings.Contains(g, "/") {
			return fmt.Errorf("language %s: filename %q must be a non-empty base name", s.Language, g)
		}
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("language %s: filename pattern %q: %w", s.Language, g, err)
		}
	}
	switch s.Graph {
	case "", "linked":
	default:
		return fmt.Errorf("language %s: unknown graph %q (want linked)", s.Language, s.Graph)
	}
	if len(s.Extensions) > MaxSpecExtensions {
		return fmt.Errorf("language %s: %d extensions exceeds the limit of %d", s.Language, len(s.Extensions), MaxSpecExtensions)
	}
	seenExt := make(map[string]bool, len(s.Extensions))
	for _, ext := range s.Extensions {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 {
			return fmt.Errorf("language %s: extension %q must start with '.'", s.Language, ext)
		}
		if seenExt[ext] {
			return fmt.Errorf("language %s: extension %q listed twice", s.Language, ext)
		}
		seenExt[ext] = true
	}

	if len(s.Strings) > MaxSpecStrings {
		return fmt.Errorf("language %s: %d string rules exceeds the limit of %d", s.Language, len(s.Strings), MaxSpecStrings)
	}
	for i, str := range s.Strings {
		if str.Open == "" || str.Close == "" {
			return fmt.Errorf("language %s: strings[%d] needs both open and close", s.Language, i)
		}
		if len(str.Escape) > 1 {
			return fmt.Errorf("language %s: strings[%d] escape %q must be a single byte", s.Language, i, str.Escape)
		}
		if (str.InterpOpen == "") != (str.InterpClose == "") {
			return fmt.Errorf("language %s: strings[%d] needs both interp_open and interp_close, or neither", s.Language, i)
		}
		// Interpolation is tracked by counting braces, so a different closer
		// would never be matched and the string would swallow the rest of the
		// file.
		if str.InterpClose != "" && str.InterpClose != "}" {
			return fmt.Errorf("language %s: strings[%d] interp_close must be \"}\"", s.Language, i)
		}
	}

	for _, pair := range s.Comments.Block {
		if pair[0] == "" || pair[1] == "" {
			return fmt.Errorf("language %s: block comment needs both delimiters", s.Language)
		}
	}
	for _, m := range s.Comments.Line {
		if m == "" {
			return fmt.Errorf("language %s: empty line comment delimiter would blank every line", s.Language)
		}
	}
	if len(s.Comments.NotLine) > MaxSpecStrings {
		return fmt.Errorf("language %s: %d not_line entries exceeds the limit of %d", s.Language, len(s.Comments.NotLine), MaxSpecStrings)
	}
	for _, m := range s.Comments.NotLine {
		opens := false
		for _, line := range s.Comments.Line {
			opens = opens || (line != "" && strings.HasPrefix(m, line) && len(m) > len(line))
		}
		if !opens {
			return fmt.Errorf("language %s: not_line %q must extend a line comment delimiter", s.Language, m)
		}
	}

	if n := len(s.Symbols) + len(s.Imports); n > MaxSpecRules {
		return fmt.Errorf("language %s: %d rules exceeds the limit of %d", s.Language, n, MaxSpecRules)
	}
	if len(s.Symbols) == 0 && len(s.Imports) == 0 {
		return fmt.Errorf("language %s has no symbol or import rules, so it would index nothing", s.Language)
	}
	for i, rule := range s.Symbols {
		if err := rule.validate(s.Language, fmt.Sprintf("symbols[%d]", i), "name"); err != nil {
			return err
		}
		if !validSymbolKind(rule.Kind) {
			return fmt.Errorf("language %s: symbols[%d] has unknown kind %q (want one of %s)",
				s.Language, i, rule.Kind, kindList())
		}
	}
	for i, rule := range s.Imports {
		if err := rule.validate(s.Language, fmt.Sprintf("imports[%d]", i), "path"); err != nil {
			return err
		}
		if rule.Kind != "" {
			return fmt.Errorf("language %s: imports[%d] must not set a kind", s.Language, i)
		}
	}
	return nil
}

// validate checks one rule, requiring the capture group the caller will read.
func (r SpecRule) validate(language, where, group string) error {
	if r.Pattern == "" {
		return fmt.Errorf("language %s: %s has no pattern", language, where)
	}
	if len(r.Pattern) > MaxSpecPatternLen {
		return fmt.Errorf("language %s: %s pattern is %d bytes, over the limit of %d",
			language, where, len(r.Pattern), MaxSpecPatternLen)
	}
	re, err := regexp.Compile(r.Pattern)
	if err != nil {
		return fmt.Errorf("language %s: %s pattern does not compile: %w", language, where, err)
	}
	if !hasGroup(re, group) {
		return fmt.Errorf("language %s: %s pattern must capture (?P<%s>...)", language, where, group)
	}
	// Requires is not checked for consistency with Pattern: the gate is a
	// literal matched against the line and the pattern is a regex, so a rule
	// whose gate can never appear in its own matches is a silent no-op that
	// validation cannot distinguish from a deliberately broad gate.
	if r.Requires != "" && len(r.Requires) > MaxSpecPatternLen {
		return fmt.Errorf("language %s: %s requires is too long", language, where)
	}
	return nil
}

func hasGroup(re *regexp.Regexp, name string) bool {
	for _, n := range re.SubexpNames() {
		if n == name {
			return true
		}
	}
	return false
}

func validSymbolKind(k SymbolKind) bool {
	for _, known := range knownSymbolKinds {
		if k == known {
			return true
		}
	}
	return false
}

func kindList() string {
	parts := make([]string, 0, len(knownSymbolKinds))
	for _, k := range knownSymbolKinds {
		parts = append(parts, string(k))
	}
	return strings.Join(parts, ", ")
}

// BlankSpec builds the blanking configuration this language needs.
func (s Spec) BlankSpec() BlankSpec {
	out := BlankSpec{
		LineComments:    append([]string(nil), s.Comments.Line...),
		NotLineComments: append([]string(nil), s.Comments.NotLine...),
		BlockComments:   append([][2]string(nil), s.Comments.Block...),
		RegexLiterals:   s.RegexLiterals,
	}
	for _, str := range s.Strings {
		rule := StringRule{
			Open:             str.Open,
			Close:            str.Close,
			Multiline:        str.Multiline,
			ContinueOnEscape: str.ContinueOnEscape,
			InterpOpen:       str.InterpOpen,
			InterpClose:      str.InterpClose,
		}
		if str.Escape != "" {
			rule.Escape = str.Escape[0]
		}
		out.Strings = append(out.Strings, rule)
	}
	return out
}
