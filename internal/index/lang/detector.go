package lang

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// A facet is the role a piece of code plays, named independently of the
// framework that gave it that role. This is what makes the concept worth
// having: Django models, GORM structs, Entity Framework entities, Eloquent
// models and SQLAlchemy classes are all "model", and chi, Flask, FastAPI,
// Spring and Laravel routes are all "route". An agent can ask "every HTTP
// route in this repository" without knowing which frameworks are in it.
const (
	FacetRoute     = "route"
	FacetModel     = "model"
	FacetComponent = "component"
	FacetMigration = "migration"
	FacetJob       = "job"
	FacetTest      = "test"

	// Added with framework packs. A controller groups routes (Laravel, Nest,
	// Symfony); a service is an injectable or provider (Nest, Angular,
	// Laravel); a schema validates or shapes data without being stored
	// (pydantic, zod, DTOs), which is what tells it apart from a model.
	FacetController = "controller"
	FacetMiddleware = "middleware"
	FacetService    = "service"
	FacetSchema     = "schema"
	// A named entry point invoked by name rather than by URL: a console
	// command (Artisan, Symfony, cobra, Django management), an IPC handler
	// (Electron's ipcMain.handle), a Tauri command.
	FacetCommand = "command"
	// Infrastructure a project deploys or builds with: a container image or
	// build stage, a compose service, a CI job, a Kubernetes object, a
	// Terraform resource or module.
	FacetInfra = "infra"
	// What a styling framework is configured with: a Tailwind theme token,
	// custom utility or component class, a Bootstrap or Bulma variable
	// override, where a CSS framework is imported.
	FacetStyle = "style"
)

// knownFacets is the closed set a detector may emit. Facet names reach users
// through a query interface, so an open set would turn a typo into a facet
// nobody can find.
var knownFacets = []string{
	FacetRoute, FacetModel, FacetComponent, FacetMigration, FacetJob, FacetTest,
	FacetController, FacetMiddleware, FacetService, FacetSchema, FacetCommand, FacetInfra, FacetStyle,
}

// KnownFacets returns the facets a detector may emit, in display order.
func KnownFacets() []string { return append([]string(nil), knownFacets...) }

// Limits, applied to every detector for the same reason spec limits are.
const (
	MaxDetectorRules    = 32
	MaxDetectorImports  = 32
	MaxDetectorPrefixes = 8

	// maxLookahead bounds DetectorRule.Lookahead: enough for a stack of
	// decorators, not enough to reach an unrelated declaration.
	maxLookahead = 8

	// maxStatementLines bounds how far a statement is joined for detection.
	// Decorators and route registrations span a few lines; Angular's
	// @Component metadata can run to dozens. A paren still open past this was
	// never balanced, and joining further would drag in unrelated code.
	maxStatementLines = 64
)

// DetectorRule matches a statement, a file path, or both, and names what it
// found.
//
// Rules match statements rather than symbols because most of what is worth
// finding is not a declaration. `r.Get("/users", h)` is a call, a Django
// urlpattern is a list entry, and a migration is a file rather than a
// definition. Attaching facets to symbols would have covered models and missed
// routes entirely.
//
// A statement is a line plus the lines that follow it while a parenthesis it
// opened is still open, joined with newlines. Patterns are compiled in
// multi-line mode, so `^` and `$` still mean a line, `\s*` crosses into the next
// one, and a decorator written over several lines matches like one written on
// a single line. Each match is reported at the line it starts on.
type DetectorRule struct {
	Facet    string `json:"facet"`
	Requires string `json:"requires"`
	Pattern  string `json:"pattern"`

	// KeepStrings matches against the view that retains string contents.
	// Route paths and struct tags live inside string literals, so a rule
	// looking for one must see them; rules matching code shape should not,
	// since then a quoted example could masquerade as the real thing.
	KeepStrings bool `json:"keep_strings"`

	// RequiresAny is a gate of alternatives, checked like Requires: the line
	// the match starts on must contain at least one. It lets one rule cover
	// .get( .post( .put( without dropping the gate that keeps a route-shaped
	// string from counting.
	RequiresAny []string `json:"requires_any"`

	// Within confines the rule to statements whose code view contains this,
	// for things that are only meaningful inside one call: the keys of the
	// routes object in Bun.serve({ routes: { "/x": ... } }).
	Within string `json:"within"`

	// Detail is the detail of a match that captured none: what kind of
	// infrastructure a compose service or a build stage is.
	Detail string `json:"detail"`

	// Inside confines the rule to the block indented under a line matching
	// this pattern (in the code view): a compose file's services are the
	// keys under `services:`. With InsideChildren, only the block's direct
	// children count, at the indentation of its first line, so a service's
	// own `image:` key is not another service.
	Inside         string `json:"inside"`
	InsideChildren bool   `json:"inside_children"`

	// NameExclude drops a match whose name matches it: CodeIgniter 3 keeps
	// settings (default_controller, 404_override) in the same $route array as
	// its routes.
	NameExclude string `json:"name_exclude"`

	// PathExclude skips files whose repo-relative path matches it, for
	// conventions that carve an exception out of a broad rule: every .vue
	// file is a component, except the pages a router names.
	PathExclude string `json:"path_exclude"`

	// Lookahead extends the matched text over this many following non-blank
	// statements (at most maxLookahead). A decorator and the class it
	// decorates are separate statements -- @Component({...}) closes its
	// parenthesis before `export class X` begins -- so naming a decorated
	// class needs both.
	Lookahead int `json:"lookahead"`

	// PathPattern confines the rule to files whose repo-relative path (with
	// forward slashes) matches it, for conventions that live in the file
	// system: Next.js app/**/page.tsx, a Laravel migration directory. When it
	// captures name, a match is named from the path, which is how a file-based
	// route gets its URL; detail can come from the path too. Without Pattern,
	// a matching file yields one facet, at line 1.
	PathPattern string `json:"path_pattern"`

	// NameStyle "route" normalizes a name built from a path as a URL path:
	// route groups "(x)" and parallel slots "@x" are dropped, a final "index"
	// names its directory, and the result starts with "/". NameStyle "flat"
	// reads React Router / Remix flat-route file names, where a dot separates
	// segments: users.$id.edit is /users/:id/edit (see flatRoute).
	NameStyle string `json:"name_style"`
}

