package lang

import (
	"path"
	"strings"
)

// resolveInclude turns a C or C++ include into the repository path of the
// header it names, or returns it as written when no file in the repository
// matches: <stdio.h>, <QWidget> and <gtest/gtest.h> are headers of the
// system or of a dependency, and their spelling is what detectors gate on.
//
// A compiler searches the directories its build gives it (-I), which only
// the build knows. Without one, the conventional layouts cover most
// projects: a quoted include is first looked for beside the including file,
// and then each directory from the including file's up to the root is tried
// as an include directory, along with its include/ and src/. So
// src/net/http.c including "util.h" finds src/net/util.h, and
// <mylib/api.h> from libs/core/src/api.c finds libs/core/include/mylib/api.h.
// The nearest match wins, as it does for a compiler searching in order.
func resolveInclude(root, file, name string, quoted bool) string {
	if root == "" {
		return name
	}
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." || path.IsAbs(clean) || strings.Contains(clean, ":") {
		return name
	}
	// <vector> and <QWidget>: a standard or framework header, never a
	// project file in practice, and the most common include of all.
	if !quoted && !strings.ContainsAny(clean, "./") {
		return name
	}
	dir := path.Dir(file)
	if quoted {
		if p := path.Join(dir, clean); probeFile(root, p) {
			return p
		}
	}
	for {
		for _, sub := range [...]string{"", "include", "src"} {
			if p := path.Join(dir, sub, clean); probeFile(root, p) {
				return p
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return name
		}
		dir = path.Dir(dir)
	}
}

// probeFile is fileExists cached for the scan in progress. Outside a scan,
// or for another root, it is fileExists.
func probeFile(root, rel string) bool {
	activeSpec.mu.Lock()
	if activeSpec.root == root {
		if ok, hit := activeSpec.exists[rel]; hit {
			activeSpec.mu.Unlock()
			return ok
		}
	}
	activeSpec.mu.Unlock()

	ok := fileExists(root, rel)

	activeSpec.mu.Lock()
	if activeSpec.root == root {
		if activeSpec.exists == nil {
			activeSpec.exists = map[string]bool{}
		}
		activeSpec.exists[rel] = ok
	}
	activeSpec.mu.Unlock()
	return ok
}
