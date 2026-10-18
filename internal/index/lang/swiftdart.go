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

// spmModuleKey names a Swift file by its Swift Package Manager target:
// Sources/<Target>/... or Tests/<Target>/... beside the nearest
// Package.swift. Xcode projects without a package have no module key, and
// lose nothing by it: a module's files never import one another.
func spmModuleKey(root, file string) (string, error) {
	dir := path.Dir(file)
	for {
		if fileExists(root, path.Join(dir, "Package.swift")) {
			rel := file
			if dir != "." {
				rel = strings.TrimPrefix(file, dir+"/")
			}
			parts := strings.Split(rel, "/")
			if len(parts) >= 3 && (parts[0] == "Sources" || parts[0] == "Tests") {
				return parts[1], nil
			}
			return "", fmt.Errorf("%s is not in a target of %s/Package.swift", file, dir)
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", fmt.Errorf("%s is in no Swift package", file)
		}
		dir = path.Dir(dir)
	}
}

// dartPackageCache holds pubspec.yaml package names by absolute path,
// invalidated by size and modification time.
var dartPackageCache = struct {
	mu      sync.Mutex
	entries map[string]rustCrateEntry
}{entries: map[string]rustCrateEntry{}}

// dartPackageOf returns the directory and name of the Dart package a file
// belongs to: the nearest pubspec.yaml at or above it.
func dartPackageOf(root, file string) (dir, name string, ok bool) {
	dir = path.Dir(file)
	for {
		spec := path.Join(dir, "pubspec.yaml")
		if fileExists(root, spec) {
			if name := pubspecName(root, spec); name != "" {
				return dir, name, true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", "", false
		}
		dir = path.Dir(dir)
	}
}

func pubspecName(root, spec string) string {
	abs := filepath.Join(root, filepath.FromSlash(spec))
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	dartPackageCache.mu.Lock()
	e, ok := dartPackageCache.entries[abs]
	dartPackageCache.mu.Unlock()
	if ok && e.size == fi.Size() && e.modTime == fi.ModTime().UnixNano() {
		return e.name
	}
	name := ""
	if data, err := safefs.ReadRepoFile(root, filepath.FromSlash(spec), maxProjectFileBytes); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "name:"); ok {
				name = strings.Trim(strings.TrimSpace(v), `"'`)
				break
			}
		}
	}
	dartPackageCache.mu.Lock()
	dartPackageCache.entries[abs] = rustCrateEntry{size: fi.Size(), modTime: fi.ModTime().UnixNano(), name: name}
	dartPackageCache.mu.Unlock()
	return name
}

// resolveDartImport turns a Dart import into the repository path it names:
// package:<own package>/x.dart is <package>/lib/x.dart, and a relative URI is
// joined to the importing file's directory. dart: libraries and other
// packages keep their URI, which still gates detectors
// (package:flutter/material.dart).
func resolveDartImport(root, file, uri string) string {
	if rest, ok := strings.CutPrefix(uri, "package:"); ok {
		pkg, sub, found := strings.Cut(rest, "/")
		if !found || root == "" {
			return uri
		}
		dir, name, ok := dartPackageOf(root, file)
		if !ok || name != pkg {
			return uri
		}
		return path.Join(dir, "lib", sub)
	}
	if strings.Contains(uri, ":") {
		return uri // dart:core, a URL
	}
	joined := path.Join(path.Dir(file), uri)
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return uri
	}
	return joined
}
