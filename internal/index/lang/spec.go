package lang

import (
	"encoding/json"
	"fmt"
	"os"
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
}

// Spec is a complete language description.
type Spec struct {
	Language      string       `json:"language"`
	Extensions    []string     `json:"extensions"`
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
}

// knownSymbolKinds is the closed set a spec may use. Rendering interpolates
// Kind verbatim, so an unchecked value would reach index notes and the MCP
// tools as-is; new kinds should be added here deliberately.
var knownSymbolKinds = []SymbolKind{KindFunc, KindMethod, KindType, KindConst, KindVar}

// LoadSpec parses and validates a JSONC spec.
func LoadSpec(data []byte) (Spec, error) {
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
	if err := spec.Validate(); err != nil {
		return Spec{}, err
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

	switch s.DepthStyle {
	case DepthNone, DepthBrace, DepthIndent:
	default:
		return fmt.Errorf("language %s: unknown depth_style %q (want none, brace or indent)", s.Language, s.DepthStyle)
	}

	switch s.ImportPath {
	case ImportPathNone, ImportPathSlash, ImportPathDotted:
	default:
		return fmt.Errorf("language %s: unknown import_path %q (want slash or dotted)", s.Language, s.ImportPath)
	}

	if len(s.Extensions) == 0 {
		return fmt.Errorf("language %s claims no extensions, so no file could select it", s.Language)
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
		LineComments:  append([]string(nil), s.Comments.Line...),
		BlockComments: append([][2]string(nil), s.Comments.Block...),
		RegexLiterals: s.RegexLiterals,
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
