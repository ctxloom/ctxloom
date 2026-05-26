package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// listRemotesInput is empty — list_remotes takes no arguments, but the SDK
// still wants a struct type for schema inference.
type listRemotesInput struct{}

type searchRemotesInput struct {
	Query    string `json:"query" jsonschema:"Search query - can be a name, tag (tag:foo), author (author:name), or plain text"`
	ItemType string `json:"item_type,omitempty" jsonschema:"Type of items to search for (one of: bundle, fragment, profile; default: all)"`
}

type discoverRemotesInput struct {
	Query    string `json:"query,omitempty" jsonschema:"Optional search term to filter repositories"`
	Source   string `json:"source,omitempty" jsonschema:"Which forge to search (one of: github, gitlab, all; default: all)"`
	MinStars int    `json:"min_stars,omitempty" jsonschema:"Minimum star count filter (default: 0)"`
}

type browseRemoteInput struct {
	Remote   string `json:"remote" jsonschema:"Remote name (from list_remotes)"`
	ItemType string `json:"item_type,omitempty" jsonschema:"Type of items to list (one of: fragment, prompt, profile; default: all)"`
	Path     string `json:"path,omitempty" jsonschema:"Subdirectory path to browse"`
}

type pullRemoteInput struct {
	Reference string `json:"reference" jsonschema:"Remote reference (e.g., 'remote-name/bundle-name' or 'remote-name/bundle-name@v1.0.0')"`
	ItemType  string `json:"item_type" jsonschema:"Type of item to pull (one of: bundle, fragment, profile)"`
}

// pullRemoteResult mirrors the legacy map response shape so existing clients
// keep parsing it. Two fields are conditional in the legacy code:
// installation appears only on bundle pulls that have non-empty Installation,
// and set_as_default appears only when a profile pull auto-promotes to the
// default. omitempty preserves that conditional surfacing.
type pullRemoteResult struct {
	Status       string `json:"status"`
	Reference    string `json:"reference"`
	ItemType     string `json:"item_type"`
	LocalPath    string `json:"local_path"`
	SHA          string `json:"sha"`
	SourceURL    string `json:"source_url"`
	Overwritten  bool   `json:"overwritten"`
	Installation string `json:"installation,omitempty"`
	SetAsDefault bool   `json:"set_as_default,omitempty"`
}

type addRemoteInput struct {
	Name string `json:"name" jsonschema:"Short name for the remote (e.g., 'alice')"`
	URL  string `json:"url" jsonschema:"Repository URL (e.g., 'alice/ctxloom' or 'https://github.com/alice/ctxloom')"`
}

type removeRemoteInput struct {
	Name string `json:"name" jsonschema:"Remote name to remove"`
}

type updateRemoteInput struct {
	Name string `json:"name,omitempty" jsonschema:"Remote name to update (omit to update all)"`
}

