package secrets

import (
	"strings"
	"testing"
)

// The positive samples are assembled at run time so this file does not itself
// contain anything a push-protection scanner would flag as a live credential.
func TestScanRecognizesKnownShapes(t *testing.T) {
	alnum := func(n int) string { return strings.Repeat("a1B2", n/4+1)[:n] }
	for name, sample := range map[string]string{
		"aws":            "AKIA" + strings.ToUpper(alnum(16)),
		"github classic": "ghp_" + alnum(36),
		"github fine":    "github" + "_pat_" + alnum(82),
		"slack":          "xox" + "b-" + "1234567890-abcdefghij",
		"anthropic":      "sk-" + "ant-" + "api03-" + alnum(40),
		"openai proj":    "sk-" + "proj-" + alnum(48),
		"openai legacy":  "sk-" + alnum(20) + "T3Blbk" + "FJ" + alnum(20),
		"google":         "AI" + "za" + alnum(35),
		"stripe":         "sk" + "_live_" + alnum(24),
		"pem":            "-----BEGIN " + "PRIVATE KEY-----",
		"pem encrypted":  "-----BEGIN ENCRYPTED " + "PRIVATE KEY-----",
		"pgp":            "-----BEGIN PGP " + "PRIVATE KEY BLOCK-----",
		"jwt":            "eyJ" + alnum(12) + ".eyJ" + alnum(12) + ".sig",
	} {
		if _, ok := Scan("leaked " + sample + " here"); !ok {
			t.Errorf("%s: %q not recognized", name, sample)
		}
	}
}

// False positives block legitimate summaries, so ordinary text that merely
// resembles a prefix must pass.
func TestScanIgnoresOrdinaryText(t *testing.T) {
	for _, text := range []string{
		"switched to sk-learn for the classifier",
		"renamed the sk-something-long-css-class-name",
		"AIza prefix check added",
		"xoxo goodbye",
		"-----BEGIN PUBLIC KEY-----",
		"github_pat_ prefix documented",
	} {
		if p, ok := Scan(text); ok {
			t.Errorf("%q matched %s", text, p)
		}
	}
}
