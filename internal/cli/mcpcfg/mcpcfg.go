// Package mcpcfg holds pure MCP client configuration manipulation: the table of
// known clients, registration, unregistration, and rendering snippets.
package mcpcfg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/cjrdz/githints/internal/gitutil"
)

// Scope says where a client's config lives.
type Scope int

const (
	// ScopeProject: a file inside the repository. The default, and the only
	// scope setup writes without being asked by name.
	ScopeProject Scope = iota
	// ScopeGlobal: one file per user, shared by every repository. Written
	// only when the client is named explicitly, with a backup.
	ScopeGlobal
	// ScopeManual: no file githints can safely write (YAML, or a settings UI).
	// The snippet and steps are printed instead.
	ScopeManual
)

// EntryCtx is what an entry is built from.
type EntryCtx struct {
	Command string // githints, or its absolute path when it is not on PATH
	Root    string // absolute repository root
}

// Client describes one MCP client.
type Client struct {
	name    string
	title   string
	aliases []string
	scope   Scope
	// paths returns candidate config files. For project clients it is one
	// path under root. For global clients, every path that exists is written;
	// if none does, the client is treated as not installed.
	paths func(root string) ([]string, error)
	// toml: Codex. Everything else is JSON.
	toml bool
	// keyPath leads from the document root to the object of servers.
	keyPath []string
	entry   func(c EntryCtx) any
	// detect reports whether the client looks in use for this repository or
	// on this machine; setup registers detected project clients by default.
	detect func(root string) bool
	note   string
}

// Name returns the client's short name.
func (c Client) Name() string { return c.name }

// Title returns the client's human-readable title.
func (c Client) Title() string { return c.title }

// Scope returns where the client's config lives.
func (c Client) Scope() Scope { return c.scope }

// Note returns the client's note, used for manual clients and post-register notes.
func (c Client) Note() string { return c.note }

// Detect reports whether the client looks in use for the given repository.
func (c Client) Detect(root string) bool { return c.detect != nil && c.detect(root) }

// serveArgs is ["serve"], plus -root when the client's working directory is
// not documented to be the project. root may be a client-side variable such
// as ${workspaceFolder}.
func serveArgs(root string) []string {
	if root == "" {
		return []string{"serve"}
	}
	return []string{"serve", "-root=" + root}
}

func projectFile(rel ...string) func(string) ([]string, error) {
	return func(root string) ([]string, error) {
		return []string{filepath.Join(append([]string{root}, rel...)...)}, nil
	}
}

func onPath(names ...string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// homeDir and configDir are variables so tests can point them at a temp dir.
var (
	homeDir   = os.UserHomeDir
	appDataFn = func() string { return os.Getenv("APPDATA") }
)

func homePath(rel ...string) (string, error) {
	h, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{h}, rel...)...), nil
}