// PrefixRule captures a route prefix declared in the same file, joined onto
// the name of every route found after it.
//
// With a var group it is a router variable's prefix (FastAPI's
// `router = APIRouter(prefix="/items")`), applied to routes whose own var
// group names that variable. Without one it is a class-level prefix (Nest's
// @Controller("users"), Symfony's class #[Route]), applied to every route
// after it until the next. A prefix set in another file -- Express's
// app.use("/api", router) -- is not followed; those routes keep their own
// path.
//
// A var-scoped prefix may also capture parent, the variable it was derived
// from: Gin's v1 := api.Group("/v1") under api := r.Group("/api") is
// /api/v1. The parent must be declared earlier in the file.
//
// With "scope": "statement" it applies only inside the call it was found in:
// Laravel's Route::prefix("admin")->group(function () { ... }) and
// CodeIgniter's $routes->group("admin", ...) reach the routes inside their
// closure, which sits inside the call's parentheses, and no further. Nested
// groups join, outermost first. "block" reaches the routes inside the braces
// opened on the prefix's line (Ktor's route("/api") { get("/users") }), and
// "indent" the lines after it indented deeper (Rails' namespace :admin do).
type PrefixRule struct {
	Requires    string `json:"requires"`
	Pattern     string `json:"pattern"`
	KeepStrings bool   `json:"keep_strings"`
	Scope       string `json:"scope"`

	// NameStyle "segments" reads the prefix as quoted path segments, as
	// Vapor writes them: grouped("api", "v1") is /api/v1.
	NameStyle string `json:"name_style"`

	// Lookahead, as for rules: Symfony's class-level #[Route("/blog")] is a
	// prefix only when the statement after it declares the class.
	Lookahead int `json:"lookahead"`
}

// Detector is a framework recognized in one or more languages.
type Detector struct {
	Framework string   `json:"framework"`
	Languages []string `json:"languages"`

	// The gates: at least one must hold for the detector to run on a file.
	//
	// Without a gate, a pattern like `class \w+\(.*Model\)` would claim every
	// ORM ever written, and attributing it to the wrong framework is worse
	// than not detecting it.
	//
	// WhenImports holds when the file imports one of these. A trailing "*"
	// makes it a prefix match.
	WhenImports []string `json:"when_imports"`

	// WhenDependencies holds when a manifest at or above the file declares
	// one of these: see ManifestFiles. It is the gate for frameworks
	// whose files need not import them: React since the automatic JSX
	// runtime, Nuxt's auto-imports, Next.js pages. Same matching as
	// WhenImports.
	WhenDependencies []string `json:"when_dependencies"`

	// PathGlobs holds when the file's path matches, for frameworks whose
	// convention is a location rather than an import. A glob may match at any
	// directory boundary, so "app/**/page.tsx" also finds
	// apps/web/app/x/page.tsx in a monorepo; "**" spans directories, "*" and
	// "?" stay within one, and {a,b} is an alternative.
	PathGlobs []string `json:"path_globs"`

	// UnlessImports vetoes the detector for a file importing any of these,
	// whatever the gates say. Go's net/http and the routers built on it share
	// .HandleFunc("/x", h); a chi or gorilla file is reported by its own
	// detector, not twice.
	UnlessImports []string `json:"unless_imports"`

	Prefixes []PrefixRule   `json:"prefixes"`
	Rules    []DetectorRule `json:"rules"`
}

// Detected is one facet found in a file.
type Detected struct {
	FilePath  string
	Facet     string
	Framework string
	Name      string
	Detail    string
	Line      int
}

// compiledDetector is a Detector with its patterns compiled.
type compiledDetector struct {
	framework        string
	languages        map[string]bool
	whenImports      []string
	whenDependencies []string
	unlessImports    []string
	pathGlobs        []compiledGlob
	prefixes         []compiledPrefix
	rules            []compiledDetectorRule
}

type compiledDetectorRule struct {
	facet       string
	requires    string
	requiresAny []string
	within      string
	lookahead   int
	re          *regexp.Regexp // nil for a rule that matches a path only
	nameIdx     int
	detailIdx   int
	varIdx      int
	keepStrings bool
	pathRe      *regexp.Regexp
	pathExclude *regexp.Regexp
	nameExclude *regexp.Regexp
	inside      *regexp.Regexp
	children    bool
	detail      string
	pathNameIdx int
	pathDetIdx  int
	routeStyle  bool
	flatStyle   bool
	segStyle    bool
}

type compiledPrefix struct {
	requires    string
	re          *regexp.Regexp
	prefixIdx   int
	varIdx      int
	parentIdx   int
	keepStrings bool
	statement   bool
	scope       string
	segments    bool
	lookahead   int
}

// DetectorSet is a collection of detectors, each compiled on first use.
type DetectorSet struct {
	detectors []*lazyDetector
	// byLanguage lists, for each language a detector names, the detectors
	// that can apply to it (those naming it and those naming none), in
	// declaration order. A Go file never looks at a TypeScript detector.
	byLanguage  map[string][]int
	anyLanguage []int
}

// lazyDetector compiles its detector the first time a file of one of its
// languages is checked. Compiling every embedded detector's patterns was the
// largest single cost of the post-commit hook, which usually touches one or
// two languages out of twenty.
type lazyDetector struct {
	def  Detector
	once sync.Once
	cd   compiledDetector
	err  error
}

func (l *lazyDetector) compiled() (*compiledDetector, error) {
	l.once.Do(func() { l.cd, l.err = compileDetector(l.def) })
	return &l.cd, l.err
}

