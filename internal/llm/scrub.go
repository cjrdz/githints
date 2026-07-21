// Package llm contains the local Ollama integration and the diff scrubbing
// that runs before any repository content is sent off-process. All code here
// uses only the Go standard library.
package llm

import (
	"path/filepath"
	"regexp"
	"strings"
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

// secretValuePatterns catches high-signal credential shapes embedded anywhere
// in the diff, even inside files that are not otherwise classified as secret
// files. These are the same shapes recorder.ScanSecrets looks for.
var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.`),
}

// ScrubDiff redacts content lines from secret files and lines that contain
// high-signal credential patterns. Diff headers are preserved so the model
// still sees which files changed; only payload lines are redacted.
func ScrubDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	inSecretFile := false

	for i, line := range lines {
		if path, ok := filePathFromHeader(line); ok {
			inSecretFile = isSecretFile(path)
			continue
		}

		if isHeaderLine(line) {
			continue
		}

		if inSecretFile {
			lines[i] = secretLineMarker
			continue
		}

		for _, p := range secretValuePatterns {
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
func filePathFromHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)

	if strings.HasPrefix(line, "diff --git ") {
		parts := strings.Fields(line)
		if len(parts) >= 4 {
			return strings.TrimPrefix(parts[3], "b/"), true
		}
		return "", true
	}

	if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			p := strings.TrimPrefix(fields[1], "a/")
			p = strings.TrimPrefix(p, "b/")
			if p != "/dev/null" {
				return p, true
			}
		}
	}

	return "", false
}

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
