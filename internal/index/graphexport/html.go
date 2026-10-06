package graphexport

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cjrdz/githints/internal/index"
)

//go:embed viewer/viewer.html viewer/viewer.css viewer/viewer.js
var viewerFS embed.FS

func asset(name string) (string, error) {
	b, err := viewerFS.ReadFile("viewer/" + name)
	if err != nil {
		return "", fmt.Errorf("embedded viewer asset %s: %w", name, err)
	}
	return string(b), nil
}

func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// writeHTML emits one self-contained page: markup, CSS, JS and the graph as
// data. It works offline from a file:// URL and, by its Content-Security-
// Policy, cannot fetch anything at all: default-src 'none', and the only
// script and style allowed are the exact bytes below, pinned by hash.
//
// The graph is embedded in a <script type="application/json"> block, which the
// browser never executes. encoding/json escapes <, > and & (and U+2028/2029),
// so no file name can close that block. The viewer reads it with
// JSON.parse and puts strings on the page with textContent only.
func writeHTML(w io.Writer, g index.Graph) error {
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encode graph: %w", err)
	}
	var css, js, body string
	for name, dst := range map[string]*string{"viewer.css": &css, "viewer.js": &js, "viewer.html": &body} {
		if *dst, err = asset(name); err != nil {
			return err
		}
	}
	csp := fmt.Sprintf("default-src 'none'; script-src %s; style-src %s; img-src data:; base-uri 'none'; form-action 'none'",
		cspHash(js), cspHash(css))
	_, err = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="%s">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>githints dependency graph</title>
<style>%s</style>
</head>
<body>
%s<script type="application/json" id="graph-data">%s</script>
<script>%s</script>
</body>
</html>
`, csp, css, body, data, js)
	return err
}