// LoadDetector parses and validates a JSONC detector definition.
func LoadDetector(data []byte) (Detector, error) {
	d, err := decodeDetector(data)
	if err != nil {
		return Detector{}, err
	}
	if err := d.Validate(); err != nil {
		return Detector{}, err
	}
	return d, nil
}

// decodeDetector parses a definition without compiling it. The embedded
// detectors load this way and compile on first use: a test compiles every
// one of them, so an invalid one cannot ship.
func decodeDetector(data []byte) (Detector, error) {
	var d Detector
	if err := json.Unmarshal(stripJSONC(data), &d); err != nil {
		return Detector{}, fmt.Errorf("parse detector: %w", err)
	}
	d.Framework = strings.ToLower(strings.TrimSpace(d.Framework))
	for i, l := range d.Languages {
		d.Languages[i] = strings.ToLower(strings.TrimSpace(l))
	}
	return d, nil
}

// Validate reports the first problem that would make a detector unusable or
// misleading.
func (d Detector) Validate() error {
	_, err := compileDetector(d)
	return err
}

func validFacet(f string) bool {
	for _, known := range knownFacets {
		if f == known {
			return true
		}
	}
	return false
}

// compilePattern compiles a detector regexp in multi-line mode; see
// DetectorRule for why.
func compilePattern(framework, where, pattern string) (*regexp.Regexp, error) {
	if len(pattern) > MaxSpecPatternLen {
		return nil, fmt.Errorf("framework %s: %s pattern is over the limit of %d", framework, where, MaxSpecPatternLen)
	}
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("framework %s: %s pattern does not compile: %w", framework, where, err)
	}
	return re, nil
}

