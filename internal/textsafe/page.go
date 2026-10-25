package textsafe

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
)

// StaticPage builds one of githints' offline viewers (the dependency graph,
// the notes): markup, CSS, JS and the data in a single file that works from a
// file:// URL and, by its Content-Security-Policy, cannot fetch anything:
// default-src 'none', and the only script and style allowed are the exact
// bytes given, pinned by hash. Both viewers go through here so that policy
// exists once.
//
// data is JSON-encoded here, never by the caller, into a <script
// type="application/json"> block the browser does not execute.
// encoding/json escapes <, > and & (and U+2028/2029), so no file name or note
// can close that block; the viewer reads it with JSON.parse and puts strings
// on the page with textContent only.
func StaticPage(title, dataID, css, js, body string, data any) ([]byte, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode page data: %w", err)
	}
	csp := fmt.Sprintf("default-src 'none'; script-src %s; style-src %s; img-src data:; base-uri 'none'; form-action 'none'",
		cspHash(js), cspHash(css))
	return fmt.Appendf(nil, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="%s">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>%s</title>
<style>%s</style>
</head>
<body>
%s<script type="application/json" id="%s">%s</script>
<script>%s</script>
</body>
</html>
`, csp, html.EscapeString(title), css, body, html.EscapeString(dataID), payload, js), nil
}

func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}
