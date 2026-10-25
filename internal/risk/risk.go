// Package risk scores a set of changed files by what they are, not by how
// much changed: a README edit needs no review however long it is, and two lines
// in an auth package need a deep one. It is the evidence half of
// receipt-driven review: githints says how deep a review should go and why,
// and records the receipt; it never runs a review or calls a model.
//
// A file's level is the highest of its signals, never a sum: its path class,
// the framework facets the index found in it, how many files import it, and
// whether a recent change to it failed or was reverted. Repository rules can
// only add classes, so a config.json that arrives in a clone can raise a level
// but never lower one.
package risk

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/store"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Level is how deep a review should go.
type Level int

const (
	Low    Level = iota // no review needed
	Medium              // one focused lens
	High                // all four lenses
)

func (l Level) String() string {
	switch l {
	case Low:
		return "low"
	case Medium:
		return "medium"
	default:
		return "high"
	}
}

// MarshalText makes a level read as its name in JSON.
func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// ParseLevel is the inverse of String.
func ParseLevel(s string) (Level, bool) {
	for _, l := range []Level{Low, Medium, High} {
		if l.String() == s {
			return l, true
		}
	}
	return Low, false
}

// Lenses are the four review perspectives. A medium change gets one of them,
// chosen by the reviewer; a high one gets all four.
var Lenses = []string{"risk", "resilience", "readability", "reliability"}

// LensCount is how many lenses a review at this level covers.
func (l Level) LensCount() int {
	switch l {
	case Low:
		return 0
	case Medium:
		return 1
	default:
		return len(Lenses)
	}
}

const (
	// MaxPaths bounds one assessment. Past it the rest are not scored and the
	// level is at least medium: a change that large is not low risk.
	MaxPaths = 500
	// FanInHigh is the number of importing files that makes a file high risk
	// on its own: a change there ripples further than one review can follow.
	FanInHigh = 15
	// coChangeMin is how many shared commits make a missing partner worth
	// reading first; fewer is coincidence.
	coChangeMin = 3
	// historyWindow is how many recent changes to a file are checked for a
	// failed or reverted outcome.
	historyWindow = 10
)

// Reason is one signal that set a file's level.
type Reason struct {
	File   string
	Signal string // class, facet, fan-in, history, scope, index
	Detail string
	Level  Level
}

// Report is the assessment of one candidate change set.
type Report struct {
	Level     Level
	Files     int
	Reasons   []Reason
	ReadFirst []string // files that usually change with these but are not in the set
	Truncated bool
	// Warnings are problems with the repository's review rules, skipped
	// rather than fatal so a newer config.json cannot break an older binary.
	Warnings []string
}

// Inputs is what Assess reads. Index may be nil (indexing off, or not built
// yet); then only path rules and history apply, and the report says so.
type Inputs struct {
	Store  *store.Store
	Index  *index.Store
	Root   string
	Config config.Review
}

// Assess scores paths, which must already be validated repo-relative paths.
func Assess(in Inputs, paths []string) (Report, error) {
	rules, warnings := compileRules(in.Config.Rules)
	rep := Report{Warnings: warnings}
	if len(paths) > MaxPaths {
		rep.Truncated = true
		rep.add(Reason{Signal: "scope", Detail: fmt.Sprintf("%d files; only the first %d were scored", len(paths), MaxPaths), Level: Medium})
		paths = paths[:MaxPaths]
	}
	rep.Files = len(paths)

	var registry *lang.Registry
	if in.Index != nil {
		if last, err := in.Index.LastIndexedAt(); err != nil || last == 0 {
			in.Index = nil // nothing indexed: no claim to make from it
		} else {
			registry = lang.NewRegistryForRoot(in.Root)
		}
	}
	if in.Index == nil && len(paths) > 0 {
		rep.add(Reason{Signal: "index", Detail: "structural index unavailable: path rules and history only", Level: Low})
	}

	inSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		inSet[p] = true
	}
	partners := map[string]int{}
	for _, p := range paths {
		level := Low
		for _, c := range classify(p, rules) {
			rep.add(Reason{File: p, Signal: "class", Detail: c.name, Level: c.level})
			level = max(level, c.level)
		}
		if in.Index != nil {
			l, err := rep.indexSignals(in.Index, registry, in.Root, p)
			if err != nil {
				return Report{}, err
			}
			level = max(level, l)
		}
		if in.Store != nil {
			if err := rep.historySignal(in.Store, p, level); err != nil {
				return Report{}, err
			}
			co, err := in.Store.CoChangingFiles(p, 3)
			if err != nil {
				return Report{}, err
			}
			for _, c := range co {
				if c.Commits >= coChangeMin && !inSet[c.FilePath] {
					partners[c.FilePath] = max(partners[c.FilePath], c.Commits)
				}
			}
		}
	}
	for f := range partners {
		rep.ReadFirst = append(rep.ReadFirst, f)
	}
	sort.Slice(rep.ReadFirst, func(i, j int) bool {
		a, b := rep.ReadFirst[i], rep.ReadFirst[j]
		if partners[a] != partners[b] {
			return partners[a] > partners[b]
		}
		return a < b
	})
	return rep, nil
}