// compileDetector validates d and compiles it. Validation and compilation are
// one step so the two cannot disagree about what a detector means.
func compileDetector(d Detector) (compiledDetector, error) {
	cd := compiledDetector{
		framework:        d.Framework,
		whenImports:      d.WhenImports,
		whenDependencies: d.WhenDependencies,
		unlessImports:    d.UnlessImports,
		pathGlobs:        compileGlobs(d.PathGlobs),
	}
	if d.Framework == "" {
		return cd, fmt.Errorf("detector has no framework name")
	}
	if strings.ContainsAny(d.Framework, ":,") {
		return cd, fmt.Errorf("framework %q must not contain ':' or ','", d.Framework)
	}
	if len(d.WhenImports) == 0 && len(d.WhenDependencies) == 0 && len(d.PathGlobs) == 0 {
		return cd, fmt.Errorf("framework %s has no when_imports, when_dependencies or path_globs, so it would claim every file", d.Framework)
	}
	if len(d.WhenImports) > MaxDetectorImports {
		return cd, fmt.Errorf("framework %s: %d when_imports exceeds the limit of %d", d.Framework, len(d.WhenImports), MaxDetectorImports)
	}
	if len(d.WhenDependencies) > MaxDetectorImports {
		return cd, fmt.Errorf("framework %s: %d when_dependencies exceeds the limit of %d", d.Framework, len(d.WhenDependencies), MaxDetectorImports)
	}
	if len(d.UnlessImports) > MaxDetectorImports {
		return cd, fmt.Errorf("framework %s: %d unless_imports exceeds the limit of %d", d.Framework, len(d.UnlessImports), MaxDetectorImports)
	}
	if len(d.PathGlobs) > MaxDetectorImports {
		return cd, fmt.Errorf("framework %s: %d path_globs exceeds the limit of %d", d.Framework, len(d.PathGlobs), MaxDetectorImports)
	}
	if len(d.Rules) == 0 {
		return cd, fmt.Errorf("framework %s has no rules", d.Framework)
	}
	if len(d.Rules) > MaxDetectorRules {
		return cd, fmt.Errorf("framework %s: %d rules exceeds the limit of %d", d.Framework, len(d.Rules), MaxDetectorRules)
	}
	if len(d.Prefixes) > MaxDetectorPrefixes {
		return cd, fmt.Errorf("framework %s: %d prefixes exceeds the limit of %d", d.Framework, len(d.Prefixes), MaxDetectorPrefixes)
	}
	if len(d.Languages) > 0 {
		cd.languages = make(map[string]bool, len(d.Languages))
		for _, l := range d.Languages {
			cd.languages[l] = true
		}
	}

	for i, p := range d.Prefixes {
		where := fmt.Sprintf("prefixes[%d]", i)
		if p.Pattern == "" {
			return cd, fmt.Errorf("framework %s: %s has no pattern", d.Framework, where)
		}
		// The same reason as for rules: the gate, checked against the code
		// view, is what keeps a prefix-shaped string from counting.
		if p.KeepStrings && p.Requires == "" {
			return cd, fmt.Errorf("framework %s: %s sets keep_strings so it must set requires", d.Framework, where)
		}
		re, err := compilePattern(d.Framework, where, p.Pattern)
		if err != nil {
			return cd, err
		}
		if !hasGroup(re, "prefix") {
			return cd, fmt.Errorf("framework %s: %s pattern must capture (?P<prefix>...)", d.Framework, where)
		}
		switch p.Scope {
		case "", "statement", "block", "indent":
		default:
			return cd, fmt.Errorf("framework %s: %s has unknown scope %q (want statement, block or indent)", d.Framework, where, p.Scope)
		}
		if p.Scope != "" && hasGroup(re, "var") {
			return cd, fmt.Errorf("framework %s: %s is %s-scoped, so it cannot also belong to a variable", d.Framework, where, p.Scope)
		}
		switch p.NameStyle {
		case "", "segments":
		default:
			return cd, fmt.Errorf("framework %s: %s has unknown name_style %q (want segments)", d.Framework, where, p.NameStyle)
		}
		if p.Lookahead < 0 || p.Lookahead > maxLookahead {
			return cd, fmt.Errorf("framework %s: %s lookahead must be 0 to %d", d.Framework, where, maxLookahead)
		}
		cd.prefixes = append(cd.prefixes, compiledPrefix{
			requires:    p.Requires,
			re:          re,
			prefixIdx:   re.SubexpIndex("prefix"),
			varIdx:      re.SubexpIndex("var"),
			parentIdx:   re.SubexpIndex("parent"),
			keepStrings: p.KeepStrings,
			statement:   p.Scope != "",
			scope:       p.Scope,
			segments:    p.NameStyle == "segments",
			lookahead:   p.Lookahead,
		})
	}

	for i, rule := range d.Rules {
		where := fmt.Sprintf("rules[%d]", i)
		if !validFacet(rule.Facet) {
			return cd, fmt.Errorf("framework %s: %s has unknown facet %q (want one of %s)",
				d.Framework, where, rule.Facet, strings.Join(knownFacets, ", "))
		}
		if rule.Pattern == "" && rule.PathPattern == "" {
			return cd, fmt.Errorf("framework %s: %s has no pattern or path_pattern", d.Framework, where)
		}
		// Without a gate checked against the code view, a keep_strings rule
		// would match its own shape inside any string literal.
		if rule.KeepStrings && rule.Requires == "" && len(rule.RequiresAny) == 0 {
			return cd, fmt.Errorf("framework %s: %s sets keep_strings so it must set requires or requires_any, "+
				"which is what confines it to real code", d.Framework, where)
		}
		if rule.Lookahead < 0 || rule.Lookahead > maxLookahead {
			return cd, fmt.Errorf("framework %s: %s lookahead must be 0 to %d", d.Framework, where, maxLookahead)
		}
		if len(rule.RequiresAny) > MaxDetectorImports {
			return cd, fmt.Errorf("framework %s: %s has more than %d requires_any", d.Framework, where, MaxDetectorImports)
		}
		for _, alt := range rule.RequiresAny {
			if alt == "" {
				return cd, fmt.Errorf("framework %s: %s has an empty requires_any entry, which would pass every line", d.Framework, where)
			}
		}
		switch rule.NameStyle {
		case "", "route", "flat", "segments":
		default:
			return cd, fmt.Errorf("framework %s: %s has unknown name_style %q (want route, flat or segments)", d.Framework, where, rule.NameStyle)
		}
		cr := compiledDetectorRule{
			facet:       rule.Facet,
			requires:    rule.Requires,
			requiresAny: rule.RequiresAny,
			within:      rule.Within,
			lookahead:   rule.Lookahead,
			nameIdx:     -1,
			detailIdx:   -1,
			varIdx:      -1,
			pathNameIdx: -1,
			pathDetIdx:  -1,
			keepStrings: rule.KeepStrings,
			routeStyle:  rule.NameStyle == "route",
			detail:      rule.Detail,
			flatStyle:   rule.NameStyle == "flat",
			segStyle:    rule.NameStyle == "segments",
		}
		if rule.Pattern != "" {
			re, err := compilePattern(d.Framework, where, rule.Pattern)
			if err != nil {
				return cd, err
			}
			cr.re = re
			cr.nameIdx = re.SubexpIndex("name")
			cr.detailIdx = re.SubexpIndex("detail")
			cr.varIdx = re.SubexpIndex("var")
		}
		if rule.PathPattern != "" {
			if len(rule.PathPattern) > MaxSpecPatternLen {
				return cd, fmt.Errorf("framework %s: %s path_pattern is over the limit of %d", d.Framework, where, MaxSpecPatternLen)
			}
			re, err := regexp.Compile(rule.PathPattern)
			if err != nil {
				return cd, fmt.Errorf("framework %s: %s path_pattern does not compile: %w", d.Framework, where, err)
			}
			cr.pathRe = re
			cr.pathNameIdx = re.SubexpIndex("name")
			cr.pathDetIdx = re.SubexpIndex("detail")
		}
		if rule.InsideChildren && rule.Inside == "" {
			return cd, fmt.Errorf("framework %s: %s sets inside_children without inside", d.Framework, where)
		}
		if rule.Inside != "" {
			re, err := compilePattern(d.Framework, where+" inside", rule.Inside)
			if err != nil {
				return cd, err
			}
			cr.inside, cr.children = re, rule.InsideChildren
		}
		if rule.NameExclude != "" {
			if len(rule.NameExclude) > MaxSpecPatternLen {
				return cd, fmt.Errorf("framework %s: %s name_exclude is over the limit of %d", d.Framework, where, MaxSpecPatternLen)
			}
			re, err := regexp.Compile(rule.NameExclude)
			if err != nil {
				return cd, fmt.Errorf("framework %s: %s name_exclude does not compile: %w", d.Framework, where, err)
			}
			cr.nameExclude = re
		}
		if rule.PathExclude != "" {
			if len(rule.PathExclude) > MaxSpecPatternLen {
				return cd, fmt.Errorf("framework %s: %s path_exclude is over the limit of %d", d.Framework, where, MaxSpecPatternLen)
			}
			re, err := regexp.Compile(rule.PathExclude)
			if err != nil {
				return cd, fmt.Errorf("framework %s: %s path_exclude does not compile: %w", d.Framework, where, err)
			}
			cr.pathExclude = re
		}
		if cr.nameIdx < 0 && cr.pathNameIdx < 0 {
			return cd, fmt.Errorf("framework %s: %s must capture (?P<name>...) in pattern or path_pattern", d.Framework, where)
		}
		cd.rules = append(cd.rules, cr)
	}
	return cd, nil
}

