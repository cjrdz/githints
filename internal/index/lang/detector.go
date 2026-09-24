package lang

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
)

// knownFacets is the closed set a detector may emit. Facet names reach users
// through a query interface, so an open set would turn a typo into a facet
// nobody can find.
var knownFacets = []string{FacetRoute, FacetModel, FacetComponent, FacetMigration, FacetJob, FacetTest}

// Limits, applied to every detector for the same reason spec limits are.
const (
	MaxDetectorRules   = 32
	MaxDetectorImports = 32
)

// DetectorRule matches one line and names what it found.
//
// Rules match lines rather than symbols because most of what is worth finding
// is not a declaration. `r.Get("/users", h)` is a call, a Django urlpattern is
// a list entry, and a migration is a file rather than a definition. Attaching
// facets to symbols would have covered models and missed routes entirely.
type DetectorRule struct {
	Facet    string `json:"facet"`
	Requires string `json:"requires"`
	Pattern  string `json:"pattern"`

	// KeepStrings matches against the view that retains string contents.
	// Route paths and struct tags live inside string literals, so a rule
	// looking for one must see them; rules matching code shape should not,
	// since then a quoted example could masquerade as the real thing.
	KeepStrings bool `json:"keep_strings"`
}

// Detector is a framework recognized in one or more languages.
type Detector struct {
	Framework string   `json:"framework"`
	Languages []string `json:"languages"`

	// WhenImports gates the whole detector: at least one of these must be
	// imported by the file. A trailing "*" makes it a prefix match.
	//
	// Without a gate, a pattern like `class \w+\(.*Model\)` would claim every
	// ORM ever written, and attributing it to the wrong framework is worse
	// than not detecting it.
	WhenImports []string `json:"when_imports"`

	// PathGlobs optionally restricts the detector to matching file paths, for
	// frameworks whose convention is a location rather than an import.
	PathGlobs []string `json:"path_globs"`

	Rules []DetectorRule `json:"rules"`
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
	framework   string
	languages   map[string]bool
	whenImports []string
	pathGlobs   []string
	rules       []compiledDetectorRule
}

type compiledDetectorRule struct {
	facet       string
	requires    string
	re          *regexp.Regexp
	nameIdx     int
	detailIdx   int
	keepStrings bool
}

// DetectorSet is a compiled collection of detectors.
type DetectorSet struct {
	detectors []compiledDetector
}

// LoadDetector parses and validates a JSONC detector definition.
func LoadDetector(data []byte) (Detector, error) {
	var d Detector
	if err := json.Unmarshal(stripJSONC(data), &d); err != nil {
		return Detector{}, fmt.Errorf("parse detector: %w", err)
	}
	d.Framework = strings.ToLower(strings.TrimSpace(d.Framework))
	for i, l := range d.Languages {
		d.Languages[i] = strings.ToLower(strings.TrimSpace(l))
	}
	if err := d.Validate(); err != nil {
		return Detector{}, err
	}
	return d, nil
}

// Validate reports the first problem that would make a detector unusable or
// misleading.
func (d Detector) Validate() error {
	if d.Framework == "" {
		return fmt.Errorf("detector has no framework name")
	}
	if strings.ContainsAny(d.Framework, ":,") {
		return fmt.Errorf("framework %q must not contain ':' or ','", d.Framework)
	}
	if len(d.WhenImports) == 0 && len(d.PathGlobs) == 0 {
		return fmt.Errorf("framework %s has no when_imports or path_globs, so it would claim every file", d.Framework)
	}
	if len(d.WhenImports) > MaxDetectorImports {
		return fmt.Errorf("framework %s: %d when_imports exceeds the limit of %d", d.Framework, len(d.WhenImports), MaxDetectorImports)
	}
	if len(d.Rules) == 0 {
		return fmt.Errorf("framework %s has no rules", d.Framework)
	}
	if len(d.Rules) > MaxDetectorRules {
		return fmt.Errorf("framework %s: %d rules exceeds the limit of %d", d.Framework, len(d.Rules), MaxDetectorRules)
	}
	for i, rule := range d.Rules {
		if !validFacet(rule.Facet) {
			return fmt.Errorf("framework %s: rules[%d] has unknown facet %q (want one of %s)",
				d.Framework, i, rule.Facet, strings.Join(knownFacets, ", "))
		}
		if rule.Pattern == "" {
			return fmt.Errorf("framework %s: rules[%d] has no pattern", d.Framework, i)
		}
		if len(rule.Pattern) > MaxSpecPatternLen {
			return fmt.Errorf("framework %s: rules[%d] pattern is over the limit of %d", d.Framework, i, MaxSpecPatternLen)
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("framework %s: rules[%d] pattern does not compile: %w", d.Framework, i, err)
		}
		if !hasGroup(re, "name") {
			return fmt.Errorf("framework %s: rules[%d] pattern must capture (?P<name>...)", d.Framework, i)
		}
	}
	return nil
}

