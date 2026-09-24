package lang

import (
	"path/filepath"
)

// VueParser indexes the <script> blocks of .vue single-file components,
// including <script setup>, by delegating their contents to the TypeScript
// parser with line offsets preserved. Template markup and styles are not
// indexed.
//
// This is a hand-written parser rather than a language spec because a spec is
// line-oriented and an SFC needs the opposite: keep one region and discard the
// rest. Svelte and Astro are the same shape, and this shares their machinery.
type VueParser struct{}

func (VueParser) Language() string { return "vue" }

func (VueParser) Extensions() []string { return []string{".vue"} }

func (VueParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	symbols, imports := parseTSScriptBlocks(path, src)
	return symbols, imports, nil
}

// BeginScan installs the tsconfig path aliases this parser resolves against.
func (VueParser) BeginScan(root string) func() { return beginTSScan(root) }

// ImportPath maps the file to the key TypeScript importers resolve to. Unlike
// the other TS-family extensions a .vue import keeps its extension in source
// (`import X from "./Thing.vue"`), which tsFileKey handles because .vue is in
// tsCodeExtensions.
func (VueParser) ImportPath(_, file string) (string, error) {
	return tsFileKey(filepath.ToSlash(file)), nil
}

// BlankLines returns the comment- and string-free view of the file.
//
// The whole file is blanked with the TypeScript rules, template included.
// Markup is not TypeScript, but treating it as such only affects framework
// detection, and the alternative -- detectors seeing raw template text -- is
// worse.
func (VueParser) BlankLines(src []byte) []string { return tsBlanker.Blank(src) }

// BlankLinesKeepingStrings keeps string contents, which detectors need.
func (VueParser) BlankLinesKeepingStrings(src []byte) []string {
	return tsBlanker.BlankKeepingStrings(src)
}
