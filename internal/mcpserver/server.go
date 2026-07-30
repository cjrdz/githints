// Package mcpserver exposes the change log to any MCP-capable agent
// (Claude Code, opencode, etc) over stdio: one write tool (record_change)
// and read tools for catching up on history, auditing diffs, and doing
// timeline forensics.
package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/cjrdz/githints/internal/config"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/index"
	"github.com/cjrdz/githints/internal/index/lang"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/llm"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
)

// Run starts the stdio MCP server. Blocks until the client disconnects.
func Run(root string, st *store.Store, cfg config.Config, version string) error {
	var client *llm.Client
	if cfg.Ollama.Enabled {
		var err error
		client, err = llm.NewClient(cfg)
		if err != nil {
			return fmt.Errorf("ollama client: %w", err)
		}
	}

	s := server.NewMCPServer(
		"githints",
		version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	session := NewSessionTracker(st)

	// addTool registers a tool and marks it as used whenever it runs. Taking
	// the name from the tool itself means a new tool cannot be registered
	// without being tracked, and no name literal is repeated.
	addTool := func(tool mcp.Tool, h server.ToolHandlerFunc) {
		name := tool.Name
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			defer session.MarkToolCalled(name)
			return h(ctx, req)
		})
	}

	addTool(
		mcp.NewTool("get_session_context",
			mcp.WithDescription("Orient yourself at the START of a session, before reading files or "+
				"making edits. Returns when this session began, which githints tools have already been "+
				"used this session, how much history this repo has recorded, and which tools to call "+
				"next. Prefer the suggested history tools over raw file reads — they explain why the "+
				"code is shaped the way it is, so you don't re-litigate settled decisions."),
		),
		handleGetSessionContext(session),
	)

	addTool(
		mcp.NewTool("record_change",
			mcp.WithDescription("Record what changed in a file and why, right after editing it. "+
				"This is the primary way changes get tracked — call it once per file you modify, "+
				"with a concrete summary, not a generic one."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the changed file, e.g. internal/auth/token.go")),
			mcp.WithString("summary", mcp.Required(),
				mcp.Description("One to two sentences: what changed, in plain language")),
			mcp.WithString("reason",
				mcp.Description("Why the change was made, if not obvious from the summary")),
			mcp.WithString("agent_id",
				mcp.Description("Optional agent/session fingerprint, e.g. claude-code-session-abc123")),
		),
		handleRecordChange(root, st),
	)

	addTool(
		mcp.NewTool("get_file_history",
			mcp.WithDescription("Get the recorded change history for one file, newest first."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the file")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 10)")),
		),
		handleFileHistory(st),
	)

	addTool(
		mcp.NewTool("get_recent_changes",
			mcp.WithDescription("Get the most recent changes across the whole repo, newest first. "+
				"Use this to catch up on what happened since your last session. With summarize=true, "+
				"Ollama compresses the list into one or two sentences."),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 20)")),
			mcp.WithBoolean("summarize",
				mcp.Description("If true and Ollama is enabled, return a compressed summary instead of the full list.")),
		),
		handleRecentChanges(st, client),
	)

	addTool(
		mcp.NewTool("search_changes",
			mcp.WithDescription("Full-text search over recorded change summaries and reasons."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search text")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 20)")),
		),
		handleSearch(st),
	)

	addTool(
		mcp.NewTool("get_diff",
			mcp.WithDescription("Show the full unified diff for one file, either from a specific "+
				"commit or from the current working tree vs HEAD. Use this to verify what actually "+
				"changed before trusting a recorded summary — closes the audit loop a summary alone "+
				"can't. With summarize=true, Ollama returns a one-sentence compression instead of the raw diff."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the file")),
			mcp.WithString("hash",
				mcp.Description("Commit hash to diff within. Omit (or empty) for the working-tree diff vs HEAD.")),
			mcp.WithBoolean("summarize",
				mcp.Description("If true and Ollama is enabled, return a one-sentence summary instead of the full diff.")),
		),
		handleGetDiff(client),
	)

	addTool(
		mcp.NewTool("get_changes_in_range",
			mcp.WithDescription("Timeline forensics: list changes whose recorded_at timestamp falls "+
				"between since and until (inclusive). Timestamps can be Unix seconds or RFC3339. "+
				"Use this to answer 'what happened to auth.go last week?'."),
			mcp.WithString("since", mcp.Required(), mcp.Description("Start timestamp (RFC3339 or Unix seconds)")),
			mcp.WithString("until", mcp.Required(), mcp.Description("End timestamp (RFC3339 or Unix seconds)")),
			mcp.WithString("file", mcp.Description("Optional repo-relative file to restrict to")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 50)")),
		),
		handleChangesInRange(st),
	)

	addTool(
		mcp.NewTool("record_batch",
			mcp.WithDescription("Record multiple file changes in one call. Use this when several "+
				"files were edited in the same conceptual step so the changelog reflects a single batch."),
			mcp.WithArray("changes", mcp.Required(),
				mcp.Description("Array of change objects; each needs file and summary"),
				mcp.Items(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file":    map[string]any{"type": "string", "description": "Repo-relative path to the changed file"},
						"summary": map[string]any{"type": "string", "description": "One to two sentences: what changed"},
						"reason":  map[string]any{"type": "string", "description": "Why the change was made"},
					},
					"required": []string{"file", "summary"},
				}),
			),
			mcp.WithString("agent_id",
				mcp.Description("Optional agent/session fingerprint applied to every change in the batch")),
		),
		handleRecordBatch(root, st),
	)

	idxDB := openIndexDB(root, cfg)
	if idxDB != nil {
		session.SetIndexDB(idxDB)
	}

	addTool(
		mcp.NewTool("list_symbols",
			mcp.WithDescription("List the symbols defined in a source file. Use this to orient "+
				"yourself before editing a file you have not touched this session."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the source file, e.g. internal/store/store.go")),
			mcp.WithNumber("limit", mcp.Description("Max symbols to return (default 50, cap 500)")),
		),
		handleListSymbols(idxDB),
	)

	addTool(
		mcp.NewTool("find_symbol",
			mcp.WithDescription("Find a symbol by exact or prefix name across the whole repo. "+
				"Returns the file, line, and kind for each match."),
			mcp.WithString("name", mcp.Required(),
				mcp.Description("Symbol name to search for (exact or prefix)")),
			mcp.WithNumber("limit", mcp.Description("Max matches to return (default 20, cap 500)")),
		),
		handleFindSymbol(idxDB),
	)

	addTool(
		mcp.NewTool("get_dependents",
			mcp.WithDescription("Reverse import lookup: which files import this one. This is your "+
				"'blast radius' check before refactoring or deleting a file."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the source file, e.g. internal/store/store.go")),
		),
		handleGetDependents(root, idxDB),
	)

	addTool(
		mcp.NewTool("get_index_summary",
			mcp.WithDescription("Return the structural index summary: total files, total symbols, "+
				"last indexed time, language breakdown, and the most imported (hub) files."),
			mcp.WithNumber("limit", mcp.Description("Max hub files to return (default 10, cap 100)")),
		),
		handleGetIndexSummary(idxDB),
	)

	err := server.ServeStdio(s)
	if idxDB != nil {
		_ = idxDB.Close()
	}
	return err
}

func openIndexDB(root string, cfg config.Config) *index.Store {
	if !cfg.Index.Enabled {
		return nil
	}
	db, err := index.Open(lang.IndexDBPath(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "githints: could not open index db for MCP tools: %v\n", err)
		return nil
	}
	return db
}

func handleGetSessionContext(session *SessionTracker) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(session.ContextReport()), nil
	}
}

