// Package llm contains the local Ollama integration and the diff scrubbing
// that runs before any repository content is sent off-process. All code here
// uses only the Go standard library.
package llm

import (
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/secrets"
)

// secretLineMarker replaces lines that are too sensitive to ship to the local
// model. It is the same marker the rest of githints would want for any stored
// caption: it hides the value without changing the structure of the diff.
const secretLineMarker = "[REDACTED SECRET LINE]"

// secretFileGlobs matches file paths whose entire diff content should be
// redacted before it reaches the model. These are the obvious credential
// carriers; the list errs on the side of caution.
var secretFileGlobs = []string{
	".env*",
	"*.pem",
	"*.key",
	"id_rsa*",
	"id_ed25519*",
	"id_ecdsa*",
	"id_dsa*",
	"*.p12",
	"*.pfx",
	"*.der",
	"*.pkcs*",
	"*secret*",
	"*credential*",
	"*private*",
}

// ScrubDiff redacts content lines from secret files and lines that contain
// high-signal credential patterns. Diff headers are preserved so the model
// still sees which files changed; only payload lines are redacted.
//
// Header detection is position-sensitive, which matters for correctness: inside
// a hunk, a deleted line whose own text begins with "--" renders as "--- ...",
// and an added line beginning with "++" renders as "+++ ...". Treating those as
// file headers would re-classify the file mid-hunk and could clear protection
// while still inside a secret file. Only lines before the first "@@" of the
// current file are considered headers.
func ScrubDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	// Fail closed: until a header names the file, assume it is sensitive.
	inSecretFile := false
	inHunk := false

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// A new file starts. Its path cannot be read reliably from this
			// line, so stay closed until the ---/+++ pair names it.
			inSecretFile = true
			inHunk = false
			continue

		case strings.HasPrefix(line, "@@ "):
			inHunk = true
			continue

		case !inHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			if path, ok := filePathFromHeader(line); ok {
				inSecretFile = isSecretFile(path)
			}
			continue

		case !inHunk && isHeaderLine(line):
			continue
		}

		if inSecretFile {
			lines[i] = secretLineMarker
			continue
		}

		for _, p := range secrets.ValuePatterns {
			if p.MatchString(line) {
				lines[i] = secretLineMarker
				break
			}
		}
	}

	return strings.Join(lines, "\n")
}

// filePathFromHeader extracts the file path from unified-diff header lines
// such as "diff --git a/foo.go b/foo.go", "--- a/foo.go", or "+++ b/foo.go".
// It returns ("", false) for non-header lines.
//
// The "diff --git a/P b/P" line is deliberately NOT parsed: git does not quote
// paths, so a path containing a space (or worse, " b/") makes the two halves
// impossible to split reliably — the old code used strings.Fields and
// classified "config/private data.txt" as "data.txt", which matched no secret
// glob and cleared protection for the whole hunk. It returns unparseableHeader
// instead, which callers treat as secret.
//
// The "--- a/P" and "+++ b/P" lines carry exactly one path each and are
// unambiguous once the 4-character prefix is stripped. They always precede a
// hunk's content, so classification is correct before any payload line is
// reached; the git header only has to fail closed until then.
func filePathFromHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)

	if strings.HasPrefix(line, "diff --git ") {
		return unparseableHeader, true
	}

	if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
		p := strings.TrimSpace(line[4:])
		if p == "/dev/null" {
			// One side of an add or delete; the other side names the file.
			return "", false
		}
		// Strip the a/ or b/ prefix git adds, and any trailing tab-separated
		// metadata (timestamps in non-git unified diffs).
		if tab := strings.IndexByte(p, '\t'); tab >= 0 {
			p = p[:tab]
		}
		if after, ok := strings.CutPrefix(p, "a/"); ok {
			p = after
		} else if after, ok := strings.CutPrefix(p, "b/"); ok {
			p = after
		}
		if p == "" {
			return unparseableHeader, true
		}
		return p, true
	}

	return "", false
}

// unparseableHeader is returned for a file header we could not read a path
// from. isSecretFile treats it as secret so an unrecognized header redacts its
// hunk instead of leaking it.
const unparseableHeader = "\x00unparseable"

func isHeaderLine(line string) bool {
	return strings.HasPrefix(line, "@@ ") ||
		strings.HasPrefix(line, "--- ") ||
		strings.HasPrefix(line, "+++ ") ||
		strings.HasPrefix(line, "diff ") ||
		strings.HasPrefix(line, "index ") ||
		strings.HasPrefix(line, "new file ") ||
		strings.HasPrefix(line, "deleted file ") ||
		strings.HasPrefix(line, "similarity ") ||
		strings.HasPrefix(line, "rename ") ||
		strings.HasPrefix(line, "Binary ")
}

func isSecretFile(path string) bool {
	// Fail closed: a header we could not parse is treated as a secret file so
	// its hunk is redacted rather than emitted unclassified.
	if path == unparseableHeader {
		return true
	}
	base := filepath.Base(path)
	for _, g := range secretFileGlobs {
		if matched, _ := filepath.Match(g, base); matched {
			return true
		}
		if matched, _ := filepath.Match(g, path); matched {
			return true
		}
	}
	return false
}
