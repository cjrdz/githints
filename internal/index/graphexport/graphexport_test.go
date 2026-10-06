package graphexport

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/index"
)

func graphNamed(names ...string) index.Graph {
	g := index.Graph{Version: index.GraphSchemaVersion}
	for _, n := range names {
		g.Nodes = append(g.Nodes, index.GraphNode{ID: n, Dir: "."})
	}
	for i := 1; i < len(names); i++ {
		g.Edges = append(g.Edges, index.GraphEdge{From: names[0], To: names[i]})
	}
	return g
}

var hostile = []string{
	"plain.go",
	"</script><script>alert(1)</script>.ts",
	`a"]; evil [label="x`,
	"line\nbreak.go",
	"back`tick`.go",
	`back\slash.go`,
	"<img src=x onerror=alert(1)>.py",
	"%% mermaid comment",
	"#quot; entity.go",
	" sep.js",
}

func render(t *testing.T, g index.Graph, format string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, g, format); err != nil {
		t.Fatalf("Write %s: %v", format, err)
	}
	return b.String()
}

// The HTML page must contain exactly the two script elements it means to, and
// the data block must parse back to the same names.
func TestHTMLCannotBeBrokenOutOf(t *testing.T) {
	g := graphNamed(hostile...)
	out := render(t, g, "html")
	if n := strings.Count(strings.ToLower(out), "</script"); n != 2 {
		t.Fatalf("expected 2 </script> closers, found %d", n)
	}
	if strings.Count(out, "<script") != 2 {
		t.Fatalf("unexpected script elements")
	}
	if !strings.Contains(out, "Content-Security-Policy") || !strings.Contains(out, "default-src 'none'") {
		t.Fatal("missing CSP")
	}
	m := regexp.MustCompile(`(?s)<script type="application/json" id="graph-data">(.*?)</script>`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("no data block")
	}
	var back index.Graph
	if err := json.Unmarshal([]byte(m[1]), &back); err != nil {
		t.Fatalf("data block is not JSON: %v", err)
	}
	for i, n := range back.Nodes {
		if n.ID != g.Nodes[i].ID {
			t.Errorf("node %d round-tripped as %q, want %q", i, n.ID, g.Nodes[i].ID)
		}
	}
}

// The CSP pins the inline script and style by hash; if the hash did not match
// the bytes on the page, the viewer would silently not run.
func TestHTMLCSPHashesMatch(t *testing.T) {
	out := render(t, graphNamed("a.go"), "html")
	script := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(out)[1]
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(out)[1]
	for name, body := range map[string]string{"script": script, "style": style} {
		if !strings.Contains(out, cspHash(body)) {
			t.Errorf("CSP does not pin the inline %s", name)
		}
	}
}

func TestDOTQuotesEveryID(t *testing.T) {
	out := render(t, graphNamed(hostile...), "dot")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "digraph") || line == "}" || strings.Contains(line, "rankdir") ||
			strings.HasPrefix(strings.TrimSpace(line), "node [") || strings.HasPrefix(strings.TrimSpace(line), "edge [") {
			continue
		}
		// Strip quoted strings (honoring backslash escapes); what remains must
		// be only an arrow, separators and the terminating semicolon.
		rest := regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).ReplaceAllString(line, "Q")
		if !regexp.MustCompile(`^\s*Q(\s*->\s*Q)?;$`).MatchString(rest) {
			t.Errorf("DOT line escapes its quoting: %q -> %q", line, rest)
		}
	}
}

func TestMermaidLabelsStayLabels(t *testing.T) {
	out := render(t, graphNamed(hostile...), "mermaid")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "flowchart LR" {
		t.Fatalf("header: %q", lines[0])
	}
	node := regexp.MustCompile(`^  n\d+\["[^"\x60<>\n]*"\]$`)
	edge := regexp.MustCompile(`^  n\d+ --> n\d+$`)
	for _, l := range lines[1:] {
		if !node.MatchString(l) && !edge.MatchString(l) {
			t.Errorf("unexpected Mermaid line %q", l)
		}
	}
	if len(lines) != 1+len(hostile)+len(hostile)-1 {
		t.Errorf("a label added or swallowed a line: %d lines", len(lines))
	}
}

func FuzzExportersEscape(f *testing.F) {
	for _, h := range hostile {
		f.Add(h)
	}
	f.Fuzz(func(t *testing.T, name string) {
		g := graphNamed("root.go", name)
		var b bytes.Buffer
		if err := Write(&b, g, "html"); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(strings.ToLower(b.String()), "</script"); n != 2 {
			t.Fatalf("name %q produced %d </script> closers", name, n)
		}
		b.Reset()
		if err := Write(&b, g, "mermaid"); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(b.String(), "\n"); n != 4 {
			t.Fatalf("name %q changed the Mermaid line count to %d", name, n)
		}
	})
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, graphNamed("a"), "svg"); err == nil {
		t.Fatal("unknown format accepted")
	}
}
