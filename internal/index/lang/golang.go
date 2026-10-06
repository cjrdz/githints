package lang

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/safefs"
)

// GoParser uses the standard library go/parser to extract top-level symbols and
// imports. It is the only supported parser in v1, per the structural index plan.
type GoParser struct{}

func (GoParser) Language() string { return "go" }

func (GoParser) Extensions() []string { return []string{".go"} }

func (GoParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		// ParseFile can return a partial file on syntax errors; if it did, we
		// still try to extract what we can. Otherwise we report the error.
		if f == nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}

	var symbols []Symbol
	var imports []Import

	for _, imp := range f.Imports {
		pathVal := strings.Trim(imp.Path.Value, `"`)
		if pathVal == "" {
			continue
		}
		imports = append(imports, Import{FilePath: path, ImportedPath: pathVal})
	}

	for _, decl := range f.Decls {
		pos := decl.Pos()
		end := decl.End()
		startLine := fset.Position(pos).Line
		endLine := fset.Position(end).Line

		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			kind := KindFunc
			sig := funcSignature(d)
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = KindMethod
			}
			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				FilePath:  path,
				LineStart: startLine,
				LineEnd:   endLine,
				Signature: sig,
			})

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				pos := spec.Pos()
				end := spec.End()
				startLine := fset.Position(pos).Line
				endLine := fset.Position(end).Line

				switch s := spec.(type) {
				case *ast.TypeSpec:
					symbols = append(symbols, Symbol{
						Name:      s.Name.Name,
						Kind:      KindType,
						FilePath:  path,
						LineStart: startLine,
						LineEnd:   endLine,
						Signature: typeSpecSignature(s),
					})
				case *ast.ValueSpec:
					kind := KindVar
					if d.Tok == token.CONST {
						kind = KindConst
					}
					for _, n := range s.Names {
						symbols = append(symbols, Symbol{
							Name:      n.Name,
							Kind:      kind,
							FilePath:  path,
							LineStart: startLine,
							LineEnd:   endLine,
							Signature: valueSpecSignature(d.Tok, s),
						})
					}
				}
			}
		}
	}

	return symbols, imports, nil
}

func funcSignature(d *ast.FuncDecl) string {
	var b strings.Builder
	if d.Recv != nil && len(d.Recv.List) > 0 {
		b.WriteString("func ")
		b.WriteString(receiverString(d.Recv.List[0]))
		b.WriteString(" ")
	} else {
		b.WriteString("func ")
	}
	b.WriteString(d.Name.Name)
	b.WriteString(fieldListString(d.Type.Params))
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		b.WriteString(" ")
		b.WriteString(fieldListString(d.Type.Results))
	}
	return b.String()
}

func receiverString(f *ast.Field) string {
	var b strings.Builder
	if len(f.Names) > 0 {
		b.WriteString(f.Names[0].Name)
		b.WriteString(" ")
	}
	b.WriteString(exprString(f.Type))
	return b.String()
}

func fieldListString(fl *ast.FieldList) string {
	if fl == nil {
		return "()"
	}
	var b strings.Builder
	b.WriteString("(")
	for i, f := range fl.List {
		if i > 0 {
			b.WriteString(", ")
		}
		if len(f.Names) > 0 {
			for j, n := range f.Names {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(n.Name)
			}
			b.WriteString(" ")
		}
		b.WriteString(exprString(f.Type))
	}
	b.WriteString(")")
	return b.String()
}

func typeSpecSignature(s *ast.TypeSpec) string {
	if s.TypeParams != nil && len(s.TypeParams.List) > 0 {
		return fmt.Sprintf("type %s%s %s", s.Name.Name, fieldListString(s.TypeParams), exprString(s.Type))
	}
	return fmt.Sprintf("type %s %s", s.Name.Name, exprString(s.Type))
}

func valueSpecSignature(tok token.Token, s *ast.ValueSpec) string {
	prefix := "var"
	if tok == token.CONST {
		prefix = "const"
	}
	var names []string
	for _, n := range s.Names {
		names = append(names, n.Name)
	}
	if s.Type != nil {
		return fmt.Sprintf("%s %s %s", prefix, strings.Join(names, ", "), exprString(s.Type))
	}
	return fmt.Sprintf("%s %s", prefix, strings.Join(names, ", "))
}

func exprString(e ast.Expr) string {
	if e == nil {
		return ""
	}
	// Cheap textual reconstruction of the expression. The signature is a human
	// hint, not compiler output.
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.ArrayType:
		if x.Len == nil {
			return "[]" + exprString(x.Elt)
		}
		return fmt.Sprintf("[%s]%s", exprString(x.Len), exprString(x.Elt))
	case *ast.MapType:
		return fmt.Sprintf("map[%s]%s", exprString(x.Key), exprString(x.Value))
	case *ast.ChanType:
		switch x.Dir {
		case ast.SEND:
			return "chan<- " + exprString(x.Value)
		case ast.RECV:
			return "<-chan " + exprString(x.Value)
		default:
			return "chan " + exprString(x.Value)
		}
	case *ast.FuncType:
		var b strings.Builder
		b.WriteString("func")
		b.WriteString(fieldListString(x.Params))
		if x.Results != nil && len(x.Results.List) > 0 {
			b.WriteString(" ")
			b.WriteString(fieldListString(x.Results))
		}
		return b.String()
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + exprString(x.Sel)
	case *ast.Ellipsis:
		return "..." + exprString(x.Elt)
	case *ast.ParenExpr:
		return "(" + exprString(x.X) + ")"
	case *ast.IndexExpr:
		return exprString(x.X) + "[" + exprString(x.Index) + "]"
	case *ast.IndexListExpr:
		var b strings.Builder
		b.WriteString(exprString(x.X))
		b.WriteString("[")
		for i, idx := range x.Indices {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(exprString(idx))
		}
		b.WriteString("]")
		return b.String()
	case *ast.BasicLit:
		return x.Value
	case *ast.UnaryExpr:
		return x.Op.String() + exprString(x.X)
	case *ast.BinaryExpr:
		return exprString(x.X) + " " + x.Op.String() + " " + exprString(x.Y)
	case *ast.KeyValueExpr:
		return exprString(x.Key) + ": " + exprString(x.Value)
	case *ast.CompositeLit:
		return exprString(x.Type) + "{...}"
	case *ast.TypeAssertExpr:
		return exprString(x.X) + ".(" + exprString(x.Type) + ")"
	default:
		return "..."
	}
}

