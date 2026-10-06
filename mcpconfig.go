package main

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
	"sort"
	"strings"
	"unicode"

	"github.com/cjrdz/githints/internal/gitutil"
)

// Registering githints with an MCP client means writing one entry into that
// client's config. Every client has its own file, top-level key and entry
// shape, and several of them changed recently, so each one is described here
// once, against its documentation (checked 2026-10), and both `mcp-config`
// and `setup` work from this table.

// clientScope says where a client's config lives.
type clientScope int

const (
	// scopeProject: a file inside the repository. The default, and the only
	// scope `setup` writes without being asked by name.
	scopeProject clientScope = iota
	// scopeGlobal: one file per user, shared by every repository. Written
	// only when the client is named explicitly, with a backup.
	scopeGlobal
	// scopeManual: no file githints can safely write (YAML, or a settings UI).
	// The snippet and steps are printed instead.
	scopeManual
)

// entryCtx is what an entry is built from.
type entryCtx struct {
	command string // githints, or its absolute path when it is not on PATH
	root    string // absolute repository root
}

// mcpClient describes one MCP client.
type mcpClient struct {
	name    string
	title   string
	aliases []string
	scope   clientScope
	// paths returns candidate config files. For project clients it is one
	// path under root. For global clients, every path that exists is written;
	// if none does, the client is treated as not installed.
	paths func(root string) ([]string, error)
	// toml: Codex. Everything else is JSON.
	toml bool
	// keyPath leads from the document root to the object of servers.
	keyPath []string
	entry   func(c entryCtx) any
	// detect reports whether the client looks in use for this repository or
	// on this machine; `setup` registers detected project clients by default.
	detect func(root string) bool
	note   string
}

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

