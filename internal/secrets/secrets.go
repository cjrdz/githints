// Package secrets holds the one canonical list of credential shapes githints
// recognizes. Two independent controls consume it: recorder refuses to persist
// a summary that matches, and llm redacts matching lines before a diff leaves
// the process. The list used to be duplicated in both packages and drifting
// silently was only a matter of time.
//
// The list is deliberately short — well-known shapes with a fixed, distinctive
// prefix, chosen so a match is almost never an ordinary word or identifier.
// False positives block legitimate summaries, so this is a defense-in-depth
// backstop, not a substitute for proper secret management.
package secrets

import "regexp"

// ValuePatterns are credential shapes recognized anywhere in a line of text.
var ValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),             // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),    // GitHub PAT / OAuth / user-to-server / server-to-server / refresh
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{80,}`), // GitHub fine-grained PAT
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`), // Slack bot / user / app tokens
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),    // Anthropic API key
	regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{40,}`),   // OpenAI project key
	regexp.MustCompile(`sk-[A-Za-z0-9]{20}T3BlbkFJ`),   // OpenAI legacy key (embeds base64 "OpenAI")
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),        // Google API key
	regexp.MustCompile(`[rs]k_live_[0-9A-Za-z]{24,}`),  // Stripe live secret / restricted key
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----`),
	regexp.MustCompile(`-----BEGIN PGP PRIVATE KEY BLOCK-----`),
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
