// Package mcpserver exposes the change log to any MCP-capable agent
// (Claude Code, opencode, etc) over stdio: one write tool (record_change)
// and three read tools for catching up on history.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"githints/internal/recorder"
	"githints/internal/store"
)

// Run starts the stdio MCP server. Blocks until the client disconnects.
func Run(root string, st *store.Store) error {
	s := server.NewMCPServer(
		"githints",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	s.AddTool(
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
		),
		handleRecordChange(root, st),
	)

	s.AddTool(
		mcp.NewTool("get_file_history",
			mcp.WithDescription("Get the recorded change history for one file, newest first."),
			mcp.WithString("file", mcp.Required(),
				mcp.Description("Repo-relative path to the file")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 10)")),
		),
		handleFileHistory(st),
	)

	s.AddTool(
		mcp.NewTool("get_recent_changes",
			mcp.WithDescription("Get the most recent changes across the whole repo, newest first. "+
				"Use this to catch up on what happened since your last session."),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 20)")),
		),
		handleRecentChanges(st),
	)

	s.AddTool(
		mcp.NewTool("search_changes",
			mcp.WithDescription("Full-text search over recorded change summaries and reasons."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search text")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 20)")),
		),
		handleSearch(st),
	)

	return server.ServeStdio(s)
}

func handleRecordChange(root string, st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		file, err := requireString(req, "file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		summary, err := requireString(req, "summary")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reason := optionalString(req, "reason")

		err = recorder.Record(st, root, recorder.Input{
			FilePath: file,
			Summary:  summary,
			Reason:   reason,
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
		file, err := requireString(req, "file")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := optionalInt(req, "limit", 10)

		changes, err := st.FileHistory(file, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func handleRecentChanges(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := optionalInt(req, "limit", 20)
		changes, err := st.RecentChanges(limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func handleSearch(st *store.Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := requireString(req, "query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := optionalInt(req, "limit", 20)

		changes, err := st.Search(query, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(formatChanges(changes)), nil
	}
}

func formatChanges(changes []store.Change) string {
	if len(changes) == 0 {
		return "no matching changes recorded"
	}
	var b strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&b, "[%s] %s (%s, %s): %s", c.CreatedAt, c.FilePath, c.Source, shortHash(c.CommitHash), c.Summary)
		if c.Reason != "" {
			fmt.Fprintf(&b, " — why: %s", c.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
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

// requireString reads a required string argument from the tool call.
func requireString(req mcp.CallToolRequest, key string) (string, error) {
	v, ok := req.Params.Arguments[key]
	if !ok {
		return "", fmt.Errorf("missing required argument: %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}
	return s, nil
}

// optionalString reads a non-required string arg, returning "" if absent.
func optionalString(req mcp.CallToolRequest, key string) string {
	v, ok := req.Params.Arguments[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// optionalInt reads a non-required numeric arg (MCP sends numbers as
// float64), returning def if absent.
func optionalInt(req mcp.CallToolRequest, key string, def int) int {
	v, ok := req.Params.Arguments[key]
	if !ok {
		return def
	}
	f, ok := v.(float64)
	if !ok {
		return def
	}
	return int(f)
}