// NewDetectorSet compiles detectors for use during a scan.
func NewDetectorSet(detectors []Detector) (*DetectorSet, error) {
	set := newLazyDetectorSet(detectors)
	for _, d := range set.detectors {
		if _, err := d.compiled(); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// newLazyDetectorSet indexes detectors by language without compiling them.
func newLazyDetectorSet(detectors []Detector) *DetectorSet {
	set := &DetectorSet{byLanguage: map[string][]int{}}
	for _, d := range detectors {
		set.detectors = append(set.detectors, &lazyDetector{def: d})
	}
	for i, d := range set.detectors {
		if len(d.def.Languages) == 0 {
			set.anyLanguage = append(set.anyLanguage, i)
		}
		for _, l := range d.def.Languages {
			set.byLanguage[l] = nil
		}
	}
	for l := range set.byLanguage {
		var idx []int
		for i, d := range set.detectors {
			if len(d.def.Languages) == 0 || slices.Contains(d.def.Languages, l) {
				idx = append(idx, i)
			}
		}
		set.byLanguage[l] = idx
	}
	return set
}

// CompileAll compiles every detector in the set and returns the errors, for
// the test that keeps an invalid embedded detector from shipping.
func (s *DetectorSet) CompileAll() []error {
	var errs []error
	for _, d := range s.detectors {
		if _, err := d.compiled(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// forLanguage returns the detectors that can apply to a language.
func (s *DetectorSet) forLanguage(language string) []int {
	if idx, ok := s.byLanguage[language]; ok {
		return idx
	}
	return s.anyLanguage
}

// Empty reports whether there is nothing to detect, so callers can skip the
// work of producing blanked views.
func (s *DetectorSet) Empty() bool { return s == nil || len(s.detectors) == 0 }

// DetectInput is everything detection needs about one file.
type DetectInput struct {
	FilePath string
	Language string
	Imports  []Import

	// Dependencies are the packages declared by manifests at or above the
	// file; see Manifests.
	Dependencies []string

	// Code has string bodies emptied; Strings keeps them. Rules choose per
	// rule which view they match against. Both have one entry per line.
	Code    []string
	Strings []string

	// LoadDependencies and LoadViews, when set, supply Dependencies and the
	// two views on first need. Gates are checked first, cheapest first, so a
	// file no detector applies to -- most files -- is never blanked for
	// detection and its manifests are never read.
	LoadDependencies func() []string
	LoadViews        func() (code, strs []string)
}

// statement is a span of lines detection treats as one unit.
type statement struct {
	first, last int
	code, str   string // the lines joined with "\n"
}

// lineAt returns the 0-based line of a byte offset into s.code or s.str.
// Blanking preserves one line per line, so the count is the same in both.
func (s statement) lineAt(text string, offset int) int {
	return s.first + strings.Count(text[:offset], "\n")
}

// statements splits a file into statements: a line, plus the lines after it
// while a parenthesis it opened is still open. Parentheses are counted in the
// code view, where blanking has already removed those in strings and comments.
func statements(in DetectInput) []statement {
	var out []statement
	n := len(in.Code)
	if len(in.Strings) < n {
		n = len(in.Strings)
	}
	for i := 0; i < n; i++ {
		last := i
		if depth := parenDelta(in.Code[i]); depth > 0 {
			limit := i + maxStatementLines - 1
			if limit > n-1 {
				limit = n - 1
			}
			for j := i + 1; j <= limit; j++ {
				depth += parenDelta(in.Code[j])
				if depth <= 0 {
					last = j
					break
				}
			}
			// Never closed within the limit: the line stands alone, and the
			// lines after it are read as statements of their own.
		}
		out = append(out, statement{
			first: i,
			last:  last,
			code:  strings.Join(in.Code[i:last+1], "\n"),
			str:   strings.Join(in.Strings[i:last+1], "\n"),
		})
		i = last
	}
	return out
}

// Detect runs every applicable detector over a file.
func (s *DetectorSet) Detect(in DetectInput) []Detected {
	if s.Empty() {
		return nil
	}
	var found []Detected
	// Two rules can legitimately describe the same thing -- a GORM model is
	// named both by its TableName method and by its AutoMigrate registration
	// -- and reporting it twice reads as noise in a listing. Detail is part of
	// the key, so two routes sharing a path but differing in HTTP method stay
	// separate.
	type facetKey struct{ facet, framework, name, detail string }
	seen := make(map[facetKey]bool)
	add := func(det Detected) {
		key := facetKey{det.Facet, det.Framework, det.Name, det.Detail}
		if det.Name == "" || seen[key] {
			return
		}
		seen[key] = true
		found = append(found, det)
	}

	// The gates first: which detectors apply, loading dependencies only if
	// a detector gets as far as asking for them.
	depsLoaded := in.LoadDependencies == nil
	deps := func() []string {
		if !depsLoaded {
			in.Dependencies, depsLoaded = in.LoadDependencies(), true
		}
		return in.Dependencies
	}
	var applicable []*compiledDetector
	for _, i := range s.forLanguage(in.Language) {
		d, err := s.detectors[i].compiled()
		if err != nil {
			// Only reachable for an embedded detector, and the test that
			// compiles all of them keeps one from shipping. Skipping it
			// leaves the rest of the file's detection intact.
			continue
		}
		if d.applies(in, deps) {
			applicable = append(applicable, d)
		}
	}
	if len(applicable) == 0 {
		return nil
	}
	if in.Code == nil && in.LoadViews != nil {
		in.Code, in.Strings = in.LoadViews()
	}

	var stmts []statement // built on first use
	for _, d := range applicable {
		if stmts == nil {
			stmts = statements(in)
		}
		prefixes := d.findPrefixes(in, stmts)

		for _, rule := range d.rules {
			if rule.pathExclude != nil && rule.pathExclude.MatchString(in.FilePath) {
				continue
			}
			var pathName, pathDetail string
			if rule.pathRe != nil {
				m := rule.pathRe.FindStringSubmatch(in.FilePath)
				if m == nil {
					continue
				}
				if rule.pathNameIdx >= 0 {
					pathName = m[rule.pathNameIdx]
				}
				if rule.pathDetIdx >= 0 {
					pathDetail = m[rule.pathDetIdx]
				}
				if rule.re == nil {
					detail := pathDetail
					if detail == "" {
						detail = rule.detail
					}
					if rule.facet == FacetRoute {
						detail = normalizeMethod(detail)
					}
					add(Detected{
						FilePath:  in.FilePath,
						Facet:     rule.facet,
						Framework: d.framework,
						Name:      rule.styleName(pathName),
						Detail:    detail,
						Line:      1,
					})
					continue
				}
			}

			sections := rule.sections(in.Code)
			if rule.inside != nil && len(sections) == 0 {
				continue
			}
			for k, st := range stmts {
				if rule.within != "" && !strings.Contains(st.code, rule.within) {
					continue
				}
				if rule.lookahead > 0 {
					st = extendStatement(in, stmts, k, rule.lookahead)
				}
				text := st.code
				if rule.keepStrings {
					text = st.str
				}
				// All matches, not just the first: one statement can name
				// several things worth recording, as AutoMigrate(&User{},
				// &Post{}) does, and reporting only the first would silently
				// drop the rest.
				for _, m := range rule.re.FindAllStringSubmatchIndex(text, -1) {
					line := st.lineAt(text, m[0])
					// A match beginning in a statement the lookahead joined
					// belongs to that statement, whose own window reaches
					// further: from here it can be cut short. Crow's
					// CROW_ROUTE(...) followed by .methods(...) on the next
					// line was reported a second time, as GET, from the
					// statement before it.
					if line > stmts[k].last {
						continue
					}
					// The gate is checked against the code view of the lines
					// the match spans, even when the pattern matches the
					// string-preserving view. That is what keeps a
					// route-shaped string from being read as a route: in
					// `var doc = ` + "`" + `r.Get("/x", h)` + "`" + `, the code view
					// holds an empty literal, so the gate finds no call. Only
					// the match's own lines count, so a real call elsewhere
					// in a joined statement cannot license a string.
					if !rule.gate(strings.Join(in.Code[line:st.lineAt(text, m[1])+1], "\n")) {
						continue
					}
					if rule.inside != nil && !inSection(sections, in.Code, line) {
						continue
					}
					name := pathName
					if v := group(text, m, rule.nameIdx); v != "" {
						name = v
					}
					if rule.nameExclude != nil && rule.nameExclude.MatchString(name) {
						continue
					}
					name = rule.styleName(name)
					if rule.facet == FacetRoute {
						// A route the pattern matched without a path --
						// path("", view), Nest's bare @Get() -- is the root
						// of whatever prefix is in force, not unnamed.
						if name == "" {
							name = "/"
						}
						name = prefixes.apply(line, group(text, m, rule.varIdx), name)
					}
					detail := pathDetail
					if v := group(text, m, rule.detailIdx); v != "" {
						detail = v
					}
					if detail == "" {
						detail = rule.detail
					}
					if rule.facet == FacetRoute {
						detail = normalizeMethod(detail)
					}
					add(Detected{
						FilePath:  in.FilePath,
						Facet:     rule.facet,
						Framework: d.framework,
						Name:      name,
						Detail:    detail,
						Line:      line + 1,
					})
				}
			}
		}
	}
	return found
}

// section is the block under a line an Inside pattern matched.
type section struct {
	first, last int // the indented lines, inclusive
	child       int // indentation of the block's direct children, -1 for any
}

// sections finds the blocks a rule is confined to; nil for a rule without
// Inside.
func (r compiledDetectorRule) sections(code []string) []section {
	if r.inside == nil {
		return nil
	}
	var out []section
	for i, line := range code {
		if !r.inside.MatchString(line) {
			continue
		}
		last := indentEnd(code, i)
		if last == i {
			continue
		}
		sec := section{first: i + 1, last: last, child: -1}
		if r.children {
			for j := i + 1; j <= last; j++ {
				if strings.TrimSpace(code[j]) != "" {
					sec.child = indentWidth(code[j])
					break
				}
			}
		}
		out = append(out, sec)
	}
	return out
}

func inSection(sections []section, code []string, line int) bool {
	for _, s := range sections {
		if line >= s.first && line <= s.last && (s.child < 0 || indentWidth(code[line]) == s.child) {
			return true
		}
	}
	return false
}

// gate reports whether a line passes the rule's requires and requires_any.
func (r compiledDetectorRule) gate(line string) bool {
	if r.requires != "" && !strings.Contains(line, r.requires) {
		return false
	}
	if len(r.requiresAny) == 0 {
		return true
	}
	for _, alt := range r.requiresAny {
		if strings.Contains(line, alt) {
			return true
		}
	}
	return false
}

// extendStatement returns stmts[k] extended over the next n statements that
// are not blank. Statements cover consecutive lines, so the result is still a
// contiguous span and lineAt still maps offsets to lines.
func extendStatement(in DetectInput, stmts []statement, k, n int) statement {
	st := stmts[k]
	last := st.last
	for j := k + 1; j < len(stmts) && n > 0; j++ {
		last = stmts[j].last
		if strings.TrimSpace(stmts[j].code) != "" {
			n--
		}
	}
	if last == st.last {
		return st
	}
	return statement{
		first: st.first,
		last:  last,
		code:  strings.Join(in.Code[st.first:last+1], "\n"),
		str:   strings.Join(in.Strings[st.first:last+1], "\n"),
	}
}

// httpMethods are the route details written in upper case whatever the
// framework's own spelling: Express says get, Nest Get, Next.js GET, and an
// agent filtering routes by method should not need to know which.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HEAD": true, "OPTIONS": true, "TRACE": true, "CONNECT": true, "ALL": true, "ANY": true,
}

func normalizeMethod(detail string) string {
	if up := strings.ToUpper(detail); httpMethods[up] {
		return up
	}
	return detail
}

// group returns submatch idx of a FindAllStringSubmatchIndex result, or "".
func group(text string, m []int, idx int) string {
	if idx < 0 || m[2*idx] < 0 {
		return ""
	}
	return text[m[2*idx]:m[2*idx+1]]
}

func (r compiledDetectorRule) styleName(name string) string {
	switch {
	case r.routeStyle:
		return routeFromPath(name)
	case r.flatStyle:
		return flatRoute(name)
	case r.segStyle:
		// Segments name a path: "hello", ":name" is /hello/:name.
		if s := pathSegments(name); s != "" {
			return "/" + strings.TrimPrefix(s, "/")
		}
		return ""
	}
	return name
}

// flatRoute reads a React Router / Remix flat-route name: "." separates
// segments, "_index" is the index route, a segment starting with "_" is a
// pathless layout, a trailing "_" opts out of nesting, "$id" is ":id", a bare
// "$" is the splat "*", "($lang)" an optional ":lang?", and "[.]" an escaped
// literal dot.
func flatRoute(name string) string {
	const dot = "\x00"
	name = strings.ReplaceAll(name, "[.]", dot)
	var out []string
	for _, seg := range strings.Split(name, ".") {
		seg = strings.ReplaceAll(seg, dot, ".")
		switch {
		case seg == "" || seg == "_index":
			continue
		case strings.HasPrefix(seg, "_"):
			continue
		}
		seg = strings.TrimSuffix(seg, "_")
		optional := strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")")
		if optional {
			seg = seg[1 : len(seg)-1]
		}
		switch {
		case seg == "$":
			seg = "*"
		case strings.HasPrefix(seg, "$"):
			seg = ":" + seg[1:]
		}
		if optional {
			seg += "?"
		}
		out = append(out, seg)
	}
	return "/" + strings.Join(out, "/")
}

// routeFromPath turns a path fragment into a URL path: app/(shop)/users/[id]
// becomes /users/[id], pages/blog/index becomes /blog.
func routeFromPath(p string) string {
	var segs []string
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "" || seg == ".":
		case strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")"):
			// A route group organizes files without adding to the URL.
		case strings.HasPrefix(seg, "@"):
			// A parallel-route slot, likewise.
		default:
			segs = append(segs, seg)
		}
	}
	if n := len(segs); n > 0 && segs[n-1] == "index" {
		segs = segs[:n-1]
	}
	return "/" + strings.Join(segs, "/")
}