func validFacet(f string) bool {
	for _, known := range knownFacets {
		if f == known {
			return true
		}
	}
	return false
}

// NewDetectorSet compiles detectors for use during a scan.
func NewDetectorSet(detectors []Detector) (*DetectorSet, error) {
	set := &DetectorSet{}
	for _, d := range detectors {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		cd := compiledDetector{
			framework:   d.Framework,
			whenImports: d.WhenImports,
			pathGlobs:   d.PathGlobs,
		}
		if len(d.Languages) > 0 {
			cd.languages = make(map[string]bool, len(d.Languages))
			for _, l := range d.Languages {
				cd.languages[l] = true
			}
		}
		for _, rule := range d.Rules {
			re, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return nil, fmt.Errorf("framework %s: %w", d.Framework, err)
			}
			cd.rules = append(cd.rules, compiledDetectorRule{
				facet:       rule.Facet,
				requires:    rule.Requires,
				re:          re,
				nameIdx:     re.SubexpIndex("name"),
				detailIdx:   re.SubexpIndex("detail"),
				keepStrings: rule.KeepStrings,
			})
		}
		set.detectors = append(set.detectors, cd)
	}
	return set, nil
}

// Empty reports whether there is nothing to detect, so callers can skip the
// work of producing blanked views.
func (s *DetectorSet) Empty() bool { return s == nil || len(s.detectors) == 0 }

// DetectInput is everything detection needs about one file.
type DetectInput struct {
	FilePath string
	Language string
	Imports  []Import

	// Code has string bodies emptied; Strings keeps them. Rules choose per
	// rule which view they match against.
	Code    []string
	Strings []string
}

// Detect runs every applicable detector over a file.
func (s *DetectorSet) Detect(in DetectInput) []Detected {
	if s.Empty() {
		return nil
	}
	var found []Detected
	for _, d := range s.detectors {
		if !d.applies(in) {
			continue
		}
		for _, rule := range d.rules {
			lines := in.Code
			if rule.keepStrings {
				lines = in.Strings
			}
			for i, line := range lines {
				if rule.requires != "" && !strings.Contains(line, rule.requires) {
					continue
				}
				m := rule.re.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				name := m[rule.nameIdx]
				if name == "" {
					continue
				}
				det := Detected{
					FilePath:  in.FilePath,
					Facet:     rule.facet,
					Framework: d.framework,
					Name:      name,
					Line:      i + 1,
				}
				if rule.detailIdx >= 0 {
					det.Detail = m[rule.detailIdx]
				}
				found = append(found, det)
			}
		}
	}
	return found
}

// applies reports whether a detector should run on this file at all.
func (d compiledDetector) applies(in DetectInput) bool {
	if d.languages != nil && !d.languages[in.Language] {
		return false
	}
	if len(d.pathGlobs) > 0 && matchesAnyGlob(in.FilePath, d.pathGlobs) {
		return true
	}
	for _, want := range d.whenImports {
		for _, imp := range in.Imports {
			if importMatches(imp.ImportedPath, want) {
				return true
			}
		}
	}
	return false
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
	for _, sep := range []string{".", "/"} {
		if strings.HasPrefix(imported, pattern+sep) {
			return true
		}
	}
	return false
}

// matchesAnyGlob does a simple suffix/segment match, enough for the
// "**/models.py" and "migrations/*" conventions detectors need.
func matchesAnyGlob(path string, globs []string) bool {
	for _, g := range globs {
		g = strings.TrimPrefix(g, "**/")
		if strings.Contains(g, "*") {
			prefix, suffix, _ := strings.Cut(g, "*")
			if strings.Contains(path, prefix) && strings.HasSuffix(path, suffix) {
				return true
			}
			continue
		}
		if path == g || strings.HasSuffix(path, "/"+g) {
			return true
		}
	}
	return false
}
