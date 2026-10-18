package lang

import (
	"fmt"
	"os"
	"sync"
)

// Specs are parsed and compiled once per process. NewRegistry runs on every
// scan, and recompiling a few dozen regexes each time would be pure waste; a
// SpecParser is immutable and safe to share.
var (
	embeddedOnce    sync.Once
	embeddedParsers []*SpecParser
	embeddedErrs    []error
)

// EmbeddedParsers returns the parsers built from the specs compiled into this
// binary, plus any spec that failed to load.
//
// A broken embedded spec is a build-time bug, and TestEmbeddedSpecsAreValid
// fails on one. It is still reported rather than panicked on, because this
// path runs inside the post-commit hook, where a panic is an uncaught crash
// mid-commit.
func EmbeddedParsers() ([]*SpecParser, []error) {
	embeddedOnce.Do(loadEmbedded)
	return embeddedParsers, embeddedErrs
}

// loadEmbedded builds a parser from every packs/<language>/language.json.
// Adding a language is adding that file: the registry picks it up with no Go
// change and no edit to NewRegistry.
func loadEmbedded() {
	names, err := packFiles(true)
	if err != nil {
		embeddedErrs = append(embeddedErrs, err)
		return
	}
	for _, name := range names {
		data, err := packFS.ReadFile(name)
		if err != nil {
			embeddedErrs = append(embeddedErrs, fmt.Errorf("%s: %w", packLabel(name), err))
			continue
		}
		// Validated and compiled on first use, not here; see newSpecParser.
		spec, err := decodeSpec(data)
		if err != nil {
			embeddedErrs = append(embeddedErrs, fmt.Errorf("%s: %w", packLabel(name), err))
			continue
		}
		embeddedParsers = append(embeddedParsers, newSpecParser(spec))
	}
}

// registerSpecParser adds a spec-driven parser, reporting a clash instead of
// panicking. Native parsers keep the panic: those are wired up in code, so a
// collision is a mistake that cannot reach a user. A spec can arrive from a
// file, so it gets an error path.
func (r *Registry) registerSpecParser(p *SpecParser, origin string) error {
	name := p.Language()
	if _, taken := r.parsers[name]; taken {
		return fmt.Errorf("language %q is already registered", name)
	}
	for _, ext := range p.Extensions() {
		if existing, taken := r.byExt[ext]; taken {
			return fmt.Errorf("extension %q is already claimed by %s", ext, existing.Language())
		}
	}
	r.parsers[name] = p
	r.origin[name] = origin
	for _, ext := range p.Extensions() {
		r.byExt[ext] = p
	}
	r.noteNames(p)
	return nil
}

// warnf reports a spec problem. Diagnostics go to stderr: stdout in serve mode
// carries JSON-RPC and nothing else.
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "githints: "+format+"\n", args...)
}