var clientList = []Client{
	{
		name: "claude", title: "Claude Code", aliases: []string{"claude-code", "warp"},
		paths:   projectFile(".mcp.json"),
		keyPath: []string{"mcpServers"},
		// Claude Code passes CLAUDE_PROJECT_DIR, which serve honors.
		entry: func(c EntryCtx) any {
			return map[string]any{"type": "stdio", "command": c.Command, "args": serveArgs("")}
		},
		detect: func(root string) bool { return onPath("claude") || exists(filepath.Join(root, ".mcp.json")) },
		note:   "Warp reads this file too, and so does Pi's pi-mcp-adapter; the pi entry below detects Pi separately.",
	},
	{
		name: "pi", title: "Pi coding agent (pi-mcp-adapter)", aliases: []string{"pi-mcp-adapter"},
		paths:   projectFile(".mcp.json"),
		keyPath: []string{"mcpServers"},
		// pi-mcp-adapter reads the shared .mcp.json format, same as Claude
		// Code: stdio entries with command and args. The adapter starts the
		// server from the project directory, so no root is pinned.
		entry: func(c EntryCtx) any {
			return map[string]any{"type": "stdio", "command": c.Command, "args": serveArgs("")}
		},
		detect: func(root string) bool { return onPath("pi") || exists(filepath.Join(root, ".pi")) },
		note:   "Pi reads this file through the pi-mcp-adapter extension (pi.dev/packages/pi-mcp-adapter), which replaces Pi's built-in MCP.",
	},
	{
		name: "opencode", title: "opencode",
		paths:   projectFile("opencode.json"),
		keyPath: []string{"mcp"},
		entry: func(c EntryCtx) any {
			return map[string]any{"type": "local", "command": append([]string{c.Command}, serveArgs("")...), "enabled": true}
		},
		detect: func(root string) bool {
			return onPath("opencode") || exists(filepath.Join(root, "opencode.json")) || exists(filepath.Join(root, "opencode.jsonc"))
		},
	},
	{
		name: "codex", title: "Codex CLI",
		paths: projectFile(".codex", "config.toml"),
		toml:  true,
		// No documented default working directory, so the root is pinned.
		entry:  func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
		detect: func(root string) bool { return onPath("codex") || exists(filepath.Join(root, ".codex")) },
		note:   "Codex reads .codex/config.toml only for projects you have marked trusted. The file pins this checkout's absolute path.",
	},
	{
		name: "vscode", title: "VS Code (Copilot agent mode)", aliases: []string{"code", "copilot"},
		paths:   projectFile(".vscode", "mcp.json"),
		keyPath: []string{"servers"},
		entry: func(c EntryCtx) any {
			return map[string]any{"type": "stdio", "command": c.Command, "args": serveArgs("")}
		},
		detect: func(root string) bool { return onPath("code") || exists(filepath.Join(root, ".vscode")) },
	},
	{
		name: "cursor", title: "Cursor",
		paths:   projectFile(".cursor", "mcp.json"),
		keyPath: []string{"mcpServers"},
		// Working directory undocumented; Cursor expands ${workspaceFolder},
		// which keeps the file portable.
		entry: func(c EntryCtx) any {
			return map[string]any{"type": "stdio", "command": c.Command, "args": serveArgs("${workspaceFolder}")}
		},
		detect: func(root string) bool { return onPath("cursor") || exists(filepath.Join(root, ".cursor")) },
	},
	{
		name: "zed", title: "Zed",
		paths:   projectFile(".zed", "settings.json"),
		keyPath: []string{"context_servers"},
		entry: func(c EntryCtx) any {
			return map[string]any{"command": c.Command, "args": serveArgs(""), "env": map[string]any{}}
		},
		detect: func(root string) bool { return onPath("zed", "zeditor") || exists(filepath.Join(root, ".zed")) },
	},
	{
		name: "kiro", title: "Kiro",
		paths:   projectFile(".kiro", "settings", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry: func(c EntryCtx) any {
			return map[string]any{"command": c.Command, "args": serveArgs(c.Root), "disabled": false}
		},
		detect: func(root string) bool { return onPath("kiro", "kiro-cli") || exists(filepath.Join(root, ".kiro")) },
		note:   "The file pins this checkout's absolute path (Kiro does not document the server's working directory).",
	},
	{
		name: "gemini", title: "Gemini CLI",
		paths:   projectFile(".gemini", "settings.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs("")} },
		detect:  func(root string) bool { return onPath("gemini") || exists(filepath.Join(root, ".gemini")) },
	},
	{
		name: "junie", title: "JetBrains Junie",
		paths:   projectFile(".junie", "mcp", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
		detect:  func(root string) bool { return onPath("junie") || exists(filepath.Join(root, ".junie")) },
		note:    "The file pins this checkout's absolute path.",
	},
	{
		name: "continue", title: "Continue",
		paths:   projectFile(".continue", "mcpServers", "githints.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
		detect:  func(root string) bool { return exists(filepath.Join(root, ".continue")) },
		note:    "The file pins this checkout's absolute path.",
	},
	{
		name: "amazonq", title: "Amazon Q Developer CLI (legacy; Kiro CLI replaces it)", aliases: []string{"q"},
		paths:   projectFile(".amazonq", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
		detect:  func(root string) bool { return exists(filepath.Join(root, ".amazonq")) },
	},
	{
		name: "claude-desktop", title: "Claude Desktop", scope: ScopeGlobal,
		paths: func(string) ([]string, error) {
			switch runtime.GOOS {
			case "darwin":
				p, err := homePath("Library", "Application Support", "Claude", "claude_desktop_config.json")
				return []string{p}, err
			case "windows":
				if a := appDataFn(); a != "" {
					return []string{filepath.Join(a, "Claude", "claude_desktop_config.json")}, nil
				}
				return nil, errors.New("APPDATA is not set")
			}
			return nil, errors.New("claude Desktop is available only for macOS and Windows")
		},
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
	},
	{
		name: "windsurf", title: "Windsurf / Devin Desktop", aliases: []string{"devin"}, scope: ScopeGlobal,
		paths: func(string) ([]string, error) {
			var out []string
			if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
				out = append(out, filepath.Join(x, "devin", "mcp_config.json"))
			} else if p, err := homePath(".config", "devin", "mcp_config.json"); err == nil {
				out = append(out, p)
			}
			if a := appDataFn(); a != "" {
				out = append(out, filepath.Join(a, "devin", "mcp_config.json"))
			}
			if p, err := homePath(".codeium", "windsurf", "mcp_config.json"); err == nil {
				out = append(out, p) // pre-rebrand location
			}
			return out, nil
		},
		keyPath: []string{"mcpServers"},
		entry:   func(c EntryCtx) any { return map[string]any{"command": c.Command, "args": serveArgs(c.Root)} },
		note:    "Applies to the Cascade agent; Devin Local uses a different config.",
	},
	{
		name: "cline", title: "Cline CLI", scope: ScopeGlobal,
		paths: func(string) ([]string, error) {
			if p := os.Getenv("CLINE_MCP_SETTINGS_PATH"); p != "" {
				return []string{p}, nil
			}
			p, err := homePath(".cline", "data", "settings", "cline_mcp_settings.json")
			return []string{p}, err
		},
		keyPath: []string{"mcpServers"},
		entry: func(c EntryCtx) any {
			return map[string]any{"command": c.Command, "args": serveArgs(c.Root), "disabled": false, "autoApprove": []string{}}
		},
		note: "The Cline IDE extension keeps its settings elsewhere: open its MCP Servers view and paste the same entry.",
	},
	{
		name: "goose", title: "Goose", scope: ScopeManual,
		note: "Goose keeps extensions in YAML (~/.config/goose/config.yaml). Add:\n\n" +
			"extensions:\n  githints:\n    type: stdio\n    name: githints\n    enabled: true\n    cmd: %[1]s\n    args: [\"serve\", \"-root=%[2]s\"]\n    envs: {}\n    timeout: 300\n",
	},
	{
		name: "jetbrains", title: "JetBrains AI Assistant", scope: ScopeManual,
		note: "AI Assistant has no config file to write. In Settings > Tools > AI Assistant > Model Context Protocol,\n" +
			"add a server with command %[1]s and arguments: serve -root=%[2]s\n(or use `githints setup -clients=junie` for Junie).",
	},
}

// LookupClient returns the client with the given name or alias.
func LookupClient(name string) (Client, bool) {
	name = strings.ToLower(name)
	for _, c := range clientList {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return Client{}, false
}

// Clients returns a copy of the built-in client table.
func Clients() []Client {
	out := make([]Client, len(clientList))
	copy(out, clientList)
	return out
}

// ClientNames returns a pipe-separated list of all client names for usage strings.
func ClientNames() string {
	names := make([]string, 0, len(clientList))
	for _, c := range clientList {
		names = append(names, c.name)
	}
	return strings.Join(names, "|")
}

// GithintsCommand is what configs should run. Plain "githints" when that is
// on PATH, so a committed project file works on every machine; otherwise
// this binary's absolute path, which is only right on this one. GUI apps
// (Claude Desktop) often get a minimal PATH, so global configs always use
// the absolute path.
func GithintsCommand(global bool) (cmd string, portable bool) {
	exe, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
	}
	if global && err == nil {
		return exe, false
	}
	if _, lerr := exec.LookPath("githints"); lerr == nil || err != nil {
		return "githints", true
	}
	return exe, false
}

// globalServerName gives each repository its own entry in a global config,
// where a fixed "githints" would make every repo overwrite the last.
func globalServerName(root string) string {
	sum := sha256.Sum256([]byte(root))
	base := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return unicode.ToLower(r)
		}
		return '-'
	}, filepath.Base(root))
	return "githints-" + base + "-" + hex.EncodeToString(sum[:])[:6]
}

// RegisterResult is what happened for one file.
type RegisterResult struct {
	Path    string
	Changed bool   // the file was (or, in a dry run, would be) written
	Detail  string // e.g. "already registered"
}

// Register adds githints to one client's config(s). An entry that is already
// there is left alone unless update is set, and then only githints' own entry
// is replaced. A file it cannot parse exactly is never rewritten.
func Register(c Client, root string, dryRun, update bool) ([]RegisterResult, error) {
	cmd, _ := GithintsCommand(c.scope == ScopeGlobal)
	ctx := EntryCtx{Command: cmd, Root: root}
	paths, err := c.paths(root)
	if err != nil {
		return nil, err
	}
	name := "githints"
	if c.scope == ScopeGlobal {
		name = globalServerName(root)
		var present []string
		for _, p := range paths {
			if exists(p) {
				present = append(present, p)
			}
		}
		if len(present) == 0 {
			return nil, fmt.Errorf("%s does not look installed (no config at %s)", c.title, strings.Join(paths, " or "))
		}
		paths = present
	}

	var out []RegisterResult
	for _, p := range paths {
		var r RegisterResult
		var err error
		if c.toml {
			r, err = registerTOML(p, name, ctx, c, dryRun)
		} else {
			r, err = registerJSON(p, name, ctx, c, dryRun, update)
		}
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func readConfig(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (symlink?); refusing to rewrite it", path)
	}
	return os.ReadFile(path)
}

// writeConfig replaces path atomically. A global config gets a one-time
// backup next to it first, since it belongs to every project on the machine.
func writeConfig(path string, data []byte, global bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if global {
			bak := path + ".githints-backup"
			if !exists(bak) {
				old, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.WriteFile(bak, old, mode); err != nil {
					return fmt.Errorf("back up %s: %w", path, err)
				}
			}
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()           // the write error is the one worth reporting
		_ = os.Remove(tmp.Name()) // best effort; nothing else references it
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name()) // best effort
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		_ = os.Remove(tmp.Name()) // best effort
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func registerJSON(path, name string, ctx EntryCtx, c Client, dryRun, update bool) (RegisterResult, error) {
	res := RegisterResult{Path: path}
	data, err := readConfig(path)
	if err != nil {
		return res, err
	}
	doc := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber() // keep numbers exactly as written
		if err := dec.Decode(&doc); err != nil {
			return res, fmt.Errorf("%s is not plain JSON (%v; comments are not supported). Add this entry under %q by hand:\n%s",
				path, err, strings.Join(c.keyPath, "."), SnippetJSON(c, name, ctx))
		}
	}
	servers := nest(doc, c.keyPath)
	want := c.entry(ctx)
	if existing, ok := servers[name]; ok && update {
		a, _ := json.Marshal(existing) // decoded JSON re-encodes
		b, _ := json.Marshal(want)     // maps of strings, bools and slices
		if bytes.Equal(a, b) {
			res.Detail = "up to date"
			return res, nil
		}
	} else if existing, ok := servers[name]; ok {
		res.Detail = "already registered"
		if c.scope == ScopeGlobal {
			if b, _ := json.Marshal(existing); !bytes.Contains(b, []byte(jsonEscaped(ctx.Root))) {
				res.Detail = "an entry named " + name + " exists for another checkout; left unchanged"
			}
		}
		return res, nil
	}
	servers[name] = want
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, err
	}
	res.Changed = true
	if dryRun {
		return res, nil
	}
	return res, writeConfig(path, append(out, '\n'), c.scope == ScopeGlobal)
}

func jsonEscaped(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b[1 : len(b)-1])
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var tomlTableRe = regexp.MustCompile(`(?m)^\s*\[\s*mcp_servers\s*\.\s*"?([A-Za-z0-9_-]+)"?\s*\]`)

// registerTOML appends a [mcp_servers.<name>] table. There is no TOML parser
// in the dependency graph and adding one for an append is not worth it, so
// this only ever appends text and leaves everything already in the file
// byte-for-byte alone.
func registerTOML(path, name string, ctx EntryCtx, c Client, dryRun bool) (RegisterResult, error) {
	res := RegisterResult{Path: path}
	data, err := readConfig(path)
	if err != nil {
		return res, err
	}
	for _, m := range tomlTableRe.FindAllSubmatch(data, -1) {
		if string(m[1]) == name {
			res.Detail = "already registered (TOML is never rewritten; edit [mcp_servers." + name + "] by hand to change it)"
			return res, nil
		}
	}
	if bytes.Contains(data, []byte("mcp_servers."+name)) || bytes.Contains(data, []byte(`mcp_servers."`+name+`"`)) {
		res.Detail = "already registered"
		return res, nil
	}
	var b bytes.Buffer
	b.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteByte('\n')
	}
	if len(data) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(SnippetTOML(name, ctx))
	res.Changed = true
	if dryRun {
		return res, nil
	}
	return res, writeConfig(path, b.Bytes(), c.scope == ScopeGlobal)
}

