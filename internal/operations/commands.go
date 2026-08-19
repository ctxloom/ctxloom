package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// CommandEntry represents a command in operation results. Tags carry the
// bundle's tags merged with the command's own; Source is the bundle name,
// which also serves as the grouping key for the CLI's grouped listing.
// Description is the command's own authored `description:` (BundleCommand),
// "" when the bundle didn't set one — carried through so a listing surface
// (e.g. the ACP agent role's available_commands_update, B4) can advertise a
// real human-readable description rather than fabricating one.
type CommandEntry struct {
	Name        string   `json:"name"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source"`
	Description string   `json:"description,omitempty"`
}

// ListCommandsRequest contains parameters for listing commands.
type ListCommandsRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "source"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListCommandsResult contains the list of commands.
type ListCommandsResult struct {
	Commands []CommandEntry `json:"commands"`
	Count    int            `json:"count"`
}

// ListCommands returns all commands matching the criteria. Mirrors
// ListFragments: it filters and sorts the raw ContentInfo (so SortBy:"source"
// groups by bundle) before projecting, keeping one read path for both the MCP
// resource surface and the grouped CLI listing.
func ListCommands(ctx context.Context, cfg *config.Config, req ListCommandsRequest) (*ListCommandsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	infos, err := loader.ListAllCommands()
	if err != nil {
		return nil, err
	}

	// Filter by query if provided (name match, matching the prior behavior).
	if req.Query != "" {
		query := strings.ToLower(req.Query)
		var filtered []bundles.ContentInfo
		for _, info := range infos {
			if strings.Contains(strings.ToLower(info.Name), query) {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

	sortContentInfos(infos, req.SortBy, req.SortOrder)

	result := &ListCommandsResult{
		Commands: make([]CommandEntry, 0, len(infos)),
		Count:    len(infos),
	}
	for _, info := range infos {
		result.Commands = append(result.Commands, CommandEntry{
			Name:        info.Name,
			Tags:        info.Tags,
			Source:      info.Source,
			Description: info.Description,
		})
	}

	return result, nil
}

// GetCommandRequest contains parameters for getting a command.
type GetCommandRequest struct {
	Name string `json:"name"`

	// Pipeline is an optional pre-configured process stage (for testing) —
	// see GetFragmentRequest.Pipeline.
	Pipeline *bundles.Pipeline `json:"-"`
}

// GetCommandResult contains the command content.
type GetCommandResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GetCommand returns a specific command by name.
func GetCommand(ctx context.Context, cfg *config.Config, req GetCommandRequest) (*GetCommandResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	pipe := req.Pipeline
	if pipe == nil {
		// Exposure surface (ctxloom://commands/{name}): gate the resolved content
		// (trust rework, TR5). A withheld command surfaces as errs.ErrCommandWithheld
		// so the resource omits it.
		pipe = exposurePipeline(cfg)
	}

	// A name-addressed ref may pin a content version ("@<commit>"): split it to
	// the canonical version-less ref + parsed version. With no version the
	// unchanged GetCommand path resolves the lockfile-pinned default; a pinned
	// ref resolves that exact historical version via GetPromptAtVersion, gated
	// by ITS OWN content hash (fail-closed on fetch/resolve error).
	ref, version, err := remote.SplitPromptVersion(req.Name)
	if err != nil {
		return nil, err
	}
	prompt, err := getPromptVersioned(pipe, req.Name, ref, version)
	if err != nil {
		return nil, err
	}

	// Clean content: drop a single leading H1 title line. Skip any leading
	// blank lines first (so a leading newline doesn't bypass stripping), then
	// remove exactly the first heading line — not a contiguous run of headings,
	// which would swallow real body sub-headings.
	lines := strings.Split(prompt.Content, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && isATXH1(lines[i]) {
		i++
	}
	content := strings.TrimSpace(strings.Join(lines[i:], "\n"))

	return &GetCommandResult{
		Name:    prompt.Name,
		Content: content,
	}, nil
}

// isATXH1 reports whether line is a CommonMark ATX heading of level 1: exactly
// one '#', then a space/tab or end of line. Deeper headings ("## Deep Dive"), a
// shebang ("#!/usr/bin/env bash") and a bare tag word ("#urgent") are body
// content, not titles — a plain "#" prefix test deletes the command's first
// real line.
func isATXH1(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !ok {
		return false
	}
	return rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")
}

// getPromptVersioned resolves a command honoring a pinned content version. An
// unversioned request takes the lockfile-pinned default path (loader.GetCommand
// on the ORIGINAL name, so today's behavior is unchanged); a "@<commit>"-pinned
// request resolves that exact historical version (loader.GetPromptAtVersion on
// the canonical version-less ref), gated by ITS OWN effective-content hash. A
// version fetch/resolve failure fails closed (returns the error so the surface
// withholds), mirroring loadFragmentRef.
func getPromptVersioned(pipe *bundles.Pipeline, name, ref, version string) (*bundles.LoadedContent, error) {
	if version == "" {
		return pipe.GetCommand(name)
	}
	return pipe.GetPromptAtVersion(ref, version)
}