func handleRecordChange(root string, st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		file, err := req.RequireString("file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := recorder.ValidateFilePath(file); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		summary, err := req.RequireString("summary")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reason := req.GetString("reason", "")
		agentID := req.GetString("agent_id", "")

		key, err := integrity.KeyFromRepo(root)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		err = recorder.Record(st, root, key, recorder.Input{
			FilePath: file,
			Summary:  summary,
			Reason:   reason,
			AgentID:  agentID,
			Source:   "agent",
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("recorded change for %s", file)), nil
	}
}

func handleFileHistory(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		file, err := req.RequireString("file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := recorder.ValidateFilePath(file); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := clampLimit(req.GetInt("limit", 10), 10, 500)

		changes, err := st.FileHistory(file, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func handleRecentChanges(st *store.Store, client *llm.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := clampLimit(req.GetInt("limit", 20), 20, 500)
		changes, err := st.RecentChanges(limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		text := formatChanges(changes)
		if !req.GetBool("summarize", false) || client == nil {
			return mcp.NewToolResultText(text), nil
		}

		summary, err := client.SummarizeText(ctx, text)
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: ollama summarize recent changes: %v\n", err)
			return mcp.NewToolResultText(text), nil
		}
		return mcp.NewToolResultText(summary), nil
	}
}

func handleSearch(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := clampLimit(req.GetInt("limit", 20), 20, 500)

		changes, err := st.Search(query, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func handleGetDiff(client *llm.Client) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		file, err := req.RequireString("file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := recorder.ValidateFilePath(file); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hash := req.GetString("hash", "")

		diff, err := gitutil.FileDiff(hash, file)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if strings.TrimSpace(diff) == "" {
			return mcp.NewToolResultText("no changes"), nil
		}

		if !req.GetBool("summarize", false) || client == nil {
			return mcp.NewToolResultText(diff), nil
		}

		summary, err := client.SummarizeDiff(ctx, file, diff)
		if err != nil {
			fmt.Fprintf(os.Stderr, "githints: ollama summarize diff: %v\n", err)
			return mcp.NewToolResultText(diff), nil
		}
		return mcp.NewToolResultText(summary), nil
	}
}

func handleChangesInRange(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sinceStr, err := req.RequireString("since")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		untilStr, err := req.RequireString("until")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		file := req.GetString("file", "")
		limit := clampLimit(req.GetInt("limit", 50), 50, 500)

		since, err := parseTimestamp(sinceStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("since: %v", err)), nil
		}
		until, err := parseTimestamp(untilStr)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("until: %v", err)), nil
		}
		if file != "" {
			if err := recorder.ValidateFilePath(file); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		changes, err := st.ChangesInRange(since, until, file, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func handleRecordBatch(root string, st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, ok := req.GetArguments()["changes"]
		if !ok {
			return mcp.NewToolResultError("missing required argument: changes"), nil
		}
		arr, ok := raw.([]any)
		if !ok {
			return mcp.NewToolResultError("changes must be an array"), nil
		}
		if len(arr) == 0 {
			return mcp.NewToolResultError("changes array is empty"), nil
		}

		key, err := integrity.KeyFromRepo(root)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		agentID := req.GetString("agent_id", "")

		inputs := make([]recorder.Input, 0, len(arr))
		for i, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("changes[%d] must be an object", i)), nil
			}
			file, err := stringFromMap(m, "file")
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("changes[%d].%s", i, err.Error())), nil
			}
			summary, err := stringFromMap(m, "summary")
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("changes[%d].%s", i, err.Error())), nil
			}
			reason := ""
			if r, ok := m["reason"].(string); ok {
				reason = r
			}
			inputs = append(inputs, recorder.Input{
				FilePath: file,
				Summary:  summary,
				Reason:   reason,
				AgentID:  agentID,
				Source:   "agent",
			})
		}

		if err := recorder.BatchRecord(st, root, key, inputs); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("recorded %d changes", len(inputs))), nil
	}
}

