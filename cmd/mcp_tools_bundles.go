package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// Bundle tools embed nested fragment/prompt/MCP-server entries through
// operations.BundleFragmentInput / BundlePromptInput / BundleMCPInput,
// which already carry the right json tags. The SDK's schema reflector
// walks into those types and produces nested object schemas
// automatically — the long hand-rolled bundleFragmentEntrySchema /
// bundlePromptEntrySchema / bundleMCPEntrySchema helpers in the legacy
// cmd/mcp.go get to disappear.

type createBundleInput struct {
	Name        string                                    `json:"name" jsonschema:"Bundle name (without extension). May include forward-slash path segments to place under a remote subdirectory, e.g. 'personal/foo'. Path-traversal segments ('..'), absolute paths, and null bytes are rejected — names are validated by bundles.ValidateBundleName."`
	Description string                                    `json:"description,omitempty" jsonschema:"Human-readable description"`
	Version     string                                    `json:"version,omitempty" jsonschema:"Bundle version (default: 1.0.0)"`
	Tags        []string                                  `json:"tags,omitempty" jsonschema:"Tags for discovery"`
	Author      string                                    `json:"author,omitempty" jsonschema:"Author identifier"`
	Fragments   map[string]operations.BundleFragmentInput `json:"fragments,omitempty" jsonschema:"Map of fragment name → fragment entry. Fragments are markdown context loaded into the AI session."`
	Prompts     map[string]operations.BundlePromptInput   `json:"prompts,omitempty" jsonschema:"Map of prompt name → prompt entry. Prompts are reusable instructions surfaced as slash commands."`
	MCPServers  map[string]operations.BundleMCPInput      `json:"mcp_servers,omitempty" jsonschema:"Map of MCP server name → server entry. MCP servers are external tool providers added to the backend's MCP configuration."`
}

// updateBundleInput uses pointers (*string, *bool) for fields where the
// caller needs to distinguish "leave unchanged" from "set to zero value".
// Without that, an empty SetDescription would conflate "not provided" with
// "clear the description".
type updateBundleInput struct {
	Name             string                                    `json:"name" jsonschema:"Bundle name to update"`
	SetDescription   *string                                   `json:"set_description,omitempty" jsonschema:"Replace the bundle description"`
	SetVersion       *string                                   `json:"set_version,omitempty" jsonschema:"Replace the bundle version"`
	AddTags          []string                                  `json:"add_tags,omitempty" jsonschema:"Tags to add"`
	RemoveTags       []string                                  `json:"remove_tags,omitempty" jsonschema:"Tags to remove"`
	SetFragments     map[string]operations.BundleFragmentInput `json:"set_fragments,omitempty" jsonschema:"Fragments to add or replace, keyed by name"`
	RemoveFragments  []string                                  `json:"remove_fragments,omitempty" jsonschema:"Fragment names to remove"`
	SetPrompts       map[string]operations.BundlePromptInput   `json:"set_prompts,omitempty" jsonschema:"Prompts to add or replace, keyed by name"`
	RemovePrompts    []string                                  `json:"remove_prompts,omitempty" jsonschema:"Prompt names to remove"`
	SetMCPServers    map[string]operations.BundleMCPInput      `json:"set_mcp_servers,omitempty" jsonschema:"MCP servers to add or replace, keyed by name"`
	RemoveMCPServers []string                                  `json:"remove_mcp_servers,omitempty" jsonschema:"MCP server names to remove"`
	Distill          *bool                                     `json:"distill,omitempty" jsonschema:"Override the bundle's distill flag for this update only"`
}

type deleteBundleInput struct {
	Name string `json:"name" jsonschema:"Bundle name to delete"`
}

type pushBundleInput struct {
	Path     string `json:"path" jsonschema:"Path to the bundle file to push"`
	Title    string `json:"title,omitempty" jsonschema:"PR title (used when create_pr is true)"`
	Message  string `json:"message,omitempty" jsonschema:"Commit message and PR body"`
	CreatePR bool   `json:"create_pr,omitempty" jsonschema:"Open a pull request after pushing (default: false)"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"Show what would be pushed without actually pushing"`
}

func (s *ctxServer) registerBundleTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_bundle",
			Description: "Create a new bundle in .ctxloom/cache/bundles/. A bundle is a YAML file containing fragments (markdown context), prompts (reusable commands), and/or MCP server definitions. Pass nested fragments/prompts/mcp_servers maps to author content in one call.",
		},
		s.handleCreateBundle)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_bundle",
			Description: "Update an existing bundle's metadata, fragments, prompts, or MCP servers. Pass set_* maps to add or replace entries and remove_* arrays to delete them.",
		},
		s.handleUpdateBundle)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_bundle",
			Description: "Delete a bundle from .ctxloom/cache/bundles/",
		},
		s.handleDeleteBundle)

	// Phase 4 Lever B: push_bundle moved to CLI.
	//   ctxloom bundle push <path> [--create-pr]
}

