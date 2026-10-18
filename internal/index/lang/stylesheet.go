package lang

import (
	"path"
	"strings"
)

// resolveStylesheet turns an @import, @use or @forward into the repository
// path of the stylesheet it names, the way Sass and Less look one up, or
// returns the package it names when no file in the repository matches.
//
// From the importing file's directory it tries the path as written, then
// with each of exts (the language's own) and .css, then as a partial
// (_name.ext), then as a directory's _index or index. A URL is an external
// resource with no file behind it, and gives "" (no import). Sass's built-in
// modules (sass:math) and package paths (bootstrap/scss/bootstrap,
// tailwindcss) are kept as written, without the ~ webpack's loaders use for
// a package, so a detector gate can name the package.
func resolveStylesheet(root, file, spec string, exts []string) string {
	if strings.Contains(spec, "://") || strings.HasPrefix(spec, "//") || strings.HasPrefix(spec, "data:") {
		return ""
	}
	spec = strings.TrimPrefix(spec, "~")
	if root == "" || strings.HasPrefix(spec, "sass:") || path.IsAbs(spec) {
		return spec
	}
	joined := path.Join(path.Dir(file), spec)
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return spec
	}
	dir, base := path.Split(joined)
	candidates := []string{joined}
	all := append(append([]string(nil), exts...), ".css")
	if path.Ext(base) == "" {
		for _, ext := range all {
			candidates = append(candidates, joined+ext, dir+"_"+base+ext)
		}
		for _, ext := range all {
			candidates = append(candidates, path.Join(joined, "_index"+ext), path.Join(joined, "index"+ext))
		}
	} else {
		candidates = append(candidates, dir+"_"+base)
	}
	for _, c := range candidates {
		if probeFile(root, c) {
			return c
		}
	}
	return spec
}
