package mcpcfg

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// ConfiguredCommand is the program a project MCP config runs for githints.
type ConfiguredCommand struct {
	Path    string // the config file
	Command string // "githints", or the absolute path setup wrote
}

// ProjectCommands reads the githints entry of every project-level config
// setup knows and returns the command each one runs, so doctor can tell when
// it names a binary that is gone or is not this one. setup writes an absolute
// path when githints is not on PATH; after the binary moves (a reinstall, a
// package upgrade, a deleted build) the client fails to start the server
// with nothing pointing at why.
//
// It only reads: a file that is missing, not plain JSON (JSONC with comments),
// not a regular file, or without a githints entry is skipped. Global configs
// are not read; they belong to the machine.
func ProjectCommands(root string) []ConfiguredCommand {
	var out []ConfiguredCommand
	seen := map[string]bool{}
	for _, c := range clientList {
		if c.scope != ScopeProject {
			continue
		}
		paths, err := c.paths(root)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			data, err := readConfig(p)
			if err != nil || len(bytes.TrimSpace(data)) == 0 {
				continue
			}
			var cmd string
			if c.toml {
				cmd = tomlCommand(data)
			} else {
				cmd = jsonCommand(data, c.keyPath)
			}
			if cmd != "" {
				out = append(out, ConfiguredCommand{Path: p, Command: cmd})
			}
		}
	}
	return out
}

// jsonCommand is the githints entry's command: a string, or the first element
// of an array (opencode puts the arguments in the same array).
func jsonCommand(data []byte, keyPath []string) string {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	var node any = doc
	for _, k := range append(append([]string{}, keyPath...), "githints") {
		m, ok := node.(map[string]any)
		if !ok {
			return ""
		}
		node = m[k]
	}
	entry, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	switch v := entry["command"].(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// tomlCommand reads `command = "..."` from the [mcp_servers.githints] table
// that registerTOML appends. It is not a TOML parser: a table written by hand
// in another shape is skipped, not misread.
func tomlCommand(data []byte) string {
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			m := tomlTableRe.FindStringSubmatch(line)
			in = m != nil && m[1] == "githints"
			continue
		}
		if !in {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "command" {
			continue
		}
		// tomlString writes a basic string with \" \\ and \uXXXX escapes,
		// all of which strconv.Unquote reads the same way.
		s, err := strconv.Unquote(strings.TrimSpace(value))
		if err != nil {
			return ""
		}
		return s
	}
	return ""
}
