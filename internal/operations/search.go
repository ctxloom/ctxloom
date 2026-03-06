package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
)

// SearchResult represents a single search result.
type SearchResult struct {
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source,omitempty"`
	Match  string   `json:"match,omitempty"` // What matched (name, tag, description)
}

// SearchContentRequest contains parameters for searching content.
type SearchContentRequest struct {
	Query     string   `json:"query"`
	Types     []string `json:"types"`      // fragment, prompt, profile, mcp_server
	Tags      []string `json:"tags"`       // Filter by tags (for fragments)
	SortBy    string   `json:"sort_by"`    // name, type, relevance
	SortOrder string   `json:"sort_order"` // asc, desc
	Limit     int      `json:"limit"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// SearchContentResult contains the search results.
type SearchContentResult struct {
	Results []SearchResult `json:"results"`
	Count   int            `json:"count"`
	Query   string         `json:"query"`
}

// SearchContent searches across all content types.
func SearchContent(ctx context.Context, cfg *config.Config, req SearchContentRequest) (*SearchContentResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	// Determine which types to search
	searchTypes := map[string]bool{
		"fragment":   true,
		"prompt":     true,
		"profile":    true,
		"mcp_server": true,
	}
	if len(req.Types) > 0 {
		searchTypes = make(map[string]bool)
		for _, t := range req.Types {
			searchTypes[t] = true
		}
	}

	var results []SearchResult
	query := strings.ToLower(req.Query)

	// Use injected loader or create default
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	// Search fragments
	if searchTypes["fragment"] {
		var infos []struct {
			Name   string
			Tags   []string
			Source string
		}

		if len(req.Tags) > 0 {
			contentInfos, err := loader.ListByTags(req.Tags)
			if err == nil {
				for _, info := range contentInfos {
					infos = append(infos, struct {
						Name   string
						Tags   []string
						Source string
					}{info.Name, info.Tags, info.Source})
				}
			}
		} else {
			contentInfos, err := loader.ListAllFragments()
			if err == nil {
				for _, info := range contentInfos {
					infos = append(infos, struct {
						Name   string
						Tags   []string
						Source string
					}{info.Name, info.Tags, info.Source})
				}
			}
		}

		for _, info := range infos {
			matchType := ""
			if strings.Contains(strings.ToLower(info.Name), query) {
				matchType = "name"
			} else if containsTag(info.Tags, query) {
				matchType = "tag"
			}
			if matchType != "" {
				results = append(results, SearchResult{
					Type:   "fragment",
					Name:   info.Name,
					Tags:   info.Tags,
					Source: info.Source,
					Match:  matchType,
				})
			}
		}
	}

	// Search prompts
	if searchTypes["prompt"] {
		prompts, err := loader.ListAllPrompts()
		if err == nil {
			for _, p := range prompts {
				if strings.Contains(strings.ToLower(p.Name), query) {
					results = append(results, SearchResult{
						Type:   "prompt",
						Name:   p.Name,
						Source: p.Source,
						Match:  "name",
					})
				}
			}
		}
	}

	// Search profiles
	if searchTypes["profile"] {
		for name, profile := range cfg.Profiles {
			matchType := ""
			if strings.Contains(strings.ToLower(name), query) {
				matchType = "name"
			} else if strings.Contains(strings.ToLower(profile.Description), query) {
				matchType = "description"
			} else if containsTag(profile.Tags, query) {
				matchType = "tag"
			}
			if matchType != "" {
				results = append(results, SearchResult{
					Type:  "profile",
					Name:  name,
					Tags:  profile.Tags,
					Match: matchType,
				})
			}
		}
	}

	// Search MCP servers
	if searchTypes["mcp_server"] {
		for name, srv := range cfg.MCP.Servers {
			if strings.Contains(strings.ToLower(name), query) ||
				strings.Contains(strings.ToLower(srv.Command), query) {
				results = append(results, SearchResult{
					Type:   "mcp_server",
					Name:   name,
					Source: srv.Command,
					Match:  "name",
				})
			}
		}
	}

	// Sort results
	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "relevance" // name matches first, then others
	}
	reverse := req.SortOrder == "desc"

	switch sortBy {
	case "name":
		sort.Slice(results, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(results[i].Name), strings.ToLower(results[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "type":
		sort.Slice(results, func(i, j int) bool {
			cmp := strings.Compare(results[i].Type, results[j].Type)
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "relevance":
		// Name matches first, then tag/description matches
		sort.Slice(results, func(i, j int) bool {
			scoreI := 0
			scoreJ := 0
			if results[i].Match == "name" {
				scoreI = 2
			} else if results[i].Match == "tag" {
				scoreI = 1
			}
			if results[j].Match == "name" {
				scoreJ = 2
			} else if results[j].Match == "tag" {
				scoreJ = 1
			}
			if reverse {
				return scoreI < scoreJ
			}
			return scoreI > scoreJ
		})
	}

	// Apply limit
	if len(results) > req.Limit {
		results = results[:req.Limit]
	}

	return &SearchContentResult{
		Results: results,
		Count:   len(results),
		Query:   req.Query,
	}, nil
}
