package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/tasks"
)

// Phase 4.1 foundation. Resources are the LCD-cheap counterpart to tools:
// they consume no per-resource schema budget on initialize and clients
// pull them on demand. Today this file registers a small starter set —
// help, recent sessions, task summary — establishing the pattern so the
// listing-tool → resource migration (list_bundles/get_fragment/etc.) can
// happen incrementally without re-architecting the server each time.

const (
	resourceHelpURI            = "ctxloom://help"
	resourceSessionsRecentURI  = "ctxloom://sessions/recent"
	resourceTasksSummaryURI    = "ctxloom://tasks/summary"
)

func (s *ctxServer) registerResources(server *mcp.Server) {
	server.AddResource(&mcp.Resource{
		URI:         resourceHelpURI,
		Name:        "ctxloom help",
		Description: "Documentation of every ctxloom resource URI. Read this first if you need to know what's available.",
		MIMEType:    "text/markdown",
	}, s.handleResourceHelp)

	server.AddResource(&mcp.Resource{
		URI:         resourceSessionsRecentURI,
		Name:        "recent sessions",
		Description: "Harp-named sessions for the current project, most recent first. YAML, with harp_name, started_at, summary.",
		MIMEType:    "application/yaml",
	}, s.handleResourceSessionsRecent)

	server.AddResource(&mcp.Resource{
		URI:         resourceTasksSummaryURI,
		Name:        "task summary",
		Description: "Per-status task counts plus harp IDs currently in-progress. Cheap; safe to poll.",
		MIMEType:    "application/yaml",
	}, s.handleResourceTasksSummary)
}

func (s *ctxServer) handleResourceHelp(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	body := strings.TrimSpace(`
# ctxloom resources

ctxloom exposes a small set of MCP resources for data that doesn't
need to live in the tool surface. Fetch them via the MCP client's
resources/read with the URI.

## Available URIs

- ` + "`ctxloom://help`" + ` — this file.
- ` + "`ctxloom://sessions/recent`" + ` — harp-named sessions for the current project,
  most recent first. Each row: harp_name, started_at, summary, session_id.
- ` + "`ctxloom://tasks/summary`" + ` — per-status task counts + in-progress harp IDs
  from .ctxloom/tasks.md. Equivalent to task_list(include_summary=true).summary.

More URIs will appear as the listing-tool → resource migration progresses
(see docs/ctxloom-tasks-plan.md Phase 4.1). Until then, the existing
list_* / get_* MCP tools remain the primary surface for those queries.
`)
	return resourceText(req.Params.URI, "text/markdown", body), nil
}

func (s *ctxServer) handleResourceSessionsRecent(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	mgr, err := sessions.Open("")
	if err != nil {
		return nil, fmt.Errorf("session index: %w", err)
	}
	wd, _ := os.Getwd()
	entries, err := mgr.ListForProject(wd)
	if err != nil {
		return nil, err
	}
	// Cap the body so a project with thousands of sessions doesn't blow
	// the client's context. 25 mirrors the picker's max-after-`m` rough
	// expansion; "recent" is by definition truncated.
	if len(entries) > 25 {
		entries = entries[:25]
	}
	type row struct {
		HarpName  string `yaml:"harp_name"`
		SessionID string `yaml:"session_id,omitempty"`
		StartedAt string `yaml:"started_at"`
		Summary   string `yaml:"summary,omitempty"`
	}
	rows := make([]row, len(entries))
	for i, e := range entries {
		rows[i] = row{
			HarpName:  e.HarpName,
			SessionID: e.SessionID,
			StartedAt: e.StartedAt.Format("2006-01-02 15:04 MST"),
			Summary:   e.Summary,
		}
	}
	out, err := yaml.Marshal(map[string]any{"sessions": rows})
	if err != nil {
		return nil, fmt.Errorf("marshal sessions: %w", err)
	}
	return resourceText(req.Params.URI, "application/yaml", string(out)), nil
}

func (s *ctxServer) handleResourceTasksSummary(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	store, err := tasks.Open(wd)
	if err != nil {
		return nil, err
	}
	sum, err := store.Summarize()
	if err != nil {
		return nil, err
	}
	// Stable key order for diff-friendly output.
	keys := make([]string, 0, len(sum.Counts))
	for k := range sum.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	counts := make(map[string]int, len(keys))
	for _, k := range keys {
		counts[k] = sum.Counts[k]
	}
	out, err := yaml.Marshal(map[string]any{
		"path":        store.Path(),
		"counts":      counts,
		"in_progress": sum.InProgress,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal summary: %w", err)
	}
	return resourceText(req.Params.URI, "application/yaml", string(out)), nil
}

// resourceText wraps a string body into the SDK's text-resource response
// shape with the appropriate URI + MIME type. Used by every text resource
// in this file; a single helper keeps the per-handler boilerplate to one
// return statement.
func resourceText(uri, mimeType, body string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{
			{
				URI:      uri,
				MIMEType: mimeType,
				Text:     body,
			},
		},
	}
}

// suppress unused-import false-positive when filepath is not referenced
// in some compile paths; harmless.
var _ = filepath.Join
