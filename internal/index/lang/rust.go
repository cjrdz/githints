package lang

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/safefs"
)

// Rust names modules by the crate's module tree, not by file path alone:
// src/models/user.rs (or src/models/user/mod.rs) is crate::models::user, and
// src/lib.rs or src/main.rs is the crate itself. Keys use the crate's name
// from Cargo.toml ("my-app" is my_app) in place of "crate", so a binary that
// says `use my_app::models` and a module that says `use crate::models` reach
// the same file.
//
// Only modules of the crate the importing file belongs to resolve. Another
// workspace crate's modules, and external crates, keep the path as written:
// it shows in the file's note and links nowhere.

type rustCrate struct {
	dir  string // repo-relative directory holding Cargo.toml
	name string // the package name, with - read as _
}

// rustCrateCache holds parsed Cargo.toml package names by absolute path,
// invalidated by size and modification time like the PSR-4 cache.
var rustCrateCache = struct {
	mu      sync.Mutex
	entries map[string]rustCrateEntry
}{entries: map[string]rustCrateEntry{}}

type rustCrateEntry struct {
	size, modTime int64
	name          string
}

// crateOf returns the crate a repo-relative file belongs to: the nearest
// Cargo.toml at or above it that declares a [package].
func crateOf(root, file string) (rustCrate, bool) {
	dir := path.Dir(file)
	for {
		manifest := path.Join(dir, "Cargo.toml")
		if fileExists(root, manifest) {
			if name := cargoPackageName(root, manifest); name != "" {
				return rustCrate{dir: dir, name: name}, true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return rustCrate{}, false
		}
		dir = path.Dir(dir)
	}
}

func cargoPackageName(root, manifest string) string {
	abs := filepath.Join(root, filepath.FromSlash(manifest))
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	rustCrateCache.mu.Lock()
	e, ok := rustCrateCache.entries[abs]
	rustCrateCache.mu.Unlock()
	if ok && e.size == fi.Size() && e.modTime == fi.ModTime().UnixNano() {
		return e.name
	}
	name := ""
	if data, err := safefs.ReadRepoFile(root, filepath.FromSlash(manifest), maxProjectFileBytes); err == nil {
		name = parseCargoPackageName(data)
	}
	rustCrateCache.mu.Lock()
	rustCrateCache.entries[abs] = rustCrateEntry{size: fi.Size(), modTime: fi.ModTime().UnixNano(), name: name}
	rustCrateCache.mu.Unlock()
	return name
}

// parseCargoPackageName reads `name = "..."` from the [package] table. A
// workspace-only Cargo.toml has none, so its directory is not a crate.
func parseCargoPackageName(data []byte) string {
	table := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[] ")
			continue
		}
		if table != "package" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "name" {
			continue
		}
		name := strings.Trim(strings.TrimSpace(value), `"'`)
		return strings.ReplaceAll(name, "-", "_")
	}
	return ""
}

// rustModulePath returns a file's module path inside its crate, without the
// crate name: nil for the crate root, ["models", "user"] for
// src/models/user.rs. Files outside src/, and binaries under src/bin, are
// crate roots of their own and have no path in this crate.
func rustModulePath(c rustCrate, file string) ([]string, bool) {
	rel := file
	if c.dir != "." {
		rel = strings.TrimPrefix(file, c.dir+"/")
	}
	if !strings.HasPrefix(rel, "src/") || !strings.HasSuffix(rel, ".rs") || strings.HasPrefix(rel, "src/bin/") {
		return nil, false
	}
	rel = strings.TrimSuffix(strings.TrimPrefix(rel, "src/"), ".rs")
	if rel == "lib" || rel == "main" {
		return []string{}, true
	}
	rel = strings.TrimSuffix(rel, "/mod")
	return strings.Split(rel, "/"), true
}

// rustModuleKey is the key a Rust file is imported by.
func rustModuleKey(root, file string) (string, error) {
	c, ok := crateOf(root, file)
	if !ok {
		return "", fmt.Errorf("%s is in no crate", file)
	}
	mod, ok := rustModulePath(c, file)
	if !ok {
		return "", fmt.Errorf("%s is not a module of crate %s", file, c.name)
	}
	return strings.Join(append([]string{c.name}, mod...), "::"), nil
}

// rustModuleExists reports whether a module path (below the crate root) has a
// file: src/a/b.rs or src/a/b/mod.rs, or for the root, src/lib.rs or main.rs.
func rustModuleExists(root string, c rustCrate, mod []string) bool {
	src := path.Join(c.dir, "src")
	if len(mod) == 0 {
		return fileExists(root, path.Join(src, "lib.rs")) || fileExists(root, path.Join(src, "main.rs"))
	}
	p := path.Join(append([]string{src}, mod...)...)
	return fileExists(root, p+".rs") || fileExists(root, path.Join(p, "mod.rs"))
}

// resolveRustUse turns a use path into the key of the deepest module of the
// importing file's crate it names: `use crate::models::user::User` is
// my_app::models::user when src/models/user.rs exists. A path into another
// crate is returned unchanged.
func resolveRustUse(root, file, use string) string {
	use = strings.TrimPrefix(use, "::")
	segs := strings.Split(strings.TrimSuffix(use, "::"), "::")
	if root == "" || len(segs) == 0 {
		return use
	}
	c, ok := crateOf(root, file)
	if !ok {
		return use
	}
	var base []string
	switch segs[0] {
	case "crate", c.name:
		base = []string{}
		segs = segs[1:]
	case "self", "super":
		mod, ok := rustModulePath(c, file)
		if !ok {
			return use
		}
		base = append([]string(nil), mod...)
		// A non-mod.rs file is a module whose children live in a directory
		// of its name, so self:: is the file itself either way; super:: is
		// one level up per occurrence.
		for len(segs) > 0 && (segs[0] == "self" || segs[0] == "super") {
			if segs[0] == "super" {
				if len(base) == 0 {
					return use // above the crate root
				}
				base = base[:len(base)-1]
			}
			segs = segs[1:]
		}
	default:
		return use
	}
	full := append(base, segs...)
	for n := len(full); n >= len(base); n-- {
		if rustModuleExists(root, c, full[:n]) {
			return strings.Join(append([]string{c.name}, full[:n]...), "::")
		}
	}
	return use
}

// rustUseMembers expands `use crate::{models::User, db}` into its paths.
// Nested groups are not expanded.
func rustUseMembers(prefix, members string) []string {
	var out []string
	for _, m := range strings.Split(members, ",") {
		m = strings.TrimSpace(m)
		if name, _, ok := strings.Cut(m, " as "); ok {
			m = strings.TrimSpace(name)
		}
		if m == "" || m == "*" || strings.ContainsAny(m, "{} ") {
			continue
		}
		if m == "self" {
			out = append(out, prefix)
			continue
		}
		out = append(out, strings.TrimSuffix(prefix, "::")+"::"+m)
	}
	return out
}
