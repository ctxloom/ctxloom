package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// SkillEntry represents a prompt in operation results. Tags carry the
// bundle's tags merged with the prompt's own; Source is the bundle name,
// which also serves as the grouping key for the CLI's grouped listing.
type SkillEntry struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source"`
}

// ListSkillsRequest contains parameters for listing prompts.
type ListSkillsRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "source"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListSkillsResult contains the list of skills.
type ListSkillsResult struct {
	Skills []SkillEntry `json:"skills"`
	Count  int          `json:"count"`
}

// ListSkills returns all prompts matching the criteria. Mirrors
// ListFragments: it filters and sorts the raw ContentInfo (so SortBy:"source"
// groups by bundle) before projecting, keeping one read path for both the MCP
// resource surface and the grouped CLI listing.
func ListSkills(ctx context.Context, cfg *config.Config, req ListSkillsRequest) (*ListSkillsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	infos, err := loader.ListAllSkills()
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

	result := &ListSkillsResult{
		Skills: make([]SkillEntry, 0, len(infos)),
		Count:   len(infos),
	}
	for _, info := range infos {
		result.Skills = append(result.Skills, SkillEntry{
			Name:   info.Name,
			Tags:   info.Tags,
			Source: info.Source,
		})
	}

	return result, nil
}

// GetSkillRequest contains parameters for getting a prompt.
type GetSkillRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// GetSkillResult contains the prompt content.
type GetSkillResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GetSkill returns a specific prompt by name.
func GetSkill(ctx context.Context, cfg *config.Config, req GetSkillRequest) (*GetSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	prompt, err := loader.GetSkill(req.Name)
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

	return &GetSkillResult{
		Name:    prompt.Name,
		Content: content,
	}, nil
}