// Staged lists the candidate a review covers: every staged path, with
// renames as both their old and new names, and githints' own files left out
// (they are output, regenerated from the store).
func Staged() ([]string, error) {
	files, err := gitutil.StagedFilesNoRenames()
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, f := range files {
		if !strings.HasPrefix(f, ".githints/") || f == ".githints/config.json" {
			out = append(out, f)
		}
	}
	return out, nil
}

// add records a reason and raises the report's level to it.
func (r *Report) add(reason Reason) {
	r.Reasons = append(r.Reasons, reason)
	r.Level = max(r.Level, reason.Level)
}

// facetLevels is the level a detected framework construct implies. A facet
// not listed implies nothing beyond the file's class.
var facetLevels = map[string]Level{
	"route":      Medium,
	"middleware": Medium,
	"model":      Medium,
	"schema":     Medium,
	"controller": Medium,
	"service":    Medium,
	"job":        Medium,
	"command":    Medium,
	"migration":  High,
	"infra":      High,
}

func (r *Report) indexSignals(idx *index.Store, registry *lang.Registry, root, p string) (Level, error) {
	level := Low
	facets, err := idx.FacetsForFile(p)
	if err != nil {
		return Low, err
	}
	seen := map[string]bool{}
	for _, f := range facets {
		l, ok := facetLevels[f.Facet]
		if !ok || seen[f.Facet] {
			continue
		}
		seen[f.Facet] = true
		r.add(Reason{File: p, Signal: "facet", Detail: f.Facet + " (" + f.Framework + ")", Level: l})
		level = max(level, l)
	}
	// An unresolvable import path (a file of no indexed language) has no
	// dependents to count; that is not an error.
	if _, deps, err := idx.Dependents(registry, root, p); err == nil && len(deps) >= FanInHigh {
		r.add(Reason{File: p, Signal: "fan-in", Detail: fmt.Sprintf("imported by %d files", len(deps)), Level: High})
		level = High
	}
	return level, nil
}

// historySignal raises level by one when a recent change to p failed, was
// reverted or only partly worked: that ground has already given way once.
func (r *Report) historySignal(st *store.Store, p string, level Level) error {
	hist, err := st.FileHistory(p, historyWindow)
	if err != nil {
		return err
	}
	for _, c := range hist {
		o := c.LatestOutcome
		if o == nil || (o.OutcomeState != store.OutcomeFailed && o.OutcomeState != store.OutcomeReverted && o.OutcomeState != store.OutcomePartial) {
			continue
		}
		r.add(Reason{File: p, Signal: "history", Detail: fmt.Sprintf("change #%d was %s", c.ID, o.OutcomeState), Level: min(level+1, High)})
		return nil
	}
	return nil
}

// Format renders the report for a terminal or an agent. File names come from
// the repository, so they go through textsafe.
func (r Report) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Risk: %s (%d file(s))\n", r.Level, r.Files)
	switch r.Level {
	case Low:
		b.WriteString("Review: none needed.\n")
	case Medium:
		fmt.Fprintf(&b, "Review: one focused lens, the one that fits the change best (%s).\n", strings.Join(Lenses, ", "))
	default:
		fmt.Fprintf(&b, "Review: all four lenses (%s).\n", strings.Join(Lenses, ", "))
	}
	if len(r.Reasons) > 0 {
		b.WriteString("\nWhy:\n")
		reasons := append([]Reason(nil), r.Reasons...)
		sort.SliceStable(reasons, func(i, j int) bool { return reasons[i].Level > reasons[j].Level })
		code := 0
		for _, x := range reasons {
			if x.Signal == "class" && (x.Level == Low || x.Detail == "code") {
				if x.Detail == "code" {
					code++ // counted below: one line per ordinary file explains nothing
				}
				continue // nor does "docs: low"
			}
			where := ""
			if x.File != "" {
				where = textsafe.OneLine(x.File) + ": "
			}
			fmt.Fprintf(&b, "- [%s] %s%s %s\n", x.Level, where, x.Signal, textsafe.OneLine(x.Detail))
		}
		if code > 0 {
			fmt.Fprintf(&b, "- [medium] %d file(s) of ordinary code\n", code)
		}
	}
	if len(r.ReadFirst) > 0 {
		b.WriteString("\nUsually changes with these, which are not in this change; read them first:\n")
		for _, f := range r.ReadFirst {
			fmt.Fprintf(&b, "- %s\n", textsafe.OneLine(f))
		}
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", textsafe.OneLine(w))
	}
	return b.String()
}

// Match reports whether a review rule pattern matches a repo-relative path:
// "dir/**" matches that directory anywhere in the path, a pattern with a slash
// is matched against the whole path, and anything else against the file name.
func Match(pattern, p string) bool {
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		return strings.HasPrefix(p, dir+"/") || strings.Contains(p, "/"+dir+"/")
	}
	if strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, p) // a malformed pattern is refused by compileRules
		return ok
	}
	ok, _ := path.Match(pattern, path.Base(p))
	return ok
}
