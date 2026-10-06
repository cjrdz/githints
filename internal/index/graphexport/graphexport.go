// Package graphexport writes the structural index's dependency graph
// (index.Graph) in formats other tools and people can use: JSON for scripts,
// Graphviz DOT, Mermaid for GitHub and docs, and a self-contained offline HTML
// viewer.
//
// Every node id is a path from the repository, and a repository is untrusted
// input, so each format escapes ids for its own syntax. A file named
// `"]; evil` or `</script>` must stay a label.
package graphexport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/textsafe"
)

// Formats lists the accepted -format values.
var Formats = []string{"html", "json", "dot", "mermaid"}

// MermaidSoftLimit is the node count past which a Mermaid diagram stops being
// readable (and GitHub may refuse to render it).
const MermaidSoftLimit = 300

// Write renders g in format to w.
func Write(w io.Writer, g index.Graph, format string) error {
	switch format {
	case "json":
		return writeJSON(w, g)
	case "dot":
		return writeDOT(w, g)
	case "mermaid":
		return writeMermaid(w, g)
	case "html":
		return writeHTML(w, g)
	}
	return fmt.Errorf("unknown format %q (want one of %s)", format, strings.Join(Formats, ", "))
}

func writeJSON(w io.Writer, g index.Graph) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(g)
}

// dotString quotes s as a DOT ID. Inside a quoted ID only `"` and a trailing
// backslash are special; control characters are flattened first.
func dotString(s string) string {
	s = textsafe.OneLine(s)
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// displayName is a node's label: external targets carry an "ext:" prefix in
// their id so they never collide with a file, which is not shown.
func displayName(n index.GraphNode) string {
	if n.External {
		return strings.TrimPrefix(n.ID, "ext:")
	}
	return n.ID
}

func writeDOT(w io.Writer, g index.Graph) error {
	var b strings.Builder
	b.WriteString("digraph githints {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=rounded, fontname=\"Helvetica\", fontsize=10];\n")
	b.WriteString("  edge [color=\"#898781\", arrowsize=0.6];\n")
	for _, n := range g.Nodes {
		attrs := ""
		if n.External {
			attrs = " [label=" + dotString(displayName(n)) + ", style=\"rounded,dashed\", fontcolor=\"#52514e\"]"
		}
		fmt.Fprintf(&b, "  %s%s;\n", dotString(n.ID), attrs)
	}
	for _, e := range g.Edges {
		fmt.Fprintf(&b, "  %s -> %s;\n", dotString(e.From), dotString(e.To))
	}
	b.WriteString("}\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// mermaidLabel makes s safe inside a quoted Mermaid label. Mermaid decodes
// #name; entities there, so the characters that would end the label or be
// read as markup (quotes, angle brackets, backticks for markdown strings) are
// written as entities.
func mermaidLabel(s string) string {
	return strings.NewReplacer(
		`#`, "#35;",
		`"`, "#quot;",
		`<`, "#lt;",
		`>`, "#gt;",
		"`", "#96;",
	).Replace(textsafe.OneLine(s))
}

func writeMermaid(w io.Writer, g index.Graph) error {
	ids := make(map[string]string, len(g.Nodes))
	order := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		order = append(order, n.ID)
	}
	sort.Strings(order)
	for i, id := range order {
		// Node ids are synthetic; the path only ever appears as a label.
		ids[id] = "n" + strconv.Itoa(i)
	}

	var b strings.Builder
	b.WriteString("flowchart LR\n")
	external := false
	for _, n := range g.Nodes {
		if n.External {
			fmt.Fprintf(&b, "  %s([\"%s\"])\n", ids[n.ID], mermaidLabel(displayName(n)))
			external = true
		} else {
			fmt.Fprintf(&b, "  %s[\"%s\"]\n", ids[n.ID], mermaidLabel(n.ID))
		}
	}
	for _, e := range g.Edges {
		fmt.Fprintf(&b, "  %s --> %s\n", ids[e.From], ids[e.To])
	}
	if g.Focus != "" {
		if id, ok := ids[g.Focus]; ok {
			fmt.Fprintf(&b, "  style %s stroke-width:3px\n", id)
		}
	}
	if external {
		b.WriteString("  %% rounded nodes are imports that resolve to no indexed file\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
