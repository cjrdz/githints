package lang

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// packFS holds everything the index knows about each language, one directory
// per language:
//
//	packs/<language>/language.json        the spec, for languages shipped as data
//	packs/<language>/frameworks/**.json   framework detectors
//	packs/<language>/orm/**.json          ORM and schema detectors
//	packs/<language>/testdata/<case>/     sample projects; not embedded
//
// Directories are for people. What a detector applies to comes from its own
// "languages", and the role it reports from each rule's "facet", so a file can
// move between folders without changing what it does. Adding a framework is
// adding a file; TestEveryPackFileIsEmbedded fails if a new folder is missed
// in the pattern below.
//
//go:embed packs/*/language.json packs/*/frameworks packs/*/orm
var packFS embed.FS

// packSpecName is the file that holds a language spec inside its pack.
const packSpecName = "language.json"

// packFiles returns the embedded pack files of one kind, sorted, so loading
// order -- and therefore any duplicate-claim error -- is the same everywhere.
func packFiles(specs bool) ([]string, error) {
	var out []string
	err := fs.WalkDir(packFS, "packs", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".json" {
			return nil
		}
		if (path.Base(p) == packSpecName) == specs {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded packs: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// packLabel names a pack file in an error: "python/frameworks/fastapi.json".
func packLabel(p string) string {
	return strings.TrimPrefix(p, "packs/")
}
