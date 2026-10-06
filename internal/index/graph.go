package index

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/index/lang"
)

// GraphNode is one importable unit: a file for languages where a file is a
// module (TypeScript, Python), a package directory for languages where many
// files share one import path (Go), or, with IncludeExternal, an import
// target that resolves to no indexed file (stdlib, third-party packages).
type GraphNode struct {
	ID        string   `json:"id"`
	Files     []string `json:"files,omitempty"` // members, when ID is a package`
	Language  string   `json:"language,omitempty"`
	Dir       string   `json:"dir"`
	Symbols   int      `json:"symbols"`
	Facets    []string `json:"facets,omitempty"`
	InDegree  int      `json:"in"`
	OutDegree int      `json:"out"`
	External  bool     `json:"external,omitempty"`
}

// GraphEdge says From imports To.
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the file-level dependency graph of the structural index.
type Graph struct {
	Version       int         `json:"version"` // schema version of this JSON
	LastIndexedAt int64       `json:"last_indexed_at"`
	Focus         string      `json:"focus,omitempty"`
	Depth         int         `json:"depth,omitempty"`
	TotalNodes    int         `json:"total_nodes"` // before MaxNodes was applied
	Truncated     bool        `json:"truncated"`
	Nodes         []GraphNode `json:"nodes"`
	Edges         []GraphEdge `json:"edges"`
}

// GraphSchemaVersion is bumped when the JSON shape changes incompatibly.
const GraphSchemaVersion = 1

// GraphOptions narrows a graph.
type GraphOptions struct {
	// Focus, when set, keeps only files within Depth import hops of it, in
	// either direction.
	Focus string
	Depth int
	// IncludeExternal adds nodes for import targets that resolve to no
	// indexed file.
	IncludeExternal bool
	// MaxNodes caps the node count; past it the highest-degree nodes are kept
	// (and the focus, always). Zero means DefaultMaxGraphNodes.
	MaxNodes int
}

// DefaultMaxGraphNodes keeps the default graph legible and the HTML viewer
// responsive. Larger repositories are better explored with Focus.
const DefaultMaxGraphNodes = 1500

// MaxGraphNodes is the hard ceiling on MaxNodes.
const MaxGraphNodes = 20000

