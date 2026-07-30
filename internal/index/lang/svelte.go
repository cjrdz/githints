package lang

import (
	"regexp"
	"strings"
)

// SvelteParser indexes the <script> blocks of .svelte components (both module
// and instance scripts) by delegating their contents to the TypeScript parser
// with line offsets preserved. Markup, styles, and moustache expressions are
// not indexed.
type SvelteParser struct{}

func (SvelteParser) Language() string { return "svelte" }

func (SvelteParser) Extensions() []string { return []string{".svelte"} }

func (SvelteParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	symbols, imports := parseTSScriptBlocks(path, src)
	return symbols, imports, nil
}

var (
	reScriptOpen  = regexp.MustCompile(`(?i)<\s*script\b[^>]*>`)
	reScriptClose = regexp.MustCompile(`(?i)</\s*script\s*>`)
)

// scriptBlock is one extracted region of TS/JS code: its raw lines and the
// offset that maps content line numbers back to host-file line numbers.
type scriptBlock struct {
	content []string
	// baseLine converts a 0-based content line index to a 0-based host-file
	// line index: hostLine0 = baseLine + contentLine0. Reported 1-based
	// symbol lines are then baseLine + contentLine0 + 1.
	baseLine int
}

// parseTSScriptBlocks extracts every <script> block and parses it as
// TypeScript, offsetting reported lines back into host-file coordinates.
func parseTSScriptBlocks(file string, src []byte) ([]Symbol, []Import) {
	var symbols []Symbol
	var imports []Import
	for _, block := range extractScriptBlocks(src) {
		code := strings.Join(block.content, "\n")
		symbols = append(symbols, parseTypeScript(file, []byte(code), block.baseLine)...)
		imports = append(imports, extractTSImports(file, []byte(code))...)
	}
	return symbols, imports
}

// extractScriptBlocks finds <script>...</script> regions line-wise. Both the
// open and close tag may share a line with content; content on the tag line is
// attributed to that host line.
func extractScriptBlocks(src []byte) []scriptBlock {
	lines := strings.Split(string(src), "\n")
	var blocks []scriptBlock
	for i := 0; i < len(lines); i++ {
		open := reScriptOpen.FindStringIndex(lines[i])
		if open == nil {
			continue
		}
		rest := lines[i][open[1]:]
		// Same-line close: <script>let x = 1</script>
		if close := reScriptClose.FindStringIndex(rest); close != nil {
			blocks = append(blocks, scriptBlock{
				content:  []string{rest[:close[0]]},
				baseLine: i, // content sits on host line i (0-based)
			})
			continue
		}
		var content []string
		baseLine := i + 1 // first content line is host line i+1 (0-based)
		j := i + 1
		for ; j < len(lines); j++ {
			if close := reScriptClose.FindStringIndex(lines[j]); close != nil {
				if strings.TrimSpace(lines[j][:close[0]]) != "" {
					content = append(content, lines[j][:close[0]])
				}
				break
			}
			content = append(content, lines[j])
		}
		blocks = append(blocks, scriptBlock{content: content, baseLine: baseLine})
		i = j
	}
	return blocks
}
