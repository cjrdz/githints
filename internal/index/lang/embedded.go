package lang

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"sync"
)

// specFS holds every language shipped as data. Adding a language here is
// adding a file: the registry picks it up with no Go change and no edit to
// NewRegistry.
//
//go:embed specs/*.json
var specFS embed.FS

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

func loadEmbedded() {
	entries, err := fs.ReadDir(specFS, "specs")
	if err != nil {
		embeddedErrs = append(embeddedErrs, fmt.Errorf("read embedded specs: %w", err))
		return
	}
	// Sorted so registration order, and therefore any duplicate-claim error,
	// is the same on every machine.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && path.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := specFS.ReadFile(path.Join("specs", name))
		if err != nil {
			embeddedErrs = append(embeddedErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		spec, err := LoadSpec(data)
		if err != nil {
			embeddedErrs = append(embeddedErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		parser, err := NewSpecParser(spec)
		if err != nil {
			embeddedErrs = append(embeddedErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		embeddedParsers = append(embeddedParsers, parser)
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
	return nil
}

// warnf reports a spec problem. Diagnostics go to stderr: stdout in serve mode
// carries JSON-RPC and nothing else.
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "githints: "+format+"\n", args...)
}
