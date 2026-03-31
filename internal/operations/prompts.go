package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// PromptEntry represents a prompt in operation results.
type PromptEntry struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// ListPromptsRequest contains parameters for listing prompts.
type ListPromptsRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListPromptsResult contains the list of prompts.
type ListPromptsResult struct {
	Prompts []PromptEntry `json:"prompts"`
	Count   int           `json:"count"`
}

// ListPrompts returns all prompts matching the criteria.
func ListPrompts(ctx context.Context, cfg *config.Config, req ListPromptsRequest) (*ListPromptsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	prompts, err := loader.ListAllPrompts()
	if err != nil {
		return nil, err
	}

	var result []PromptEntry
	query := strings.ToLower(req.Query)
	for _, p := range prompts {
		// Filter by query if provided
		if query != "" && !strings.Contains(strings.ToLower(p.Name), query) {
			continue
		}
		result = append(result, PromptEntry{
			Name:   p.Name,
			Source: p.Source,
		})
	}

	// Sort results
	reverse := req.SortOrder == "desc"
	sort.Slice(result, func(i, j int) bool {
		cmp := strings.Compare(strings.ToLower(result[i].Name), strings.ToLower(result[j].Name))
		if reverse {
			return cmp > 0
		}
		return cmp < 0
	})

	return &ListPromptsResult{
		Prompts: result,
		Count:   len(result),
	}, nil
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