// SnippetTOML renders Codex's table. Only command and args: Codex rejects
// unknown fields. The root is pinned because Codex does not document the
// server's working directory.
func SnippetTOML(name string, ctx EntryCtx) string {
	args := serveArgs(ctx.Root)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = tomlString(a)
	}
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n", name, tomlString(ctx.Command), strings.Join(quoted, ", "))
}

// SnippetJSON renders the JSON snippet for the given client and entry context.
func SnippetJSON(c Client, name string, ctx EntryCtx) string {
	doc := map[string]any{}
	nest(doc, c.keyPath)[name] = c.entry(ctx)
	b, _ := json.MarshalIndent(doc, "", "  ") // maps of strings, bools and slices cannot fail
	return string(b)
}

// nest walks (creating) the object at keys inside m and returns it. A key
// holding something other than an object is replaced, which only happens in
// a file that was already broken for this client.
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

// ManualText returns the formatted manual instructions for a manual-scope client.
func ManualText(c Client, root string) string {
	cmd, _ := GithintsCommand(true)
	return fmt.Sprintf(c.note, cmd, root)
}

// MCPConfig prints, or with -write adds, githints' entry for one client.
func MCPConfig(args []string) error {
	fset := flag.NewFlagSet("mcp-config", flag.ExitOnError)
	write := fset.Bool("write", false, "add the entry to the client's config file")
	update := fset.Bool("update", false, "with -write: replace githints' existing entry (e.g. after moving githints onto PATH)")
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
	client, ok := LookupClient(name)
	if !ok {
		return fmt.Errorf("usage: githints setup -print -clients=<%s>", ClientNames())
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if client.scope == ScopeManual {
		fmt.Println(ManualText(client, root))
		if *write {
			return fmt.Errorf("-write is not supported for %s; follow the steps above", client.title)
		}
		return nil
	}

	if !*write {
		cmd, _ := GithintsCommand(client.scope == ScopeGlobal)
		ctx := EntryCtx{Command: cmd, Root: root}
		name := "githints"
		if client.scope == ScopeGlobal {
			name = globalServerName(root)
		}
		paths, err := client.paths(root)
		if err != nil {
			return err
		}
		rel := make([]string, len(paths))
		for i, p := range paths {
			rel[i] = DisplayPath(root, p)
		}
		fmt.Printf("%s: %s\n\n", client.title, strings.Join(rel, " or "))
		if client.toml {
			fmt.Print(SnippetTOML(name, ctx))
		} else {
			fmt.Println(SnippetJSON(client, name, ctx))
		}
		if client.note != "" {
			fmt.Printf("\n%s\n", client.note)
		}
		fmt.Println("\nRe-run with -write to add it.")
		return nil
	}

	results, err := Register(client, root, false, *update)
	for _, r := range results {
		PrintResult(root, client, r, false)
	}
	return err
}

// DisplayPath returns a path relative to root when possible.
func DisplayPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return p
}

