package lang

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/safefs"
)

// Manifests answers "which packages does the project around this file
// declare", for detectors gated by when_dependencies.
//
// A file's dependencies are the union of every manifest in its directory and
// each directory above it, up to the repository root: in a monorepo a package
// may list react while the root lists the tooling, and a workspace can hoist
// either. Results are cached per directory for the scan.
//
// Manifests come from the repository, which may be a hostile clone, so each is
// read through safefs (no links out of the repository, size-capped) and parsed
// leniently: a manifest that does not parse contributes nothing rather than
// failing the scan.
//
// Each directory is read once, under its own sync.Once: workers asking about
// different directories read them concurrently, and a worker asking about a
// directory already being read waits for that one result. A single lock held
// across the reads had workers queueing for each other's I/O.
type Manifests struct {
	root     string
	fsysOnce sync.Once
	fsys     *os.Root // opened on first listing, closed by Close
	fsysErr  error
	mu       sync.Mutex            // guards the two maps, not the reads
	own      map[string]*dirResult // dir -> dependencies declared in that dir
	union    map[string]*dirResult // dir -> dependencies at or above it

	// walked holds, after a full scan's walk, the manifest files of every
	// directory the walk visited (complete). A directory not in it has
	// none, and no lookup lists a directory again: listing a nested
	// directory through an os.Root resolves every component, which was most
	// of the cost of a dependency lookup. Written only before lookups start.
	walked   map[string][]string
	complete bool
}

// dirResult is one directory's answer, computed once.
type dirResult struct {
	once sync.Once
	deps []string
}

// ManifestFiles are the files Manifests reads, by base name. A requirements
// file also counts when named requirements-<x>.txt or requirements_<x>.txt,
// and any <x>.csproj is a .NET project file.
var ManifestFiles = []string{
	"package.json", "composer.json", "pyproject.toml", "requirements.txt", "go.mod", "Cargo.toml",
	"Gemfile", "pubspec.yaml", "vcpkg.json", "conanfile.txt", "conanfile.py", "CMakeLists.txt",
}

// IsManifest reports whether a repo-relative path is a file Manifests reads,
// so the hook can tell when a commit changed what detectors see.
func IsManifest(rel string) bool {
	base := path.Base(filepath.ToSlash(rel))
	for _, name := range ManifestFiles {
		if base == name {
			return true
		}
	}
	return isRequirementsFile(base) || isProjectFile(base)
}

func isProjectFile(base string) bool {
	return strings.HasSuffix(base, ".csproj") && len(base) > len(".csproj")
}

func isRequirementsFile(base string) bool {
	if !strings.HasSuffix(base, ".txt") {
		return false
	}
	stem := strings.TrimSuffix(base, ".txt")
	return stem == "requirements" || strings.HasPrefix(stem, "requirements-") || strings.HasPrefix(stem, "requirements_")
}

// NewManifests returns an empty cache for the repository at root.
func NewManifests(root string) *Manifests {
	return &Manifests{root: root, own: map[string]*dirResult{}, union: map[string]*dirResult{}}
}

// DependenciesFor returns the dependencies visible to a repo-relative file.
// The returned slice is shared; callers must not modify it.
func (m *Manifests) DependenciesFor(rel string) []string {
	if m == nil {
		return nil
	}
	return m.unionFor(path.Dir(filepath.ToSlash(rel)))
}

// result returns the entry for dir in table, creating it.
func (m *Manifests) result(table map[string]*dirResult, dir string) *dirResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := table[dir]
	if !ok {
		r = &dirResult{}
		table[dir] = r
	}
	return r
}

func (m *Manifests) unionFor(dir string) []string {
	r := m.result(m.union, dir)
	r.once.Do(func() { r.deps = m.computeUnion(dir) })
	return r.deps
}