// BuildGraph resolves the index's import edges (file -> import path) into a
// file -> file graph. Resolution is the same one the rendered notes use, so
// the graph and the notes' "Imported by" sections always agree.
func BuildGraph(db *Store, root string, opts GraphOptions) (Graph, error) {
	if opts.MaxNodes == 0 {
		opts.MaxNodes = DefaultMaxGraphNodes
	}
	if opts.MaxNodes < 1 || opts.MaxNodes > MaxGraphNodes {
		return Graph{}, fmt.Errorf("max nodes must be between 1 and %d, got %d", MaxGraphNodes, opts.MaxNodes)
	}
	if opts.Focus != "" && (opts.Depth < 1 || opts.Depth > 10) {
		return Graph{}, fmt.Errorf("depth must be between 1 and 10, got %d", opts.Depth)
	}

	registry := lang.NewRegistryForRoot(root)
	defer lang.BeginScans(registry.AllParsers(), root)()

	files, err := db.AllIndexedFiles()
	if err != nil {
		return Graph{}, fmt.Errorf("list indexed files: %w", err)
	}
	imports, err := db.AllImports()
	if err != nil {
		return Graph{}, err
	}
	symbolCounts, err := db.SymbolCountsByFile()
	if err != nil {
		return Graph{}, err
	}
	facets, err := db.Facets(FacetFilter{})
	if err != nil {
		return Graph{}, fmt.Errorf("load facets: %w", err)
	}
	lastIndexed, err := db.LastIndexedAt()
	if err != nil {
		return Graph{}, err
	}
	// Group files by the import path importers use. One file per path is a
	// file node. Several files per path is a package (Go): they become one
	// node named by their directory, since an import names the package and
	// pointing it at one arbitrary member file would be wrong.
	byImport := map[string][]string{}
	importOf := map[string]string{}
	for _, f := range files {
		ip, err := registry.ImportPath(root, f)
		if err != nil {
			continue
		}
		byImport[ip] = append(byImport[ip], f)
		importOf[f] = ip
	}
	nodeOf := make(map[string]string, len(files)) // file -> node id
	targetOf := map[string]string{}               // import path -> node id
	for ip, members := range byImport {
		sort.Strings(members)
		id := members[0]
		if len(members) > 1 {
			id = packageID(members[0])
		}
		targetOf[ip] = id
		for _, f := range members {
			nodeOf[f] = id
		}
	}
	for _, f := range files {
		if _, ok := nodeOf[f]; !ok {
			nodeOf[f] = f // no import path: nothing can import it, but it imports things
		}
	}

	nodes := make(map[string]*GraphNode, len(files))
	for _, f := range files {
		id := nodeOf[f]
		n, ok := nodes[id]
		if !ok {
			n = &GraphNode{ID: id, Dir: dirOf(f)}
			if p := registry.ForPath(f); p != nil {
				n.Language = p.Language()
			}
			nodes[id] = n
		}
		if id != f {
			n.Files = append(n.Files, f)
		}
		n.Symbols += symbolCounts[f]
	}
	facetSeen := map[string]bool{}
	for _, d := range facets {
		id, ok := nodeOf[d.FilePath]
		if !ok {
			continue
		}
		if k := id + "\x00" + d.Facet; !facetSeen[k] {
			facetSeen[k] = true
			nodes[id].Facets = append(nodes[id].Facets, d.Facet)
		}
	}

	type edgeKey struct{ from, to string }
	seen := map[edgeKey]bool{}
	var edges []GraphEdge
	for _, imp := range imports {
		from, ok := nodeOf[imp.FilePath]
		if !ok {
			continue
		}
		to, resolved := targetOf[imp.ImportedPath]
		if !resolved {
			if !opts.IncludeExternal {
				continue
			}
			to = "ext:" + imp.ImportedPath
			if _, ok := nodes[to]; !ok {
				nodes[to] = &GraphNode{ID: to, External: true}
			}
		}
		if to == from {
			continue // within one package
		}
		k := edgeKey{from, to}
		if seen[k] {
			continue
		}
		seen[k] = true
		edges = append(edges, GraphEdge{From: from, To: to})
		nodes[from].OutDegree++
		nodes[to].InDegree++
	}

	keep := make(map[string]bool, len(nodes))
	if opts.Focus != "" {
		// The focus may name a file inside a package node.
		id, ok := nodeOf[opts.Focus]
		if !ok {
			if _, isNode := nodes[opts.Focus]; !isNode {
				return Graph{}, fmt.Errorf("%s is not in the index", opts.Focus)
			}
			id = opts.Focus
		}
		opts.Focus = id
		adj := map[string][]string{}
		for _, e := range edges {
			adj[e.From] = append(adj[e.From], e.To)
			adj[e.To] = append(adj[e.To], e.From)
		}
		frontier := []string{opts.Focus}
		keep[opts.Focus] = true
		for hop := 0; hop < opts.Depth && len(frontier) > 0; hop++ {
			var next []string
			for _, id := range frontier {
				for _, nb := range adj[id] {
					if !keep[nb] {
						keep[nb] = true
						next = append(next, nb)
					}
				}
			}
			frontier = next
		}
	} else {
		for id := range nodes {
			keep[id] = true
		}
	}

	g := Graph{
		Version:       GraphSchemaVersion,
		LastIndexedAt: lastIndexed,
		Focus:         opts.Focus,
		TotalNodes:    len(keep),
	}
	if opts.Focus != "" {
		g.Depth = opts.Depth
	}

	if len(keep) > opts.MaxNodes {
		ids := make([]string, 0, len(keep))
		for id := range keep {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := nodes[ids[i]], nodes[ids[j]]
			if da, db := a.InDegree+a.OutDegree, b.InDegree+b.OutDegree; da != db {
				return da > db
			}
			return ids[i] < ids[j]
		})
		keep = map[string]bool{}
		if opts.Focus != "" {
			keep[opts.Focus] = true
		}
		for _, id := range ids {
			if len(keep) >= opts.MaxNodes {
				break
			}
			keep[id] = true
		}
		g.Truncated = true
	}

	for id := range keep {
		g.Nodes = append(g.Nodes, *nodes[id])
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	for _, e := range edges {
		if keep[e.From] && keep[e.To] {
			g.Edges = append(g.Edges, e)
		}
	}
	if g.Nodes == nil {
		g.Nodes = []GraphNode{}
	}
	if g.Edges == nil {
		g.Edges = []GraphEdge{}
	}
	return g, nil
}

// packageID names a multi-file package node by its directory, with a
// trailing slash so it never collides with a file path.
func packageID(member string) string {
	return dirOf(member) + "/"
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}