func (s *ctxServer) registerRemoteTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_remotes",
			Description: "List configured remote sources for fragments and prompts",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ listRemotesInput) (*mcp.CallToolResult, *operations.ListRemotesResult, error) {
			result, err := operations.ListRemotes(ctx, s.cfg, operations.ListRemotesRequest{})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_remotes",
			Description: "Search for bundles and profiles across ALL configured remotes. Use this when looking for a fragment or profile by name without knowing which remote contains it.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchRemotesInput) (*mcp.CallToolResult, *operations.SearchRemotesResult, error) {
			result, err := operations.SearchRemotes(ctx, s.cfg, operations.SearchRemotesRequest{
				Query:    in.Query,
				ItemType: in.ItemType,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "discover_remotes",
			Description: "Search GitHub/GitLab for ctxloom repositories containing fragments and prompts",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in discoverRemotesInput) (*mcp.CallToolResult, *operations.DiscoverRemotesResult, error) {
			result, err := operations.DiscoverRemotes(ctx, s.cfg, operations.DiscoverRemotesRequest{
				Query:    in.Query,
				Source:   in.Source,
				MinStars: in.MinStars,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "browse_remote",
			Description: "List items (fragments, prompts, profiles) available in a remote repository",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in browseRemoteInput) (*mcp.CallToolResult, *operations.BrowseRemoteResult, error) {
			result, err := operations.BrowseRemote(ctx, s.cfg, operations.BrowseRemoteRequest{
				Remote:   in.Remote,
				ItemType: in.ItemType,
				Path:     in.Path,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "pull_remote",
			Description: "Pull (install) a bundle or profile from a remote repository. Fetches and installs in one step.",
		},
		s.handlePullRemote)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "add_remote",
			Description: "Register a new remote source for fragments and prompts",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in addRemoteInput) (*mcp.CallToolResult, *operations.AddRemoteResult, error) {
			result, err := operations.AddRemote(ctx, s.cfg, operations.AddRemoteRequest{
				Name: in.Name,
				URL:  in.URL,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "remove_remote",
			Description: "Remove a registered remote source",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeRemoteInput) (*mcp.CallToolResult, *operations.RemoveRemoteResult, error) {
			result, err := operations.RemoveRemote(ctx, s.cfg, operations.RemoveRemoteRequest{
				Name: in.Name,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_remote",
			Description: "Fetch the latest changes for cached remote repos. If name is specified, updates only that remote; otherwise updates all.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in updateRemoteInput) (*mcp.CallToolResult, *operations.UpdateRemoteResult, error) {
			result, err := operations.UpdateRemote(ctx, s.cfg, operations.UpdateRemoteRequest{
				Name: in.Name,
			})
			return nil, result, err
		})
}

// handlePullRemote is broken out because it composes two operations
// (FetchRemoteContent + WriteRemoteItem) and emits a synthesized result
// type plus optional auto-default-promotion side effect — too much for an
// inline closure to remain readable.
func (s *ctxServer) handlePullRemote(ctx context.Context, _ *mcp.CallToolRequest, in pullRemoteInput) (*mcp.CallToolResult, *pullRemoteResult, error) {
	// Normalize item_type: fragments are stored as bundles for retrieval,
	// so the user-facing "fragment" name maps to the "bundle" wire type
	// for fetch/write. This matches the legacy normalization in
	// cmd/mcp.go's toolPullRemote.
	itemType := in.ItemType
	if itemType == "fragment" {
		itemType = "bundle"
	}

	fetchResult, err := operations.FetchRemoteContent(ctx, s.cfg, operations.FetchRemoteContentRequest{
		Reference: in.Reference,
		ItemType:  itemType,
	})
	if err != nil {
		return nil, nil, err
	}

	writeResult, err := operations.WriteRemoteItem(ctx, s.cfg, operations.WriteRemoteItemRequest{
		Reference: fetchResult.PullToken,
		ItemType:  itemType,
		Content:   []byte(fetchResult.Content),
		SHA:       fetchResult.FullSHA,
	})
	if err != nil {
		return nil, nil, err
	}

	out := &pullRemoteResult{
		Status:      writeResult.Status,
		Reference:   writeResult.Reference,
		ItemType:    writeResult.ItemType,
		LocalPath:   writeResult.LocalPath,
		SHA:         writeResult.SHA,
		SourceURL:   fetchResult.SourceURL,
		Overwritten: writeResult.Overwritten,
	}

	if itemType == "bundle" {
		if bundle, perr := bundles.ParseBundle([]byte(fetchResult.Content)); perr == nil && bundle.Installation != "" {
			out.Installation = bundle.Installation
		}
	}

	// Auto-promote a pulled profile to default when no default is
	// configured. Without this, `ctxloom run` launches with empty
	// context after a fresh init.
	if in.ItemType == "profile" {
		if name, ok := operations.LocalProfileNameFromPath(s.cfg, writeResult.LocalPath); ok {
			if operations.PromoteToDefaultIfFirst(s.cfg, name) {
				out.SetAsDefault = true
			}
		}
	}

	return nil, out, nil
}