func (m *Manifests) computeUnion(dir string) []string {
	var parent []string
	if dir != "." && dir != "/" && dir != "" {
		parent = m.unionFor(path.Dir(dir))
	}
	own := m.ownFor(dir)
	var deps []string
	if len(own) == 0 {
		deps = parent
	} else {
		seen := make(map[string]bool, len(own)+len(parent))
		for _, list := range [][]string{own, parent} {
			for _, d := range list {
				if !seen[d] {
					seen[d] = true
					deps = append(deps, d)
				}
			}
		}
		sort.Strings(deps)
	}
	return deps
}

func (m *Manifests) ownFor(dir string) []string {
	r := m.result(m.own, dir)
	r.once.Do(func() { r.deps = m.readOwn(dir) })
	return r.deps
}

func (m *Manifests) readOwn(dir string) []string {
	var deps []string
	for _, name := range m.manifestsIn(dir) {
		data, err := safefs.ReadRepoFile(m.root, filepath.FromSlash(path.Join(dir, name)), maxProjectFileBytes)
		if err != nil {
			continue
		}
		deps = append(deps, parseManifest(name, data)...)
	}
	return deps
}

// Seen records a manifest file a walk found (a regular file, not a link).
// Call it for every one before MarkWalked, and both before any lookup.
func (m *Manifests) Seen(rel string) {
	rel = filepath.ToSlash(rel)
	if m.walked == nil {
		m.walked = map[string][]string{}
	}
	dir := path.Dir(rel)
	m.walked[dir] = append(m.walked[dir], path.Base(rel))
}

// MarkWalked says every manifest of the walked tree has been Seen, so a
// directory with none needs no listing.
func (m *Manifests) MarkWalked() { m.complete = true }

// manifestsIn lists the manifest files present in dir, from one listing of
// it: probing every manifest name separately was eight Lstats per directory.
// After a walk it lists nothing at all.
// A directory listing that reaches the cap may have left a manifest out, so
// then the fixed names are probed as well.
func (m *Manifests) manifestsIn(dir string) []string {
	if m.complete {
		out := append([]string(nil), m.walked[dir]...)
		sort.Strings(out)
		return out
	}
	names, err := m.listDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	consider := func(name string) {
		if !seen[name] && IsManifest(name) && fileExists(m.root, path.Join(dir, name)) {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, name := range names {
		consider(name)
	}
	if len(names) >= maxManifestDirEntries {
		for _, name := range ManifestFiles {
			consider(name)
		}
	}
	sort.Strings(out)
	return out
}

// listDir lists a repository directory through the scan's os.Root, so a
// directory link cannot lead the listing outside the repository.
func (m *Manifests) listDir(dir string) ([]string, error) {
	m.fsysOnce.Do(func() { m.fsys, m.fsysErr = os.OpenRoot(m.root) })
	if m.fsysErr != nil {
		return nil, m.fsysErr
	}
	f, err := m.fsys.Open(filepath.FromSlash(dir))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(maxManifestDirEntries)
	if err != nil && len(names) == 0 {
		return nil, err
	}
	return names, nil
}

// Close releases the root opened for listings, once no lookup is running.
// A nil Manifests is fine.
func (m *Manifests) Close() error {
	if m == nil {
		return nil
	}
	// Running the Once here, if nothing ran it, means no root opens later.
	m.fsysOnce.Do(func() {})
	if m.fsys == nil {
		return nil
	}
	err := m.fsys.Close()
	m.fsys = nil
	return err
}

// maxManifestDirEntries bounds the directory listing that finds
// requirements-*.txt files; a directory with more is not a project root.
const maxManifestDirEntries = 4096

// parseManifest extracts dependency names. Names are compared the way their
// ecosystem compares them: Python's are lower-cased with "_" read as "-".
func parseManifest(name string, data []byte) []string {
	switch {
	case name == "package.json":
		return jsonKeys(data, "dependencies", "devDependencies", "peerDependencies", "optionalDependencies")
	case name == "composer.json":
		return jsonKeys(data, "require", "require-dev")
	case name == "pyproject.toml":
		return pyprojectDeps(data)
	case isRequirementsFile(name):
		return requirementsDeps(data)
	case name == "go.mod":
		return goModDeps(data)
	case name == "Cargo.toml":
		return cargoDeps(data)
	case isProjectFile(name):
		return csprojDeps(data)
	case name == "Gemfile":
		return gemfileDeps(data)
	case name == "pubspec.yaml":
		return pubspecDeps(data)
	case name == "vcpkg.json":
		return vcpkgDeps(data)
	case name == "conanfile.txt":
		return conanTxtDeps(data)
	case name == "conanfile.py":
		return conanPyDeps(data)
	case name == "CMakeLists.txt":
		return cmakeDeps(data)
	}
	return nil
}

func jsonKeys(data []byte, fields ...string) []string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []string
	for _, f := range fields {
		var deps map[string]json.RawMessage
		if json.Unmarshal(doc[f], &deps) != nil {
			continue
		}
		for k := range deps {
			out = append(out, k)
		}
	}
	return out
}

// pythonName normalizes a requirement specifier to its distribution name:
// "SQLAlchemy[asyncio]>=2.0; python_version>'3.8'" is "sqlalchemy".
func pythonName(spec string) string {
	spec = strings.TrimSpace(spec)
	end := 0
	for end < len(spec) {
		c := spec[end]
		if c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			end++
			continue
		}
		break
	}
	return strings.ReplaceAll(strings.ToLower(spec[:end]), "_", "-")
}