// filePrefixes are the route prefixes declared in one file.
type filePrefixes struct {
	byVar  map[string]string
	scoped []scopedPrefix // in line order
	spans  []spanPrefix   // in line order
}

type scopedPrefix struct {
	line   int
	prefix string
}

// spanPrefix is a statement-scoped prefix: it covers lines first to last.
type spanPrefix struct {
	first, last int
	prefix      string
}

func (d compiledDetector) findPrefixes(in DetectInput, stmts []statement) filePrefixes {
	var fp filePrefixes
	if len(d.prefixes) == 0 {
		return fp
	}
	for k, base := range stmts {
		for _, p := range d.prefixes {
			st := base
			if p.lookahead > 0 {
				st = extendStatement(in, stmts, k, p.lookahead)
			}
			text := st.code
			if p.keepStrings {
				text = st.str
			}
			for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
				line := st.lineAt(text, m[0])
				// The same gate as rules: on the match's own lines, in the
				// code view.
				span := strings.Join(in.Code[line:st.lineAt(text, m[1])+1], "\n")
				if p.requires != "" && !strings.Contains(span, p.requires) {
					continue
				}
				prefix := group(text, m, p.prefixIdx)
				if p.segments {
					prefix = pathSegments(prefix)
				}
				switch v := group(text, m, p.varIdx); {
				case v != "":
					if fp.byVar == nil {
						fp.byVar = make(map[string]string)
					}
					if parent, ok := fp.byVar[group(text, m, p.parentIdx)]; ok && parent != "" {
						prefix = joinRoute(parent, prefix)
					}
					fp.byVar[v] = prefix
				case p.scope == "block":
					fp.spans = append(fp.spans, spanPrefix{first: line, last: blockEnd(in.Code, line), prefix: prefix})
				case p.scope == "indent":
					fp.spans = append(fp.spans, spanPrefix{first: line, last: indentEnd(in.Code, line), prefix: prefix})
				case p.statement:
					last := callEnd(in.Code, line)
					if last == line {
						// The group call closes on its own line, as Actix's
						// web::scope("/api") does before its chained
						// .route(...) lines: it reaches to the end of the
						// statement it is part of.
						last = base.last
					}
					fp.spans = append(fp.spans, spanPrefix{first: line, last: last, prefix: prefix})
				default:
					fp.scoped = append(fp.scoped, scopedPrefix{line: line, prefix: prefix})
				}
			}
		}
	}
	sort.SliceStable(fp.spans, func(i, j int) bool { return fp.spans[i].first < fp.spans[j].first })
	return fp
}