var mcpClientList = []mcpClient{
	{
		name: "claude", title: "Claude Code", aliases: []string{"claude-code", "warp"},
		paths:   projectFile(".mcp.json"),
		keyPath: []string{"mcpServers"},
		// Claude Code passes CLAUDE_PROJECT_DIR, which serve honors.
		entry: func(c entryCtx) any {
			return map[string]any{"type": "stdio", "command": c.command, "args": serveArgs("")}
		},
		detect: func(root string) bool { return onPath("claude") || exists(filepath.Join(root, ".mcp.json")) },
		note:   "Warp reads this file too.",
	},
	{
		name: "opencode", title: "opencode",
		paths:   projectFile("opencode.json"),
		keyPath: []string{"mcp"},
		entry: func(c entryCtx) any {
			return map[string]any{"type": "local", "command": append([]string{c.command}, serveArgs("")...), "enabled": true}
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
		entry:  func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
		detect: func(root string) bool { return onPath("codex") || exists(filepath.Join(root, ".codex")) },
		note:   "Codex reads .codex/config.toml only for projects you have marked trusted. The file pins this checkout's absolute path.",
	},
	{
		name: "vscode", title: "VS Code (Copilot agent mode)", aliases: []string{"code", "copilot"},
		paths:   projectFile(".vscode", "mcp.json"),
		keyPath: []string{"servers"},
		entry: func(c entryCtx) any {
			return map[string]any{"type": "stdio", "command": c.command, "args": serveArgs("")}
		},
		detect: func(root string) bool { return onPath("code") || exists(filepath.Join(root, ".vscode")) },
	},
	{
		name: "cursor", title: "Cursor",
		paths:   projectFile(".cursor", "mcp.json"),
		keyPath: []string{"mcpServers"},
		// Working directory undocumented; Cursor expands ${workspaceFolder},
		// which keeps the file portable.
		entry: func(c entryCtx) any {
			return map[string]any{"type": "stdio", "command": c.command, "args": serveArgs("${workspaceFolder}")}
		},
		detect: func(root string) bool { return onPath("cursor") || exists(filepath.Join(root, ".cursor")) },
	},
	{
		name: "zed", title: "Zed",
		paths:   projectFile(".zed", "settings.json"),
		keyPath: []string{"context_servers"},
		entry: func(c entryCtx) any {
			return map[string]any{"command": c.command, "args": serveArgs(""), "env": map[string]any{}}
		},
		detect: func(root string) bool { return onPath("zed", "zeditor") || exists(filepath.Join(root, ".zed")) },
	},
	{
		name: "kiro", title: "Kiro",
		paths:   projectFile(".kiro", "settings", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry: func(c entryCtx) any {
			return map[string]any{"command": c.command, "args": serveArgs(c.root), "disabled": false}
		},
		detect: func(root string) bool { return onPath("kiro", "kiro-cli") || exists(filepath.Join(root, ".kiro")) },
		note:   "The file pins this checkout's absolute path (Kiro does not document the server's working directory).",
	},
	{
		name: "gemini", title: "Gemini CLI",
		paths:   projectFile(".gemini", "settings.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs("")} },
		detect:  func(root string) bool { return onPath("gemini") || exists(filepath.Join(root, ".gemini")) },
	},
	{
		name: "junie", title: "JetBrains Junie",
		paths:   projectFile(".junie", "mcp", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
		detect:  func(root string) bool { return onPath("junie") || exists(filepath.Join(root, ".junie")) },
		note:    "The file pins this checkout's absolute path.",
	},
	{
		name: "continue", title: "Continue",
		paths:   projectFile(".continue", "mcpServers", "githints.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
		detect:  func(root string) bool { return exists(filepath.Join(root, ".continue")) },
		note:    "The file pins this checkout's absolute path.",
	},
	{
		name: "amazonq", title: "Amazon Q Developer CLI (legacy; Kiro CLI replaces it)", aliases: []string{"q"},
		paths:   projectFile(".amazonq", "mcp.json"),
		keyPath: []string{"mcpServers"},
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
		detect:  func(root string) bool { return exists(filepath.Join(root, ".amazonq")) },
	},
	{
		name: "claude-desktop", title: "Claude Desktop", scope: scopeGlobal,
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
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
	},
	{
		name: "windsurf", title: "Windsurf / Devin Desktop", aliases: []string{"devin"}, scope: scopeGlobal,
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
		entry:   func(c entryCtx) any { return map[string]any{"command": c.command, "args": serveArgs(c.root)} },
		note:    "Applies to the Cascade agent; Devin Local uses a different config.",
	},
	{
		name: "cline", title: "Cline CLI", scope: scopeGlobal,
		paths: func(string) ([]string, error) {
			if p := os.Getenv("CLINE_MCP_SETTINGS_PATH"); p != "" {
				return []string{p}, nil
			}
			p, err := homePath(".cline", "data", "settings", "cline_mcp_settings.json")
			return []string{p}, err
		},
		keyPath: []string{"mcpServers"},
		entry: func(c entryCtx) any {
			return map[string]any{"command": c.command, "args": serveArgs(c.root), "disabled": false, "autoApprove": []string{}}
		},
		note: "The Cline IDE extension keeps its settings elsewhere: open its MCP Servers view and paste the same entry.",
	},
	{
		name: "goose", title: "Goose", scope: scopeManual,
		note: "Goose keeps extensions in YAML (~/.config/goose/config.yaml). Add:\n\n" +
			"extensions:\n  githints:\n    type: stdio\n    name: githints\n    enabled: true\n    cmd: %[1]s\n    args: [\"serve\", \"-root=%[2]s\"]\n    envs: {}\n    timeout: 300\n",
	},
	{
		name: "jetbrains", title: "JetBrains AI Assistant", scope: scopeManual,
		note: "AI Assistant has no config file to write. In Settings > Tools > AI Assistant > Model Context Protocol,\n" +
			"add a server with command %[1]s and arguments: serve -root=%[2]s\n(or use `githints mcp-config junie -write` for Junie).",
	},
}

func lookupClient(name string) (mcpClient, bool) {
	name = strings.ToLower(name)
	for _, c := range mcpClientList {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return mcpClient{}, false
}

func mcpClientNames() string {
	names := make([]string, 0, len(mcpClientList))
	for _, c := range mcpClientList {
		names = append(names, c.name)
	}
	return strings.Join(names, "|")
}

// githintsCommand is what configs should run. Plain "githints" when that is
// on PATH, so a committed project file works on every machine; otherwise
// this binary's absolute path, which is only right on this one. GUI apps
// (Claude Desktop) often get a minimal PATH, so global configs always use
// the absolute path.
func githintsCommand(global bool) (cmd string, portable bool) {
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

// registerResult is what happened for one file.
type registerResult struct {
	path    string
	changed bool   // the file was (or, in a dry run, would be) written
	detail  string // e.g. "already registered"
}

// register adds githints to one client's config(s). An entry that is already
// there is left alone unless update is set, and then only githints' own entry
// is replaced. A file it cannot parse exactly is never rewritten.
func register(c mcpClient, root string, dryRun, update bool) ([]registerResult, error) {
	cmd, _ := githintsCommand(c.scope == scopeGlobal)
	ctx := entryCtx{command: cmd, root: root}
	paths, err := c.paths(root)
	if err != nil {
		return nil, err
	}
	name := "githints"
	if c.scope == scopeGlobal {
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

	var out []registerResult
	for _, p := range paths {
		var r registerResult
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

func registerJSON(path, name string, ctx entryCtx, c mcpClient, dryRun, update bool) (registerResult, error) {
	res := registerResult{path: path}
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
				path, err, strings.Join(c.keyPath, "."), snippetJSON(c, name, ctx))
		}
	}
	servers := nest(doc, c.keyPath)
	want := c.entry(ctx)
	if existing, ok := servers[name]; ok && update {
		a, _ := json.Marshal(existing) // decoded JSON re-encodes
		b, _ := json.Marshal(want)     // maps of strings, bools and slices
		if bytes.Equal(a, b) {
			res.detail = "up to date"
			return res, nil
		}
	} else if existing, ok := servers[name]; ok {
		res.detail = "already registered"
		if c.scope == scopeGlobal {
			if b, _ := json.Marshal(existing); !bytes.Contains(b, []byte(jsonEscaped(ctx.root))) {
				res.detail = "an entry named " + name + " exists for another checkout; left unchanged"
			}
		}
		return res, nil
	}
	servers[name] = want
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, err
	}
	res.changed = true
	if dryRun {
		return res, nil
	}
	return res, writeConfig(path, append(out, '\n'), c.scope == scopeGlobal)
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
func registerTOML(path, name string, ctx entryCtx, c mcpClient, dryRun bool) (registerResult, error) {
	res := registerResult{path: path}
	data, err := readConfig(path)
	if err != nil {
		return res, err
	}
	for _, m := range tomlTableRe.FindAllSubmatch(data, -1) {
		if string(m[1]) == name {
			res.detail = "already registered (TOML is never rewritten; edit [mcp_servers." + name + "] by hand to change it)"
			return res, nil
		}
	}
	if bytes.Contains(data, []byte("mcp_servers."+name)) || bytes.Contains(data, []byte(`mcp_servers."`+name+`"`)) {
		res.detail = "already registered"
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
	b.WriteString(snippetTOML(name, ctx))
	res.changed = true
	if dryRun {
		return res, nil
	}
	return res, writeConfig(path, b.Bytes(), c.scope == scopeGlobal)
}

// snippetTOML renders Codex's table. Only command and args: Codex rejects
// unknown fields. The root is pinned because Codex does not document the
// server's working directory.
func snippetTOML(name string, ctx entryCtx) string {
	args := serveArgs(ctx.root)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = tomlString(a)
	}
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n", name, tomlString(ctx.command), strings.Join(quoted, ", "))
}

func snippetJSON(c mcpClient, name string, ctx entryCtx) string {
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

func manualText(c mcpClient, root string) string {
	cmd, _ := githintsCommand(true)
	return fmt.Sprintf(c.note, cmd, root)
}

// cmdMCPConfig prints, or with -write adds, githints' entry for one client.
func cmdMCPConfig(args []string) error {
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
	client, ok := lookupClient(name)
	if !ok {
		return fmt.Errorf("usage: githints mcp-config <%s> [-write]", mcpClientNames())
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if client.scope == scopeManual {
		fmt.Println(manualText(client, root))
		if *write {
			return fmt.Errorf("-write is not supported for %s; follow the steps above", client.title)
		}
		return nil
	}

	if !*write {
		cmd, _ := githintsCommand(client.scope == scopeGlobal)
		ctx := entryCtx{command: cmd, root: root}
		name := "githints"
		if client.scope == scopeGlobal {
			name = globalServerName(root)
		}
		paths, err := client.paths(root)
		if err != nil {
			return err
		}
		rel := make([]string, len(paths))
		for i, p := range paths {
			rel[i] = displayPath(root, p)
		}
		fmt.Printf("%s: %s\n\n", client.title, strings.Join(rel, " or "))
		if client.toml {
			fmt.Print(snippetTOML(name, ctx))
		} else {
			fmt.Println(snippetJSON(client, name, ctx))
		}
		if client.note != "" {
			fmt.Printf("\n%s\n", client.note)
		}
		fmt.Println("\nRe-run with -write to add it.")
		return nil
	}

	results, err := register(client, root, false, *update)
	for _, r := range results {
		printResult(root, client, r, false)
	}
	return err
}

func displayPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return p
}

func printResult(root string, c mcpClient, r registerResult, dryRun bool) {
	verb := "registered in"
	if dryRun {
		verb = "would write"
	}
	if !r.changed {
		verb = r.detail + ":"
	}
	fmt.Printf("  %-30s %s %s\n", c.title, verb, displayPath(root, r.path))
}

// cmdSetup is the one command to run in a new repository: it initializes
// githints if needed, registers the MCP server with the clients in use, and
// reports what still needs attention.
func cmdSetup(args []string) error {
	fset := flag.NewFlagSet("setup", flag.ExitOnError)
	clients := fset.String("clients", "", "comma-separated clients to register (default: those detected); 'all' for every project-level client. Global ones ("+globalClientNames()+") are written only when named")
	dryRun := fset.Bool("dry-run", false, "show what would be written, write nothing")
	share := fset.Bool("share", false, "passed to init: share rendered markdown with the team")
	chain := fset.Bool("chain", false, "passed to init: keep existing git hooks and run them first")
	update := fset.Bool("update", false, "replace githints' existing entries (e.g. after putting githints on PATH); other servers are never touched")
	list := fset.Bool("list", false, "list the supported clients and whether each is detected, then exit")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (clients go in -clients=a,b)", fset.Arg(0))
	}
	root, err := gitutil.RepoRoot()
	if err != nil {
		return fmt.Errorf("not inside a git repo: %w", err)
	}

	if *list {
		for _, c := range mcpClientList {
			state := ""
			switch {
			case c.scope == scopeManual:
				state = "manual steps"
			case c.scope == scopeGlobal:
				state = "global; name it to write"
			case c.detect != nil && c.detect(root):
				state = "detected"
			}
			fmt.Printf("  %-16s %-56s %s\n", c.name, c.title, state)
		}
		return nil
	}

	selected, err := selectClients(root, *clients)
	if err != nil {
		return err
	}

	if !exists(filepath.Join(root, ".githints", "store.db")) {
		if *dryRun {
			fmt.Println("would run: githints init")
		} else {
			var initArgs []string
			if *share {
				initArgs = append(initArgs, "-share")
			}
			if *chain {
				initArgs = append(initArgs, "-chain")
			}
			if err := cmdInitQuiet(initArgs); err != nil {
				return err
			}
			fmt.Println("initialized githints (hooks, store, agent instructions)")
		}
	}

	if len(selected) == 0 {
		fmt.Println("\nNo MCP clients detected. Name them, e.g.:\n  githints setup -clients=claude,vscode\n(`githints setup -list` shows them all.)")
		return nil
	}

	fmt.Println("\nMCP clients:")
	var failed, manual []string
	notes := map[string]bool{}
	for _, c := range selected {
		if c.scope == scopeManual {
			manual = append(manual, c.name)
			continue
		}
		results, err := register(c, root, *dryRun, *update)
		for _, r := range results {
			printResult(root, c, r, *dryRun)
		}
		if err != nil {
			fmt.Printf("  %-30s FAILED: %v\n", c.title, err)
			failed = append(failed, c.name)
			continue
		}
		if c.note != "" {
			notes[c.title+": "+c.note] = true
		}
	}
	for _, n := range manual {
		c, _ := lookupClient(n)
		fmt.Printf("\n%s needs a manual step:\n%s\n", c.title, manualText(c, root))
	}
	if _, portable := githintsCommand(false); !portable {
		notes["githints is not on PATH, so these configs name this binary by its absolute path and work on this machine only. Put githints on PATH, then `githints setup -update` before committing them."] = true
	}
	if len(notes) > 0 {
		keys := make([]string, 0, len(notes))
		for k := range notes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Println("\nNotes:")
		for _, k := range keys {
			fmt.Println("  - " + k)
		}
	}
	if !*dryRun {
		fmt.Println("\nRestart (or reload) your editor or agent so it picks up the new server, then run `githints doctor`.")
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not register %s", strings.Join(failed, ", "))
	}
	return nil
}

func globalClientNames() string {
	var names []string
	for _, c := range mcpClientList {
		if c.scope == scopeGlobal {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

func selectClients(root, spec string) ([]mcpClient, error) {
	var out []mcpClient
	switch strings.TrimSpace(spec) {
	case "":
		for _, c := range mcpClientList {
			if c.scope == scopeProject && c.detect != nil && c.detect(root) {
				out = append(out, c)
			}
		}
		return out, nil
	case "all":
		for _, c := range mcpClientList {
			if c.scope == scopeProject {
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
		c, ok := lookupClient(n)
		if !ok {
			return nil, fmt.Errorf("unknown client %q (known: %s)", n, mcpClientNames())
		}
		if !seen[c.name] {
			seen[c.name] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// projectConfigFiles lists every project-level config path, for doctor.
func projectConfigFiles(root string) []string {
	var out []string
	for _, c := range mcpClientList {
		if c.scope != scopeProject {
			continue
		}
		if ps, err := c.paths(root); err == nil {
			out = append(out, ps...)
		}
	}
	return out
}