func requirementsDeps(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if name := pythonName(line); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// pyprojectDeps reads the dependency lists of a pyproject.toml: PEP 621's
// [project] dependencies and optional-dependencies, PEP 735's
// [dependency-groups], and Poetry's [tool.poetry.*dependencies] tables.
//
// This is a line scanner for those shapes, not a TOML parser, in the same
// spirit as the notes front matter: the subset is small and a full parser
// would be a dependency for one lookup.
func pyprojectDeps(data []byte) []string {
	var out []string
	addAll := func(text string) {
		for _, q := range quotedStrings(text) {
			if name := pythonName(q); name != "" {
				out = append(out, name)
			}
		}
	}
	table := ""
	inArray := false // inside a dependency array that spans lines
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := commentStart(line); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		bare := withoutQuoted(line)
		if line == "" {
			continue
		}
		if inArray {
			addAll(line)
			if strings.Contains(bare, "]") {
				inArray = false
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		switch {
		case table == "tool.poetry.dependencies" || table == "tool.poetry.dev-dependencies" ||
			strings.HasPrefix(table, "tool.poetry.group.") && strings.HasSuffix(table, ".dependencies"):
			if name := pythonName(key); name != "" && name != "python" {
				out = append(out, name)
			}
		case table == "project" && key == "dependencies",
			table == "project.optional-dependencies",
			table == "dependency-groups":
			addAll(value)
			bareValue := withoutQuoted(value)
			inArray = strings.Contains(bareValue, "[") && !strings.Contains(bareValue, "]")
		}
	}
	return out
}

// commentStart returns the index of a '#' outside any string, or -1.
func commentStart(line string) int {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return i
		}
	}
	return -1
}

