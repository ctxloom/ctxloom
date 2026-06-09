package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// Phase 4.1 foundation. Resources are the LCD-cheap counterpart to tools:
// they consume no per-resource schema budget on initialize and clients
// pull them on demand. Today this file registers a small starter set —
// help, recent sessions, task summary — establishing the pattern so the
// listing-tool → resource migration (list_bundles/get_fragment/etc.) can
// happen incrementally without re-architecting the server each time.

const (
	resourceHelpURI           = "ctxloom://help"
	resourceSessionsRecentURI = "ctxloom://sessions/recent"
	resourceTasksSummaryURI   = "ctxloom://tasks/summary"
	resourceFragmentsURI      = "ctxloom://fragments"
	resourceProfilesURI       = "ctxloom://profiles"
	resourcePromptsURI        = "ctxloom://prompts"
	resourceRemotesURI        = "ctxloom://remotes"
	resourceMCPServersURI     = "ctxloom://mcp-servers"
	resourceSessionsURI       = "ctxloom://sessions"
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

	// Listing resources (Phase 4 Lever A migration). Each one mirrors a
	// formerly-MCP-tool listing endpoint. The corresponding tools have
	// been pruned from their register*Tools functions.
	server.AddResource(&mcp.Resource{
		URI:         resourceFragmentsURI,
		Name:        "fragments",
		Description: "All local context fragments with tags and source locations. Replaces the list_fragments tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceFragments)

	server.AddResource(&mcp.Resource{
		URI:         resourceProfilesURI,
		Name:        "profiles",
		Description: "All configured profiles with their bundle lists. Replaces the list_profiles tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceProfiles)

	server.AddResource(&mcp.Resource{
		URI:         resourcePromptsURI,
		Name:        "prompts",
		Description: "All available prompts with descriptions. Replaces the list_prompts tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourcePrompts)

	server.AddResource(&mcp.Resource{
		URI:         resourceRemotesURI,
		Name:        "remotes",
		Description: "Configured remote sources. Replaces the list_remotes tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceRemotes)

	server.AddResource(&mcp.Resource{
		URI:         resourceMCPServersURI,
		Name:        "mcp servers",
		Description: "Configured MCP servers per backend. Replaces the list_mcp_servers tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceMCPServers)

	server.AddResource(&mcp.Resource{
		URI:         resourceSessionsURI,
		Name:        "sessions",
		Description: "All harp-named sessions across every project. For the cwd-filtered view, use ctxloom://sessions/recent.",
		MIMEType:    "application/yaml",
	}, s.handleResourceSessionsAll)

	// Templated resources for single-record lookup. URI templates use
	// RFC 6570; the handler parses the URI to extract the {name} segment.
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "ctxloom://fragments/{name}",
		Name:        "fragment",
		Description: "A single fragment's content by name. Replaces the get_fragment tool.",
		MIMEType:    "text/markdown",
	}, s.handleResourceFragment)

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "ctxloom://profiles/{name}",
		Name:        "profile",
		Description: "A single profile's config by name. Replaces the get_profile tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceProfile)

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "ctxloom://prompts/{name}",
		Name:        "prompt",
		Description: "A single prompt's content by name. Replaces the get_prompt tool.",
		MIMEType:    "text/markdown",
	}, s.handleResourcePrompt)

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "ctxloom://remotes/{name}/contents",
		Name:        "remote contents",
		Description: "Bundles and profiles available in a configured remote, by remote name. Replaces the browse_remote tool.",
		MIMEType:    "application/yaml",
	}, s.handleResourceRemoteContents)
}

func (s *ctxServer) handleResourceHelp(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	body := strings.TrimSpace(`
# ctxloom resources

ctxloom exposes read-only listings as MCP resources. Fetch them via the MCP
client's resources/read with the URI. Retrieval tools (assemble_context,
search_content, search_library, session and task tools) are the only tools;
all management is done with the ctxloom CLI.

## Available URIs

- ` + "`ctxloom://help`" + ` — this file.
- ` + "`ctxloom://fragments`" + `, ` + "`ctxloom://prompts`" + `, ` + "`ctxloom://profiles`" + ` — listings;
  append ` + "`/{name}`" + ` for a single item's content.
- ` + "`ctxloom://remotes`" + ` — configured remotes; ` + "`ctxloom://remotes/{name}/contents`" + `
  lists a remote's bundles and profiles.
- ` + "`ctxloom://mcp-servers`" + ` — configured MCP servers.
- ` + "`ctxloom://sessions`" + ` — all recorded sessions for this project.
- ` + "`ctxloom://sessions/recent`" + ` — harp-named sessions for the current project,
  most recent first. Each row: harp_name, started_at, summary, session_id.
- ` + "`ctxloom://tasks/summary`" + ` — per-status task counts + in-progress harp IDs
  from .ctxloom/tasks.md. Equivalent to task_list(include_summary=true).summary.
`)
	return resourceText(req.Params.URI, "text/markdown", body), nil
}

func (s *ctxServer) handleResourceSessionsRecent(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	wd, _ := os.Getwd()
	entries, err := operations.ListSessionsForProject(wd)
	if err != nil {
		return nil, fmt.Errorf("session index: %w", err)
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
	res, err := operations.ListTasks(taskContext(), nil, "", false, true)
	if err != nil {
		return nil, err
	}
	warnTask(res.Warning)
	sum := res.Summary
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
		"path":        res.Path,
		"counts":      counts,
		"in_progress": sum.InProgress,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal summary: %w", err)
	}
	return resourceText(req.Params.URI, "application/yaml", string(out)), nil
}

// --- Listings (Phase 4 Lever A) ---

func (s *ctxServer) handleResourceFragments(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, err := operations.ListFragments(ctx, s.cfg, operations.ListFragmentsRequest{})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

func (s *ctxServer) handleResourceProfiles(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, err := operations.ListProfiles(ctx, s.cfg, operations.ListProfilesRequest{})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

func (s *ctxServer) handleResourcePrompts(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, err := operations.ListPrompts(ctx, s.cfg, operations.ListPromptsRequest{})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

func (s *ctxServer) handleResourceRemotes(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, err := operations.ListRemotes(ctx, s.cfg, operations.ListRemotesRequest{})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

func (s *ctxServer) handleResourceMCPServers(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	result, err := operations.ListMCPServers(ctx, s.cfg, operations.ListMCPServersRequest{})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

// handleResourceSessionsAll returns every harp-named session in the index,
// not project-filtered. Mirrors `ctxloom session list --all`.
func (s *ctxServer) handleResourceSessionsAll(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	entries, err := operations.ListSessions()
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, sessions.Index{Sessions: entries})
}

// --- Single-record templates (Phase 4 Lever A) ---

// extractURIName pulls the trailing `{name}` segment off a URI like
// "ctxloom://fragments/<name>". Returns the empty string when no segment
// is present (the SDK should never call our template handler in that
// case, but we tolerate it).
func extractURIName(uri, prefix string) string {
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	return strings.TrimPrefix(uri, prefix)
}

func (s *ctxServer) handleResourceFragment(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := extractURIName(req.Params.URI, "ctxloom://fragments/")
	if name == "" {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	result, err := operations.GetFragment(ctx, s.cfg, operations.GetFragmentRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return resourceText(req.Params.URI, "text/markdown", result.Content), nil
}

func (s *ctxServer) handleResourceProfile(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := extractURIName(req.Params.URI, "ctxloom://profiles/")
	if name == "" {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	result, err := operations.GetProfile(ctx, s.cfg, operations.GetProfileRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

func (s *ctxServer) handleResourcePrompt(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := extractURIName(req.Params.URI, "ctxloom://prompts/")
	if name == "" {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	result, err := operations.GetPrompt(ctx, s.cfg, operations.GetPromptRequest{Name: name})
	if err != nil {
		return nil, err
	}
	return resourceText(req.Params.URI, "text/markdown", result.Content), nil
}

func (s *ctxServer) handleResourceRemoteContents(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	// URI shape: ctxloom://remotes/<name>/contents — peel both the prefix
	// and the trailing /contents segment.
	rest := extractURIName(req.Params.URI, "ctxloom://remotes/")
	name := strings.TrimSuffix(rest, "/contents")
	if name == "" || name == rest {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	result, err := operations.BrowseRemote(ctx, s.cfg, operations.BrowseRemoteRequest{Remote: name})
	if err != nil {
		return nil, err
	}
	return marshalResourceYAML(req.Params.URI, result)
}

// marshalResourceYAML is the common YAML-encoding wrapper used by every
// list-style resource handler. Saves a few lines per handler and keeps
// the MIME type consistent.
func marshalResourceYAML(uri string, v any) (*mcp.ReadResourceResult, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal resource: %w", err)
	}
	return resourceText(uri, "application/yaml", string(out)), nil
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