func stringFromMap(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("missing required argument: %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}
	return s, nil
}

// parseTimestamp accepts either a Unix-seconds integer or an RFC3339 string.
func parseTimestamp(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("timestamp is required")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("expected Unix seconds or RFC3339 timestamp")
}

func formatChanges(changes []store.Change) string {
	if len(changes) == 0 {
		return "no matching changes recorded"
	}
	var b strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&b, "[%s] %s (%s", formatTime(c.RecordedAt), c.FilePath, c.Source)
		if c.Branch != "" {
			fmt.Fprintf(&b, ", %s", c.Branch)
		}
		if c.AgentID != "" {
			fmt.Fprintf(&b, ", %s", c.AgentID)
		}
		if c.ClockTamperWarning {
			fmt.Fprintf(&b, " [CLOCK TAMPER WARNING]")
		}
		fmt.Fprintf(&b, ", %s): %s", shortHash(c.CommitHash), c.Summary)
		if c.Reason != "" {
			fmt.Fprintf(&b, " — why: %s", c.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatTime(unix int64) string {
	if unix == 0 {
		return "unknown"
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	if h == "" {
		return "uncommitted"
	}
	return h
}

// clampLimit clamps a user-supplied limit to a sane range. Non-positive
// values fall back to def; anything above max is capped at max.
func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// Index MCP tool handlers.
//
// These are kept alongside the existing handlers in server.go because the tool
// registration wiring lives here, and the handlers are thin wrappers over the
// index.Store. They follow the same conventions: validate inputs, return
// mcp.NewToolResultError(err.Error()) with a nil Go error, and include
// last_indexed_at in every response.

func handleListSymbols(db *index.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db == nil {
			return mcp.NewToolResultError("structural index is not available (indexing may be disabled or the index db is missing)"), nil
		}
		file, err := req.RequireString("file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := recorder.ValidateFilePath(file); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := clampLimit(req.GetInt("limit", 50), 50, 500)

		symbols, err := db.SymbolsForFile(file)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lastIndexedAt, _ := db.LastIndexedAt()

		var b strings.Builder
		fmt.Fprintf(&b, "Symbols in %s (last indexed: %s)\n\n", file, formatIndexTime(lastIndexedAt))
		if len(symbols) == 0 {
			b.WriteString("_no symbols recorded for this file_\n")
			return mcp.NewToolResultText(b.String()), nil
		}
		if len(symbols) > limit {
			fmt.Fprintf(&b, "_showing %d of %d symbols_\n\n", limit, len(symbols))
			symbols = symbols[:limit]
		}
		for _, sym := range symbols {
			fmt.Fprintf(&b, "- `%s` (%s) lines %d-%d", sym.Name, sym.Kind, sym.LineStart, sym.LineEnd)
			if sym.Signature != "" {
				fmt.Fprintf(&b, " — `%s`", sym.Signature)
			}
			b.WriteString("\n")
		}
		return mcp.NewToolResultText(b.String()), nil
	}
}

func handleFindSymbol(db *index.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db == nil {
			return mcp.NewToolResultError("structural index is not available (indexing may be disabled or the index db is missing)"), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := clampLimit(req.GetInt("limit", 20), 20, 500)

		matches, err := db.FindSymbolsByName(name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lastIndexedAt, _ := db.LastIndexedAt()

		var b strings.Builder
		fmt.Fprintf(&b, "Symbol matches for %q (last indexed: %s)\n\n", name, formatIndexTime(lastIndexedAt))
		if len(matches) == 0 {
			b.WriteString("_no matches found_\n")
			return mcp.NewToolResultText(b.String()), nil
		}
		if len(matches) > limit {
			fmt.Fprintf(&b, "_showing %d of %d matches_\n\n", limit, len(matches))
			matches = matches[:limit]
		}
		for _, sym := range matches {
			fmt.Fprintf(&b, "- `%s` (%s) in %s:%d\n", sym.Name, sym.Kind, sym.FilePath, sym.LineStart)
		}
		return mcp.NewToolResultText(b.String()), nil
	}
}

func handleGetDependents(root string, db *index.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db == nil {
			return mcp.NewToolResultError("structural index is not available (indexing may be disabled or the index db is missing)"), nil
		}
		file, err := req.RequireString("file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := recorder.ValidateFilePath(file); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		importPath, err := lang.LocalImportPath(root, file)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("could not resolve import path for %s: %v", file, err)), nil
		}

		imports, err := db.FilesImporting(importPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lastIndexedAt, _ := db.LastIndexedAt()

		var b strings.Builder
		fmt.Fprintf(&b, "Dependents of %s (resolved import path: %s, last indexed: %s)\n\n",
			file, importPath, formatIndexTime(lastIndexedAt))
		if len(imports) == 0 {
			b.WriteString("_no files import this one in the current index_\n")
			return mcp.NewToolResultText(b.String()), nil
		}
		for _, imp := range imports {
			fmt.Fprintf(&b, "- %s imports `%s`\n", imp.FilePath, imp.ImportedPath)
		}
		return mcp.NewToolResultText(b.String()), nil
	}
}

func handleGetIndexSummary(db *index.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if db == nil {
			return mcp.NewToolResultError("structural index is not available (indexing may be disabled or the index db is missing)"), nil
		}
		limit := clampLimit(req.GetInt("limit", 10), 10, 100)

		meta, err := db.Meta()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		files, err := db.FileCount()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		symbols, err := db.SymbolCount()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		imports, err := db.ImportCount()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hubs, err := db.TopFilesByInDegree(limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Structural index summary (last indexed: %s)\n\n", formatIndexTime(meta.LastIndexedAt))
		fmt.Fprintf(&b, "- files indexed: %d\n", files)
		fmt.Fprintf(&b, "- symbols: %d\n", symbols)
		fmt.Fprintf(&b, "- imports recorded: %d\n", imports)
		if len(meta.LanguageCounts) > 0 {
			b.WriteString("- languages:\n")
			for _, langName := range lang.SortedKeys(meta.LanguageCounts) {
				fmt.Fprintf(&b, "  - %s: %d files\n", langName, meta.LanguageCounts[langName])
			}
		}
		if len(hubs) > 0 {
			b.WriteString("\nMost imported files (hubs):\n")
			for _, h := range hubs {
				fmt.Fprintf(&b, "- %d import(s): %s\n", h.Dependents, h.File)
			}
		}
		return mcp.NewToolResultText(b.String()), nil
	}
}

func formatIndexTime(ts int64) string {
	if ts == 0 {
		return "never"
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