// callEnd returns the line where the parentheses opened on line close, the
// span a statement-scoped prefix covers. A call that never closes within
// maxStatementLines covers its own line only.
func callEnd(code []string, line int) int {
	depth := parenDelta(code[line])
	if depth <= 0 {
		return line
	}
	limit := line + maxStatementLines - 1
	if limit > len(code)-1 {
		limit = len(code) - 1
	}
	for j := line + 1; j <= limit; j++ {
		depth += parenDelta(code[j])
		if depth <= 0 {
			return j
		}
	}
	return line
}

// blockEnd returns the line where the braces opened on line close: the
// reach of a block-scoped prefix. Blanking has removed braces in strings and
// comments. A block that never closes within maxStatementLines * 8 lines
// covers its own line only.
func blockEnd(code []string, line int) int {
	depth := braceDelta(code[line])
	if depth <= 0 {
		return line
	}
	limit := line + maxStatementLines*8
	if limit > len(code)-1 {
		limit = len(code) - 1
	}
	for j := line + 1; j <= limit; j++ {
		depth += braceDelta(code[j])
		if depth <= 0 {
			return j
		}
	}
	return line
}

// indentEnd returns the last line of the block indented deeper than line:
// the reach of an indent-scoped prefix (Ruby's do ... end, whose end sits at
// the opening line's own indentation). Blank lines do not end it.
func indentEnd(code []string, line int) int {
	base := indentWidth(code[line])
	last := line
	for j := line + 1; j < len(code); j++ {
		if strings.TrimSpace(code[j]) == "" {
			continue
		}
		if indentWidth(code[j]) <= base {
			break
		}
		last = j
	}
	return last
}

