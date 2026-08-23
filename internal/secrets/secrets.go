// Package secrets holds the one canonical list of credential shapes githints
// recognizes. Two independent controls consume it: recorder refuses to persist
// a summary that matches, and llm redacts matching lines before a diff leaves
// the process. The list used to be duplicated in both packages and drifting
// silently was only a matter of time.
//
// The list is deliberately short — a handful of well-known, high-signal shapes.
// False positives block legitimate summaries, so this is a defense-in-depth
// backstop, not a substitute for proper secret management.
package secrets

import "regexp"

// ValuePatterns are credential shapes recognized anywhere in a line of text.
var ValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),          // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`), // GitHub PAT / fine-grained / app / refresh
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.`), // JWT header.payload.
}

// Scan reports the first matching pattern's source, or ("", false) when text
// matches nothing.
func Scan(text string) (pattern string, matched bool) {
	for _, p := range ValuePatterns {
		if p.MatchString(text) {
			return p.String(), true
		}
	}
	return "", false
}
