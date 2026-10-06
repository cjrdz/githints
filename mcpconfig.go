package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cjrdz/githints/internal/gitutil"
)

// mcpClient describes where one MCP client keeps a project-level server list
// and what githints' entry looks like there.
type mcpClient struct {
	file  string   // relative to the repo root; "" for clients with only a global config
	path  []string // JSON object keys leading to the server map
	entry any      // githints' server entry
}

// Project configs are usually committed, so the entries name `githints` on
// PATH and leave the root implicit: these clients start servers from the
// project directory, and an absolute path would be wrong on every other
// machine.
var mcpClients = map[string]mcpClient{
	"claude": {
		file:  ".mcp.json",
		path:  []string{"mcpServers"},
		entry: map[string]any{"command": "githints", "args": []string{"serve"}},
	},
	"opencode": {
		file:  "opencode.json",
		path:  []string{"mcp"},
		entry: map[string]any{"type": "local", "command": []string{"githints", "serve"}, "enabled": true},
	},
	"gemini": {
		file:  filepath.Join(".gemini", "settings.json"),
		path:  []string{"mcpServers"},
		entry: map[string]any{"command": "githints", "args": []string{"serve"}},
	},
	"cursor": {
		file:  filepath.Join(".cursor", "mcp.json"),
		path:  []string{"mcpServers"},
		entry: map[string]any{"command": "githints", "args": []string{"serve"}},
	},
	"codex": {}, // global config only; see cmdMCPConfig
}

func mcpClientNames() string {
	names := make([]string, 0, len(mcpClients))
	for n := range mcpClients {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

// cmdMCPConfig prints, or with -write merges, the MCP server entry for one
// client. init used to print three snippets and leave the rest to the user,
// and nothing said whether one was ever added.
func cmdMCPConfig(args []string) error {
	fset := flag.NewFlagSet("mcp-config", flag.ExitOnError)
	write := fset.Bool("write", false, "merge the entry into the client's project config file")
	// Accept the client name before or after the flags.
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fset.Parse(args); err != nil {
		return err
	}
	if name == "" && fset.NArg() == 1 {
		name = fset.Arg(0)
	}
	client, ok := mcpClients[name]
	if !ok {
		return fmt.Errorf("usage: githints mcp-config <%s> [-write]", mcpClientNames())
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if client.file == "" {
		// Codex keeps one global config, so the root must be pinned or the
		// server would serve whatever directory Codex was started in.
		fmt.Printf("Codex CLI's MCP config is global; run:\n\n  codex mcp add githints -- githints serve -root=%s\n", root)
		if *write {
			return errors.New("-write is not supported for codex: its config is global, not per-repository")
		}
		return nil
	}

	snippet := map[string]any{}
	nest(snippet, client.path)["githints"] = client.entry
	if !*write {
		data, _ := json.MarshalIndent(snippet, "", "  ") // map of strings, bools and slices: cannot fail
		fmt.Printf("%s (in the repository root):\n\n%s\n\nRe-run with -write to merge it in.\n", client.file, data)
		return nil
	}

	path := filepath.Join(root, client.file)
	if err := mergeMCPEntry(path, client); err != nil {
		return err
	}
	fmt.Printf("registered githints in %s\n", client.file)
	return nil
}

// nest walks (creating) the object at keys inside m and returns it.
func nest(m map[string]any, keys []string) map[string]any {
	for _, k := range keys {
		child, ok := m[k].(map[string]any)
		if !ok {
			child = map[string]any{}
			m[k] = child
		}
		m = child
	}
	return m
}

// mergeMCPEntry adds githints' entry to a client config, keeping every other
// key. A file that is not plain JSON (opencode accepts comments) is left alone
// and the error says to add the entry by hand.
func mergeMCPEntry(path string, client mcpClient) error {
	doc := map[string]any{}
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file (symlink?); refusing to rewrite it", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &doc); err != nil {
				return fmt.Errorf("%s is not plain JSON (%v); add the entry by hand: run without -write to see it", path, err)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	servers := nest(doc, client.path)
	if _, exists := servers["githints"]; exists {
		fmt.Printf("%s already registers githints; left unchanged\n", path)
		return nil
	}
	servers["githints"] = client.entry

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
