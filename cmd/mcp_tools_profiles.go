package cmd

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
)

type listProfilesInput struct {
	Query     string `json:"query,omitempty" jsonschema:"Text search on name or description"`
	SortBy    string `json:"sort_by,omitempty" jsonschema:"Sort field (one of: name, default; default: name)"`
	SortOrder string `json:"sort_order,omitempty" jsonschema:"Sort order (one of: asc, desc; default: asc)"`
}

type getProfileInput struct {
	Name string `json:"name" jsonschema:"Profile name"`
}

type createProfileInput struct {
	Name             string   `json:"name" jsonschema:"Profile name"`
	Description      string   `json:"description,omitempty" jsonschema:"Profile description"`
	Parents          []string `json:"parents,omitempty" jsonschema:"Parent profiles to inherit from"`
	Bundles          []string `json:"bundles,omitempty" jsonschema:"Bundle references to include"`
	Tags             []string `json:"tags,omitempty" jsonschema:"Tags to include fragments by"`
	Default          bool     `json:"default,omitempty" jsonschema:"Add this profile to defaults.profiles in config.yaml. Usually unnecessary — the first installed profile is auto-promoted. Pass true only when the user wants this new profile to replace an existing default."`
	ExcludeFragments []string `json:"exclude_fragments,omitempty" jsonschema:"Fragment names to exclude"`
	ExcludePrompts   []string `json:"exclude_prompts,omitempty" jsonschema:"Prompt names to exclude"`
	ExcludeMCP       []string `json:"exclude_mcp,omitempty" jsonschema:"MCP server names to exclude"`
}

// updateProfileInput uses pointer-to-primitive for fields where the legacy
// handler needs to distinguish "not provided" from the zero value: a missing
// Description means "leave unchanged", an empty string Description means
// "set to empty". Same for Default — pointer-bool lets the caller add vs
// remove the profile from defaults.profiles without conflating with "leave
// alone".
type updateProfileInput struct {
	Name                   string   `json:"name" jsonschema:"Profile name to update"`
	Description            *string  `json:"description,omitempty" jsonschema:"New description"`
	AddParents             []string `json:"add_parents,omitempty" jsonschema:"Parent profiles to add"`
	RemoveParents          []string `json:"remove_parents,omitempty" jsonschema:"Parent profiles to remove"`
	AddBundles             []string `json:"add_bundles,omitempty" jsonschema:"Bundles to add"`
	RemoveBundles          []string `json:"remove_bundles,omitempty" jsonschema:"Bundles to remove"`
	AddTags                []string `json:"add_tags,omitempty" jsonschema:"Tags to add"`
	RemoveTags             []string `json:"remove_tags,omitempty" jsonschema:"Tags to remove"`
	Default                *bool    `json:"default,omitempty" jsonschema:"Add (true) or remove (false) this profile from defaults.profiles in config.yaml. Use to switch which profile 'ctxloom run' loads by default."`
	AddExcludeFragments    []string `json:"add_exclude_fragments,omitempty" jsonschema:"Fragment names to add to exclusion list"`
	RemoveExcludeFragments []string `json:"remove_exclude_fragments,omitempty" jsonschema:"Fragment names to remove from exclusion list"`
	AddExcludePrompts      []string `json:"add_exclude_prompts,omitempty" jsonschema:"Prompt names to add to exclusion list"`
	RemoveExcludePrompts   []string `json:"remove_exclude_prompts,omitempty" jsonschema:"Prompt names to remove from exclusion list"`
	AddExcludeMCP          []string `json:"add_exclude_mcp,omitempty" jsonschema:"MCP server names to add to exclusion list"`
	RemoveExcludeMCP       []string `json:"remove_exclude_mcp,omitempty" jsonschema:"MCP server names to remove from exclusion list"`
}

type deleteProfileInput struct {
	Name string `json:"name" jsonschema:"Profile name to delete"`
}

func (s *ctxServer) registerProfileTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_profiles",
			Description: "List all configured profiles with their descriptions",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listProfilesInput) (*mcp.CallToolResult, *operations.ListProfilesResult, error) {
			result, err := operations.ListProfiles(ctx, s.cfg, operations.ListProfilesRequest{
				Query:     in.Query,
				SortBy:    in.SortBy,
				SortOrder: in.SortOrder,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_profile",
			Description: "Get a profile's configuration including fragments, tags, and variables",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getProfileInput) (*mcp.CallToolResult, *operations.GetProfileResult, error) {
			result, err := operations.GetProfile(ctx, s.cfg, operations.GetProfileRequest{
				Name: in.Name,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_profile",
			Description: "Create a new profile with bundles, tags, and/or parent profiles",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in createProfileInput) (*mcp.CallToolResult, *operations.CreateProfileResult, error) {
			result, err := operations.CreateProfile(ctx, s.cfg, operations.CreateProfileRequest{
				Name:             in.Name,
				Description:      in.Description,
				Parents:          in.Parents,
				Bundles:          in.Bundles,
				Tags:             in.Tags,
				Default:          in.Default,
				ExcludeFragments: in.ExcludeFragments,
				ExcludePrompts:   in.ExcludePrompts,
				ExcludeMCP:       in.ExcludeMCP,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_profile",
			Description: "Update an existing profile by adding/removing bundles, tags, or parents",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in updateProfileInput) (*mcp.CallToolResult, *operations.UpdateProfileResult, error) {
			result, err := operations.UpdateProfile(ctx, s.cfg, operations.UpdateProfileRequest{
				Name:                   in.Name,
				Description:            in.Description,
				AddParents:             in.AddParents,
				RemoveParents:          in.RemoveParents,
				AddBundles:             in.AddBundles,
				RemoveBundles:          in.RemoveBundles,
				AddTags:                in.AddTags,
				RemoveTags:             in.RemoveTags,
				Default:                in.Default,
				AddExcludeFragments:    in.AddExcludeFragments,
				RemoveExcludeFragments: in.RemoveExcludeFragments,
				AddExcludePrompts:      in.AddExcludePrompts,
				RemoveExcludePrompts:   in.RemoveExcludePrompts,
				AddExcludeMCP:          in.AddExcludeMCP,
				RemoveExcludeMCP:       in.RemoveExcludeMCP,
			})
			return nil, result, err
		})

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_profile",
			Description: "Delete a profile",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deleteProfileInput) (*mcp.CallToolResult, *operations.DeleteProfileResult, error) {
			result, err := operations.DeleteProfile(ctx, s.cfg, operations.DeleteProfileRequest{
				Name: in.Name,
			})
			return nil, result, err
		})
}