// goModule is the module a directory belongs to: the nearest go.mod at or
// above it, within the repository.
type goModule struct {
	dir  string // repo-relative directory holding go.mod ("." for the root)
	path string // its module directive
	err  error
}

// activeGoModules caches, per directory, the module it belongs to, for the
// duration of a scan.
//
// resolveImportPaths asks for the import path of every indexed file; without
// a cache, each answer re-read go.mod. It is scoped to a scan through
// BeginScan rather than cached forever, so a go.mod edited between scans is
// picked up.
var activeGoModules struct {
	mu     sync.Mutex
	active bool
	root   string
	byDir  map[string]goModule
}

// BeginScan starts a per-scan cache of directory -> module.
func (GoParser) BeginScan(root string) func() {
	activeGoModules.mu.Lock()
	activeGoModules.active = true
	activeGoModules.root = root
	activeGoModules.byDir = map[string]goModule{}
	activeGoModules.mu.Unlock()

	return func() {
		activeGoModules.mu.Lock()
		activeGoModules.active = false
		activeGoModules.byDir = nil
		activeGoModules.mu.Unlock()
	}
}

// moduleFor returns the module containing repo-relative directory dir: the
// nearest go.mod at or above it. A monorepo can hold several modules (nested
// go.mod files, usually tied together by go.work), and a file belongs to the
// innermost one; using only the root go.mod gave every nested module's files
// the wrong import path, or none at all when the root had no go.mod.
func moduleFor(root, dir string) goModule {
	dir = path.Clean(filepath.ToSlash(dir))
	activeGoModules.mu.Lock()
	cached := activeGoModules.active && activeGoModules.root == root
	if cached {
		if m, ok := activeGoModules.byDir[dir]; ok {
			activeGoModules.mu.Unlock()
			return m
		}
	}
	activeGoModules.mu.Unlock()

	var m goModule
	modPath, err := readModulePathAt(root, dir)
	switch {
	case err == nil:
		m = goModule{dir: dir, path: modPath}
	case !errors.Is(err, fs.ErrNotExist):
		// A go.mod that exists but cannot be read or has no module line is
		// an answer for this directory, not a reason to look further up.
		m = goModule{dir: dir, err: err}
	case dir == ".":
		m = goModule{err: fmt.Errorf("no go.mod at or above this file: %w", err)}
	default:
		m = moduleFor(root, path.Dir(dir))
	}

	if cached {
		activeGoModules.mu.Lock()
		if activeGoModules.active && activeGoModules.root == root {
			activeGoModules.byDir[dir] = m
		}
		activeGoModules.mu.Unlock()
	}
	return m
}

// ImportPath maps a Go file back to the package path importers write: the
// path of the module it belongs to, joined with the file's directory relative
// to that module.
func (GoParser) ImportPath(root, file string) (string, error) {
	dir := path.Dir(filepath.ToSlash(file))
	m := moduleFor(root, dir)
	if m.err != nil {
		return "", fmt.Errorf("read module path: %w", m.err)
	}
	if dir == m.dir {
		return m.path, nil
	}
	rel := dir
	if m.dir != "." {
		rel = strings.TrimPrefix(dir, m.dir+"/")
	}
	return m.path + "/" + rel, nil
}

// readModulePathAt returns the module directive of dir/go.mod. A missing file
// is reported as fs.ErrNotExist so the caller can look in the parent.
func readModulePathAt(root, dir string) (string, error) {
	rel := path.Join(dir, "go.mod")
	data, err := safefs.ReadRepoFile(root, filepath.FromSlash(rel), maxProjectFileBytes)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`), nil
		}
	}
	return "", fmt.Errorf("no module directive found in %s", rel)
}

// goBlanker exposes Go's lexical surface for post-passes that match lines.
//
// GoParser itself has no use for it -- go/parser gives exact structure -- but
// framework detection runs over blanked lines for every language, and without
// this Go files would be the one place a detector could match a declaration
// that exists only inside a comment or a string.
var goBlanker = NewBlanker(BlankSpec{
	LineComments:  []string{"//"},
	BlockComments: [][2]string{{"/*", "*/"}},
	Strings: []StringRule{
		{Open: `"`, Close: `"`, Escape: '\\'},
		{Open: "'", Close: "'", Escape: '\\'},
		// Raw strings span lines and honour no escapes at all.
		{Open: "`", Close: "`", Multiline: true},
	},
})

// BlankLines returns the comment- and string-free view of a Go file.
func (GoParser) BlankLines(src []byte) []string { return goBlanker.Blank(src) }

// BlankLinesKeepingStrings returns the same view with string contents intact,
// which detectors need: a route path or a struct tag lives inside a string.
func (GoParser) BlankLinesKeepingStrings(src []byte) []string {
	return goBlanker.BlankKeepingStrings(src)
}
