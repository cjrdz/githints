package graphexport

import (
	"embed"
	"fmt"
	"io"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/textsafe"
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

// writeHTML emits the graph viewer: one self-contained page whose
// Content-Security-Policy lets it fetch nothing (see textsafe.StaticPage).
func writeHTML(w io.Writer, g index.Graph) error {
	var css, js, body string
	for name, dst := range map[string]*string{"viewer.css": &css, "viewer.js": &js, "viewer.html": &body} {
		var err error
		if *dst, err = asset(name); err != nil {
			return err
		}
	}
	page, err := textsafe.StaticPage("githints dependency graph", "graph-data", css, js, body, g)
	if err != nil {
		return err
	}
	_, err = w.Write(page)
	return err
}