// pathSegments reads quoted path segments, "api", "v1" (Vapor's
// grouped("api", "v1")), as api/v1. Text without quotes is returned as is.
func pathSegments(s string) string {
	var parts []string
	for _, q := range quotedStrings(s) {
		if q = strings.Trim(q, "/"); q != "" {
			parts = append(parts, q)
		}
	}
	if len(parts) == 0 && !strings.ContainsAny(s, `"'`) {
		return s
	}
	return strings.Join(parts, "/")
}

// apply joins the prefixes in force for a route at line, named through
// router variable v when the rule captured one: a variable's own prefix, or
// else the class-level prefix in force and every group the line is inside,
// outermost first.
func (fp filePrefixes) apply(line int, v, name string) string {
	if prefix, ok := fp.byVar[v]; ok && v != "" {
		return joinRoute(prefix, name)
	}
	var chain []string
	for _, sp := range fp.scoped {
		if sp.line > line {
			break
		}
		chain = []string{sp.prefix}
	}
	for _, sp := range fp.spans {
		if sp.first <= line && line <= sp.last {
			chain = append(chain, sp.prefix)
		}
	}
	if len(chain) == 0 {
		return name
	}
	for i := len(chain) - 1; i >= 0; i-- {
		name = joinRoute(chain[i], name)
	}
	return name
}

// joinRoute joins a prefix and a route with exactly one slash between them:
// "/items" + "/{id}" and "items" + "{id}" are both "/items/{id}", and an empty
// route, as in Nest's bare @Get(), is the prefix itself.
func joinRoute(prefix, name string) string {
	p := strings.Trim(prefix, "/")
	n := strings.TrimLeft(name, "/")
	switch {
	case p == "" && n == "":
		return "/"
	case p == "":
		return "/" + n
	case n == "":
		return "/" + p
	}
	return "/" + p + "/" + n
}

// applies reports whether a detector should run on this file, checking its
// gates from the cheapest to the dearest: language, the import veto, path,
// imports, and last the dependencies, which may mean reading manifests.
func (d compiledDetector) applies(in DetectInput, deps func() []string) bool {
	if d.languages != nil && !d.languages[in.Language] {
		return false
	}
	for _, veto := range d.unlessImports {
		for _, imp := range in.Imports {
			if importGateMatches(imp, veto) {
				return false
			}
		}
	}
	if len(d.pathGlobs) > 0 && matchesCompiledGlobs(in.FilePath, d.pathGlobs) {
		return true
	}
	for _, want := range d.whenImports {
		for _, imp := range in.Imports {
			if importGateMatches(imp, want) {
				return true
			}
		}
	}
	if len(d.whenDependencies) == 0 {
		return false
	}
	for _, want := range d.whenDependencies {
		for _, dep := range deps() {
			if importMatches(dep, want) {
				return true
			}
		}
	}
	return false
}

// importGateMatches checks an import by its key and by how it was written.
func importGateMatches(imp Import, pattern string) bool {
	return importMatches(imp.ImportedPath, pattern) || (imp.Written != "" && importMatches(imp.Written, pattern))
}

// importMatches compares an import against a gate pattern. A trailing "*"
// makes it a prefix match; otherwise the import must be exactly the pattern or
// a submodule of it, so "django.db" also gates on "django.db.models".
func importMatches(imported, pattern string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(imported, strings.TrimSuffix(pattern, "*"))
	}
	if imported == pattern {
		return true
	}
	// Submodule separators differ by ecosystem: dots in Python and Java,
	// slashes in Go module paths, double colons in Rust, backslashes in PHP
	// namespaces. A gate of "tokio" must match "tokio::task".
	for _, sep := range []string{".", "/", "::", "\\"} {
		if strings.HasPrefix(imported, pattern+sep) {
			return true
		}
	}
	return false
}

// compiledGlob is one alternative of a path glob, its braces expanded and its
// segments split once when the detector loads rather than for every file.
type compiledGlob struct {
	segs     []string
	anyDepth bool // contains **, so it may span any number of directories
}

func compileGlobs(globs []string) []compiledGlob {
	var out []compiledGlob
	for _, g := range globs {
		for _, alt := range expandBraces(strings.TrimPrefix(g, "/")) {
			segs := strings.Split(strings.TrimPrefix(alt, "**/"), "/")
			anyDepth := false
			for _, seg := range segs {
				anyDepth = anyDepth || seg == "**"
			}
			out = append(out, compiledGlob{segs: segs, anyDepth: anyDepth})
		}
	}
	return out
}

// matchesCompiledGlobs reports whether path matches any glob, each allowed to
// start at any directory boundary.
func matchesCompiledGlobs(p string, globs []compiledGlob) bool {
	segs := strings.Split(p, "/")
	for _, g := range globs {
		if !g.anyDepth {
			// Without **, a glob of n segments matches exactly n of the
			// path's, and must end at its end: only the last n can match.
			if start := len(segs) - len(g.segs); start >= 0 && globSegments(g.segs, segs[start:]) {
				return true
			}
			continue
		}
		for start := range segs {
			if globSegments(g.segs, segs[start:]) {
				return true
			}
		}
	}
	return false
}

// matchesAnyGlob is matchesCompiledGlobs for globs not yet compiled.
func matchesAnyGlob(p string, globs []string) bool {
	return matchesCompiledGlobs(p, compileGlobs(globs))
}

// globSegments matches path segments against glob segments, "**" spanning
// any number of them, including none.
func globSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for k := 0; k <= len(segs); k++ {
			if globSegments(pat[1:], segs[k:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
		return false
	}
	return globSegments(pat[1:], segs[1:])
}

// expandBraces expands the first {a,b} group, recursively. Braces do not
// nest in the globs detectors use.
func expandBraces(g string) []string {
	open := strings.IndexByte(g, '{')
	if open < 0 {
		return []string{g}
	}
	end := strings.IndexByte(g[open:], '}')
	if end < 0 {
		return []string{g}
	}
	end += open
	var out []string
	for _, alt := range strings.Split(g[open+1:end], ",") {
		out = append(out, expandBraces(g[:open]+alt+g[end+1:])...)
	}
	return out
}