// PrintResult prints one registration result.
func PrintResult(root string, c Client, r RegisterResult, dryRun bool) {
	verb := "registered in"
	if dryRun {
		verb = "would write"
	}
	if !r.Changed {
		verb = r.Detail + ":"
	}
	fmt.Printf("  %-30s %s %s\n", c.title, verb, DisplayPath(root, r.Path))
}

// GlobalClientNames returns a comma-separated list of global-scope client names.
func GlobalClientNames() string {
	var names []string
	for _, c := range clientList {
		if c.scope == ScopeGlobal {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// SelectClients chooses clients based on a spec: empty means detected project
// clients, "all" means every project client, otherwise a comma-separated list.
func SelectClients(root, spec string) ([]Client, error) {
	var out []Client
	switch strings.TrimSpace(spec) {
	case "":
		for _, c := range clientList {
			if c.scope == ScopeProject && c.detect != nil && c.detect(root) {
				out = append(out, c)
			}
		}
		return out, nil
	case "all":
		for _, c := range clientList {
			if c.scope == ScopeProject {
				out = append(out, c)
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		c, ok := LookupClient(n)
		if !ok {
			return nil, fmt.Errorf("unknown client %q (known: %s)", n, ClientNames())
		}
		if !seen[c.name] {
			seen[c.name] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// UnregisterResult is what happened for one teardown step.
type UnregisterResult struct {
	Name    string
	Skipped bool
	Detail  string
	Err     error
}

// UnregisterProject removes githints' entry from every project-level
// client config. Shared files are processed once. Global configs are not
// touched: they belong to the machine, not the project.
func UnregisterProject(root string, dryRun bool) ([]UnregisterResult, error) {
	var out []UnregisterResult
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
			name := filepath.Base(p)
			r := UnregisterResult{Name: name}
			// Continue keeps githints in a dedicated file; deleting the file
			// is cleaner than leaving an empty mcpServers object.
			if c.name == "continue" {
				if _, err := os.Stat(p); err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						r.Skipped = true
						r.Detail = "not registered"
					} else {
						r.Err = err
					}
					out = append(out, r)
					continue
				}
				if !dryRun {
					r.Err = os.Remove(p)
				}
				out = append(out, r)
				continue
			}
			var changed bool
			if c.toml {
				changed, r.Err = unregisterTOML(p, dryRun)
			} else {
				changed, r.Err = unregisterJSON(p, c.keyPath, dryRun)
			}
			if !changed && r.Err == nil {
				r.Skipped = true
				r.Detail = "not registered"
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// unregisterJSON removes the githints server entry from a JSON config. A
// missing file or missing entry is not an error. The file is rewritten only
// when the entry was actually present.
func unregisterJSON(path string, keyPath []string, dryRun bool) (bool, error) {
	data, err := readConfig(path)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return false, nil
	}
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false, fmt.Errorf("%s is not plain JSON (%v); remove the githints entry by hand", path, err)
	}
	servers := nest(doc, keyPath)
	if _, ok := servers["githints"]; !ok {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	delete(servers, "githints")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	return true, writeConfig(path, append(out, '\n'), false)
}

// unregisterTOML removes the [mcp_servers.githints] table Codex got. The
// file is only appended to on install, so the table is at the end; the scan
// handles it anywhere anyway.
func unregisterTOML(path string, dryRun bool) (bool, error) {
	data, err := readConfig(path)
	if err != nil {
		return false, err
	}
	start := bytes.Index(data, []byte("[mcp_servers.githints]"))
	if start == -1 {
		return false, nil
	}
	// The table runs to the next table header or EOF.
	rest := data[start+len("[mcp_servers.githints]"):]
	end := len(data)
	if idx := bytes.Index(rest, []byte("\n[")); idx != -1 {
		end = start + len("[mcp_servers.githints]") + idx + 1
	}
	if dryRun {
		return true, nil
	}
	out := append(data[:start], data[end:]...)
	// Collapse the blank line registerTOML added before the table.
	out = bytes.ReplaceAll(out, []byte("\n\n\n"), []byte("\n\n"))
	return true, writeConfig(path, out, false)
}

// ProjectConfigFiles lists every project-level config path, for doctor.
// Paths are deduplicated because several clients (e.g. Claude Code and Pi)
// share one file.
func ProjectConfigFiles(root string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range clientList {
		if c.scope != ScopeProject {
			continue
		}
		if ps, err := c.paths(root); err == nil {
			for _, p := range ps {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}
