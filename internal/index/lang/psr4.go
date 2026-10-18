package lang

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/safefs"
)

// PHP has no file-based imports: `use App\Models\User;` names a class, and
// composer's PSR-4 autoload map says which directory a namespace prefix lives
// in. Without it PHP files had symbols and outbound imports but no edges, so
// nothing in a Laravel or Symfony project was ever "imported by" anything.

// psr4Mapping is one prefix -> directory entry, the directory relative to the
// composer.json that declares it.
type psr4Mapping struct {
	prefix string // without the trailing backslash; "" for a fallback dir
	dir    string // "" for the project root
}

type psr4Entry struct {
	size     int64
	modTime  int64
	mappings []psr4Mapping
}

// psr4Cache holds parsed composer.json files by absolute path. Entries are
// re-read when the file's size or modification time changes, because the MCP
// server lives across edits to composer.json.
var psr4Cache = struct {
	mu      sync.Mutex
	entries map[string]psr4Entry
}{entries: map[string]psr4Entry{}}

// psr4Key returns the class name a repo-relative PHP file is imported by,
// from the nearest composer.json at or above it.
func psr4Key(root, file string) (string, error) {
	if path.Ext(file) != ".php" {
		return "", fmt.Errorf("%s is not a .php file", file)
	}
	dir := path.Dir(file)
	for {
		composer := path.Join(dir, "composer.json")
		if fileExists(root, composer) {
			rel := strings.TrimPrefix(file, dir+"/")
			if dir == "." {
				rel = file
			}
			if key, ok := psr4Resolve(psr4Mappings(root, composer), rel); ok {
				return key, nil
			}
			// A nested package's composer.json may not map this file while
			// an outer one does; keep looking.
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", fmt.Errorf("no composer.json autoload maps %s", file)
		}
		dir = path.Dir(dir)
	}
}

// psr4Resolve maps a file relative to its composer.json through the mappings,
// the longest matching directory winning, as composer's own loader does.
func psr4Resolve(mappings []psr4Mapping, rel string) (string, bool) {
	best := -1
	for i, m := range mappings {
		if m.dir != "" && !strings.HasPrefix(rel, m.dir+"/") {
			continue
		}
		if best < 0 || len(m.dir) > len(mappings[best].dir) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	m := mappings[best]
	rest := strings.TrimSuffix(rel, ".php")
	if m.dir != "" {
		rest = strings.TrimPrefix(rest, m.dir+"/")
	}
	class := strings.ReplaceAll(rest, "/", "\\")
	if m.prefix == "" {
		return class, true
	}
	return m.prefix + "\\" + class, true
}

// psr4Mappings reads the autoload and autoload-dev psr-4 maps of a
// composer.json, cached by size and modification time.
func psr4Mappings(root, composer string) []psr4Mapping {
	abs := filepath.Join(root, filepath.FromSlash(composer))
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	psr4Cache.mu.Lock()
	e, ok := psr4Cache.entries[abs]
	psr4Cache.mu.Unlock()
	if ok && e.size == fi.Size() && e.modTime == fi.ModTime().UnixNano() {
		return e.mappings
	}

	data, err := safefs.ReadRepoFile(root, filepath.FromSlash(composer), maxProjectFileBytes)
	var mappings []psr4Mapping
	if err == nil {
		mappings = parsePSR4(data)
	}
	psr4Cache.mu.Lock()
	psr4Cache.entries[abs] = psr4Entry{size: fi.Size(), modTime: fi.ModTime().UnixNano(), mappings: mappings}
	psr4Cache.mu.Unlock()
	return mappings
}

// parsePSR4 extracts the psr-4 maps. A value is a directory or a list of
// them; a malformed file contributes nothing, as for every manifest.
func parsePSR4(data []byte) []psr4Mapping {
	var doc struct {
		Autoload struct {
			PSR4 map[string]json.RawMessage `json:"psr-4"`
		} `json:"autoload"`
		AutoloadDev struct {
			PSR4 map[string]json.RawMessage `json:"psr-4"`
		} `json:"autoload-dev"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []psr4Mapping
	for _, table := range []map[string]json.RawMessage{doc.Autoload.PSR4, doc.AutoloadDev.PSR4} {
		for prefix, raw := range table {
			var dirs []string
			var one string
			if json.Unmarshal(raw, &one) == nil {
				dirs = []string{one}
			} else if json.Unmarshal(raw, &dirs) != nil {
				continue
			}
			for _, d := range dirs {
				d = strings.Trim(strings.TrimPrefix(filepath.ToSlash(d), "./"), "/")
				if strings.Contains(d, "..") {
					// Outside the project; never a file of this repository.
					continue
				}
				out = append(out, psr4Mapping{prefix: strings.Trim(prefix, "\\"), dir: d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dir != out[j].dir {
			return out[i].dir < out[j].dir
		}
		return out[i].prefix < out[j].prefix
	})
	return out
}

// psr4Members expands "use App\Models\{User, Post as P}" into the classes it
// imports.
func psr4Members(prefix, members string) []string {
	var out []string
	for _, member := range strings.Split(members, ",") {
		member = strings.TrimSpace(member)
		if name, _, ok := strings.Cut(member, " as "); ok {
			member = strings.TrimSpace(name)
		}
		member = strings.Trim(member, "\\")
		if member == "" || strings.ContainsAny(member, " {}") {
			continue
		}
		out = append(out, strings.TrimRight(prefix, "\\")+"\\"+member)
	}
	return out
}
