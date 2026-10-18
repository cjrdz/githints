// Package graph implements the index graph CLI command and the graph-page
// refresh helper used by the hook path and the index command.
package graph

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/graphexport"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/safefs"
)

// pageName is the default viewer location under .githints/.
const pageName = "graph.html"

// Run exports the index's file dependency graph. The default is a single
// offline HTML page under .githints/, a graph view that needs no server.
func Run(args []string) error {
	fset := flag.NewFlagSet("index graph", flag.ExitOnError)
	format := fset.String("format", "html", "output format: "+strings.Join(graphexport.Formats, ", "))
	out := fset.String("o", "", "output file, or - for stdout (default: .githints/graph.html for html, stdout otherwise)")
	focus := fset.String("focus", "", "only files within -depth imports of this repo-relative file")
	depth := fset.Int("depth", 2, "import hops around -focus (1-10)")
	external := fset.Bool("external", false, "include imports that resolve to no indexed file (stdlib, packages)")
	maxNodes := fset.Int("max-nodes", index.DefaultMaxGraphNodes, fmt.Sprintf("keep at most this many of the most connected files (1-%d)", index.MaxGraphNodes))
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fset.Arg(0))
	}
	if *focus != "" {
		if err := recorder.ValidateFilePath(*focus); err != nil {
			return err
		}
	}

	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}
	dbPath := lang.IndexDBPath(root)
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return errors.New("no structural index yet; run `githints index` first")
	}
	db, err := index.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	opts := index.GraphOptions{Focus: filepath.ToSlash(*focus), IncludeExternal: *external, MaxNodes: *maxNodes}
	if *focus != "" {
		opts.Depth = *depth
	}
	g, err := index.BuildGraph(db, root, opts)
	if err != nil {
		return err
	}
	if g.Truncated {
		fmt.Fprintf(os.Stderr, "githints: kept the %d most connected of %d files (-max-nodes); use -focus to explore the rest\n", len(g.Nodes), g.TotalNodes)
	}
	if *format == "mermaid" && len(g.Nodes) > graphexport.MermaidSoftLimit {
		fmt.Fprintf(os.Stderr, "githints: %d nodes is more than Mermaid renders legibly (or GitHub renders at all); try -focus=FILE -depth=1\n", len(g.Nodes))
	}

	var buf bytes.Buffer
	if err := graphexport.Write(&buf, g, *format); err != nil {
		return err
	}

	switch {
	case *out == "-" || (*out == "" && *format != "html"):
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	case *out == "":
		if err := writePage(root, buf.Bytes()); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d nodes, %d imports); open it in any browser, it works offline\n",
			filepath.Join(root, ".githints", pageName), len(g.Nodes), len(g.Edges))
		return nil
	default:
		// An explicit -o is the user's own choice of path.
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
		return nil
	}
}

// writePage writes the viewer through safefs, like every other write under
// .githints/.
func writePage(root string, page []byte) error {
	return safefs.WriteFile(filepath.Join(root, ".githints"), pageName, page, safefs.StateDirPerm, 0o644)
}

// RefreshPage rebuilds .githints/graph.html with default options. It runs
// after a full or incremental index when index.graph_html is on, and only
// warns on failure: the index itself was updated.
func RefreshPage(root string, db *index.Store) {
	g, err := index.BuildGraph(db, root, index.GraphOptions{})
	if err == nil {
		var buf bytes.Buffer
		if err = graphexport.Write(&buf, g, "html"); err == nil {
			err = writePage(root, buf.Bytes())
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not refresh %s: %v\n", pageName, err)
	}
}