// withoutQuoted returns line with every "..." and '...' removed, so brackets
// and '#' can be looked for outside strings.
func withoutQuoted(line string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// quotedStrings returns the contents of every "..." or '...' on a line.
func quotedStrings(line string) []string {
	var out []string
	for {
		i := strings.IndexAny(line, `"'`)
		if i < 0 {
			return out
		}
		q := line[i]
		j := strings.IndexByte(line[i+1:], q)
		if j < 0 {
			return out
		}
		out = append(out, line[i+1:i+1+j])
		line = line[i+j+2:]
	}
}

func goModDeps(data []byte) []string {
	var out []string
	inBlock := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "require (":
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// csprojPattern finds the SDK a project builds with (Microsoft.NET.Sdk.Web is
// ASP.NET Core) and every PackageReference. A regular expression over the XML
// rather than a parser: the two shapes are fixed, and an MSBuild file is not
// otherwise interpreted.
var csprojPattern = regexp.MustCompile(`(?i)<Project\s[^>]*\bSdk="([^"]+)"|<PackageReference\s[^>]*\bInclude="([^"]+)"`)

func csprojDeps(data []byte) []string {
	var out []string
	for _, m := range csprojPattern.FindAllSubmatch(data, -1) {
		for _, g := range m[1:] {
			if len(g) > 0 {
				out = append(out, string(g))
			}
		}
	}
	return out
}

// gemfilePattern finds `gem "rails", "~> 7.1"` lines.
var gemfilePattern = regexp.MustCompile(`(?m)^\s*gem\s+["']([^"']+)["']`)

func gemfileDeps(data []byte) []string {
	var out []string
	for _, m := range gemfilePattern.FindAllSubmatch(data, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// pubspecDeps reads the keys of the top-level dependencies and
// dev_dependencies maps of a pubspec.yaml: a line scanner over the one shape
// pub writes, not a YAML parser.
func pubspecDeps(data []byte) []string {
	var out []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			key := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			in = key == "dependencies" || key == "dev_dependencies"
			continue
		}
		if !in {
			continue
		}
		// Only the map's own keys, one level in: "  go_router: ^14.0.0".
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) != 2 {
			continue
		}
		if key, _, ok := strings.Cut(trimmed, ":"); ok && key != "" {
			out = append(out, key)
		}
	}
	return out
}

func cargoDeps(data []byte) []string {
	var out []string
	table := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[] ")
			continue
		}
		if table != "dependencies" && table != "dev-dependencies" && table != "workspace.dependencies" {
			continue
		}
		if key, _, ok := strings.Cut(line, "="); ok {
			if name := strings.Trim(strings.TrimSpace(key), `"'`); name != "" && !strings.HasPrefix(name, "#") {
				out = append(out, name)
			}
		}
	}
	return out
}

// vcpkgDeps reads vcpkg.json's dependencies, each a name or {"name": ...}.
func vcpkgDeps(data []byte) []string {
	var doc struct {
		Dependencies []json.RawMessage `json:"dependencies"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []string
	for _, raw := range doc.Dependencies {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			var obj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &obj) != nil {
				continue
			}
			name = obj.Name
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// conanTxtDeps reads the [requires] sections of a conanfile.txt, whose lines
// are references: fmt/10.2.1, or zlib/1.3@user/channel.
func conanTxtDeps(data []byte) []string {
	var out []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section := strings.Trim(line, "[] ")
			in = section == "requires" || section == "tool_requires" || section == "test_requires" || section == "build_requires"
			continue
		}
		if !in || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, "/"); ok && name != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

// conanReference finds a quoted Conan reference, "fmt/10.2.1".
var conanReference = regexp.MustCompile(`["']([A-Za-z0-9_.+-]+)/[^"'\s]+["']`)

// conanPyDeps reads the references on a conanfile.py's requires lines:
// requires = "fmt/10.2.1", "zlib/1.3", or self.requires("fmt/10.2.1").
func conanPyDeps(data []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "requires") {
			continue
		}
		for _, m := range conanReference.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// cmakePackage finds the packages a CMakeLists.txt pulls in by name:
// find_package(Qt6 ...), FetchContent_Declare(crow ...), and CPM's
// CPMAddPackage(NAME x ...) or CPMAddPackage("gh:CrowCpp/Crow@1.2").
var cmakePackage = regexp.MustCompile(`(?im)^\s*(?:find_package|FetchContent_Declare)\s*\(\s*([\w.+-]+)|^\s*CPMAddPackage\s*\(\s*(?:NAME\s+([\w.+-]+)|"[a-z]+:[\w.-]+/([\w.+-]+?)(?:@|#|"))`)

func cmakeDeps(data []byte) []string {
	var out []string
	for _, m := range cmakePackage.FindAllSubmatch(data, -1) {
		for _, g := range m[1:] {
			if len(g) > 0 {
				out = append(out, string(g))
			}
		}
	}
	return out
}
