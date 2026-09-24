package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UserSpecDir is where a repository may put its own language specs, relative
// to the repository root.
const UserSpecDir = ".githints/langs"

// Limits on repository-supplied specs. A spec cannot execute anything -- Go's
// regexp is RE2, so there is no catastrophic backtracking to exploit, and the
// index it feeds is a derived cache that can be deleted and rebuilt. These
// caps exist so a careless or hostile spec costs bounded work rather than a
// scan that never finishes.
//
// Per-pattern length and rule count are already capped for every spec by
// MaxSpecPatternLen and MaxSpecRules, which together bound how much regex one
// language can compile.
const (
	MaxUserSpecFiles = 32
	MaxUserSpecBytes = 64 << 10
)

// LoadUserSpecs reads language specs a repository ships in .githints/langs.
//
// Every failure is returned rather than raised: this runs from the post-commit
// hook, and a repository's own file must never be able to fail someone's
// commit or stop the languages that do work from being indexed.
func LoadUserSpecs(root string) ([]*SpecParser, []error) {
	dir := filepath.Join(root, filepath.FromSlash(UserSpecDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // the ordinary case
		}
		return nil, []error{fmt.Errorf("read %s: %w", UserSpecDir, err)}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	// Sorted so a duplicate claim is reported against the same file on every
	// machine, rather than depending on directory order.
	sort.Strings(names)

	var (
		parsers []*SpecParser
		errs    []error
	)
	if len(names) > MaxUserSpecFiles {
		errs = append(errs, fmt.Errorf("%s holds %d specs, over the limit of %d; ignoring the rest",
			UserSpecDir, len(names), MaxUserSpecFiles))
		names = names[:MaxUserSpecFiles]
	}

	for _, name := range names {
		path := filepath.Join(dir, name)
		rel := UserSpecDir + "/" + name

		info, err := os.Lstat(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		// Only regular files. A symlink here would read whatever it points at,
		// which is not something a repository should get to choose.
		if !info.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: not a regular file", rel))
			continue
		}
		if info.Size() > MaxUserSpecBytes {
			errs = append(errs, fmt.Errorf("%s: %d bytes, over the limit of %d", rel, info.Size(), MaxUserSpecBytes))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		spec, err := LoadSpec(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		parser, err := NewSpecParser(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
			continue
		}
		parsers = append(parsers, parser)
	}
	return parsers, errs
}
