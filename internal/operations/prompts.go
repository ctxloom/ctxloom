package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// PromptEntry represents a prompt in operation results. Tags carry the
// bundle's tags merged with the prompt's own; Source is the bundle name,
// which also serves as the grouping key for the CLI's grouped listing.
type PromptEntry struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source"`
}

// ListPromptsRequest contains parameters for listing prompts.
type ListPromptsRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "source"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListPromptsResult contains the list of prompts.
type ListPromptsResult struct {
	Prompts []PromptEntry `json:"prompts"`
	Count   int           `json:"count"`
}

// ListPrompts returns all prompts matching the criteria. Mirrors
// ListFragments: it filters and sorts the raw ContentInfo (so SortBy:"source"
// groups by bundle) before projecting, keeping one read path for both the MCP
// resource surface and the grouped CLI listing.
func ListPrompts(ctx context.Context, cfg *config.Config, req ListPromptsRequest) (*ListPromptsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	infos, err := loader.ListAllPrompts()
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

	result := &ListPromptsResult{
		Prompts: make([]PromptEntry, 0, len(infos)),
		Count:   len(infos),
	}
	for _, info := range infos {
		result.Prompts = append(result.Prompts, PromptEntry{
			Name:   info.Name,
			Tags:   info.Tags,
			Source: info.Source,
		})
	}

	return result, nil
}

// GetPromptRequest contains parameters for getting a prompt.
type GetPromptRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// GetPromptResult contains the prompt content.
type GetPromptResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GetPrompt returns a specific prompt by name.
func GetPrompt(ctx context.Context, cfg *config.Config, req GetPromptRequest) (*GetPromptResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	prompt, err := loader.GetPrompt(req.Name)
	if err != nil {
		return nil, err
	}

	// Clean content: strip leading header lines
	content := prompt.Content
	lines := strings.Split(content, "\n")
	var cleanedLines []string
	skipHeader := true
	for _, line := range lines {
		if skipHeader && strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		skipHeader = false
		cleanedLines = append(cleanedLines, line)
	}
	content = strings.TrimSpace(strings.Join(cleanedLines, "\n"))

	return &GetPromptResult{
		Name:    prompt.Name,
		Content: content,
	}, nil
}