// Bundle tool handlers are named methods (not closures inside
// registerBundleTools) so cmd-layer tests can invoke them directly
// without spinning up the SDK transport. The SDK still calls them
// through the typed function pointer in AddTool — same behavior either
// way.

func (s *ctxServer) handleCreateBundle(ctx context.Context, _ *mcp.CallToolRequest, in createBundleInput) (*mcp.CallToolResult, *operations.CreateBundleResult, error) {
	// bundleDistiller returns nil when no LLM plugin is configured;
	// the operations layer treats that as "save raw content" so
	// authoring still succeeds — distillation can be triggered later
	// via `ctxloom bundle distill`.
	result, err := operations.CreateBundle(ctx, s.cfg, operations.CreateBundleRequest{
		Name:        in.Name,
		Description: in.Description,
		Version:     in.Version,
		Tags:        in.Tags,
		Author:      in.Author,
		Fragments:   in.Fragments,
		Prompts:     in.Prompts,
		MCPServers:  in.MCPServers,
		Distiller:   s.bundleDistiller(),
	})
	return nil, result, err
}

func (s *ctxServer) handleUpdateBundle(ctx context.Context, _ *mcp.CallToolRequest, in updateBundleInput) (*mcp.CallToolResult, *operations.UpdateBundleResult, error) {
	result, err := operations.UpdateBundle(ctx, s.cfg, operations.UpdateBundleRequest{
		Name:             in.Name,
		SetDescription:   in.SetDescription,
		SetVersion:       in.SetVersion,
		AddTags:          in.AddTags,
		RemoveTags:       in.RemoveTags,
		SetFragments:     in.SetFragments,
		RemoveFragments:  in.RemoveFragments,
		SetPrompts:       in.SetPrompts,
		RemovePrompts:    in.RemovePrompts,
		SetMCPServers:    in.SetMCPServers,
		RemoveMCPServers: in.RemoveMCPServers,
		Distill:          in.Distill,
		Distiller:        s.bundleDistiller(),
	})
	return nil, result, err
}

func (s *ctxServer) handleDeleteBundle(ctx context.Context, _ *mcp.CallToolRequest, in deleteBundleInput) (*mcp.CallToolResult, *operations.DeleteBundleResult, error) {
	result, err := operations.DeleteBundle(ctx, s.cfg, operations.DeleteBundleRequest{Name: in.Name})
	return nil, result, err
}

func (s *ctxServer) handlePushBundle(ctx context.Context, _ *mcp.CallToolRequest, in pushBundleInput) (*mcp.CallToolResult, *operations.PushBundleResult, error) {
	result, err := operations.PushBundle(ctx, s.cfg, operations.PushBundleRequest{
		Path:     in.Path,
		Title:    in.Title,
		Message:  in.Message,
		CreatePR: in.CreatePR,
		DryRun:   in.DryRun,
	})
	return nil, result, err
}

// bundleDistiller mirrors the legacy *mcpServer method of the same name.
// It loads the configured LLM plugin and distill prompt and returns an
// operations.Distiller; when no plugin is configured it returns nil and
// the operations layer falls back to storing raw content.
func (s *ctxServer) bundleDistiller() operations.Distiller {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	pluginName := cfg.GetDefaultLLMPlugin()
	if pluginName == "" {
		return nil
	}
	prompt, err := loadDistillPrompt()
	if err != nil {
		return nil
	}
	return &mcpLLMDistillerSDK{
		pluginName: pluginName,
		pluginEnv:  cfg.LM.Plugins[pluginName].Env,
		prompt:     prompt,
	}
}

// mcpLLMDistillerSDK adapts the cmd/bundle.go distill helpers to the
// operations.Distiller interface. State is captured at construction so
// each Distill call is self-contained. Identical in behavior to the
// legacy mcpLLMDistiller — kept under a different name so both versions
// coexist until the legacy mcp.go is deleted at cutover.
type mcpLLMDistillerSDK struct {
	pluginName string
	pluginEnv  map[string]string
	prompt     string
}

func (d *mcpLLMDistillerSDK) Distill(_ context.Context, req operations.DistillRequest) (operations.DistillResult, error) {
	var excludeName string
	switch req.Kind {
	case operations.DistillKindFragment:
		excludeName = "fragments/" + req.Name
	case operations.DistillKindPrompt:
		excludeName = "prompts/" + req.Name
	}
	var siblingCtx string
	if req.Bundle != nil {
		siblingCtx = buildSiblingContext(req.Bundle, excludeName)
	}
	distilled, modelID, err := distillWithModel(d.pluginName, d.pluginEnv, req.Name, req.Content, d.prompt, siblingCtx)
	if err != nil {
		return operations.DistillResult{}, err
	}
	return operations.DistillResult{Distilled: distilled, ModelID: modelID}, nil
}
