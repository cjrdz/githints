package secrets

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/textsafe"
)

// MaxDiffResultBytes caps a diff handed back to an agent (get_diff, `githints
// diff`). gitutil already caps what it buffers from git; this is the smaller,
// model-facing limit so one call on a generated file can't flood a context.
const MaxDiffResultBytes = 128 << 10

// DisplayDiff is the one path from a git diff to an agent: redacted first,
// then truncated. Both the MCP tool and the CLI use it, so neither can return
// a diff that skipped the scrub.
func DisplayDiff(diff string) string {
	return TruncateDiff(ScrubDiff(diff))
}

// TruncateDiff bounds diff at MaxDiffResultBytes, cutting at a line boundary
// and saying how much was left out.
func TruncateDiff(diff string) string {
	if len(diff) <= MaxDiffResultBytes {
		return diff
	}
	cut := MaxDiffResultBytes
	// Prefer a line boundary so the tail isn't a mangled hunk line.
	if i := strings.LastIndexByte(diff[:cut], '\n'); i > 0 {
		cut = i
	}
	return diff[:cut] + fmt.Sprintf("\n[truncated: diff is %d bytes, showing first %d]\n", len(diff), cut)
}

// secretLineMarker replaces lines too sensitive to hand back. It hides the
// value without changing the structure of the diff.
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

		for _, p := range ValuePatterns {
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

// Finding is a credential shape on an added line of a diff. It never holds
// the value: Pattern is the regular expression that matched.
type Finding struct {
	File    string
	Line    int
	Pattern string
}

// ScanDiff looks for credential shapes on the lines a unified diff adds,
// after removing the control and zero-width characters that could split a key
// so no pattern recognizes it. Removed and context lines are not scanned: the
// question is what this change would put into the repository.
//
// Like ScrubDiff, a "+++" line is a header only before the current file's
// first "@@"; inside a hunk it is an added line that starts with "++".
func ScanDiff(diff string) []Finding {
	var out []Finding
	file, inHunk, line := "", false, 0
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file, inHunk = "", false
		case !inHunk && strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "@@"):
			inHunk = true
			line = hunkStart(l)
		case inHunk && strings.HasPrefix(l, "+"):
			if p, ok := Scan(textsafe.StripControls(l[1:])); ok {
				out = append(out, Finding{File: file, Line: line, Pattern: p})
			}
			line++
		case inHunk && strings.HasPrefix(l, " "):
			line++ // context, when the diff has any
		}
	}
	return out
}

// hunkStart reads the new-file start line from "@@ -a,b +c,d @@".
func hunkStart(header string) int {
	i := strings.Index(header, " +")
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range header[i+2:] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
