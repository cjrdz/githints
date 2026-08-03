package lang

import (
	"strings"
)

// AstroParser indexes .astro components: the frontmatter fence (`---` block at
// the top of the file, which is TypeScript) and any <script> blocks. Markup
// and expressions in the template section are not indexed.
type AstroParser struct{}

func (AstroParser) Language() string { return "astro" }

func (AstroParser) Extensions() []string { return []string{".astro"} }

func (AstroParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	symbols, imports := parseAstroFrontmatter(path, src)
	scriptSymbols, scriptImports := parseTSScriptBlocks(path, src)
	symbols = append(symbols, scriptSymbols...)
	imports = append(imports, scriptImports...)
	return symbols, imports, nil
}

// parseAstroFrontmatter extracts the leading `---` fence and parses it as
// TypeScript. The fence must open on the very first line; content between the
// opening and closing `---` lines is parsed with host-file line offsets.
func parseAstroFrontmatter(file string, src []byte) ([]Symbol, []Import) {
	lines := strings.Split(string(src), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end <= 1 {
		return nil, nil
	}
	// Content lines are host lines 1..end-1 (0-based), so content line 0 maps
	// to host line 1 (0-based): baseLine = 1.
	code := strings.Join(lines[1:end], "\n")
	symbols := parseTypeScript(file, []byte(code), 1)
	imports := extractTSImports(file, []byte(code))
	return symbols, imports
}
