package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/shared/collections"
	"github.com/ctxloom/ctxloom/internal/config"
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
	Types     []string `json:"types"`      // fragment, prompt, profile, mcp_server, bundle
	Tags      []string `json:"tags"`       // Filter by tags (for fragments)
	SortBy    string   `json:"sort_by"`    // name, type, relevance
	SortOrder string   `json:"sort_order"` // asc, desc
	Limit     int      `json:"limit"`

	// Scope flags for unified search.
	// When both are false, defaults to searching both local and remote.
	SearchLocal  bool `json:"search_local"`  // Search local content (fragments, prompts, profiles, mcp_servers)
	SearchRemote bool `json:"search_remote"` // Search remote content (bundles, profiles from remotes)

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
	if req.Query == "" && len(req.Tags) == 0 {
		return nil, fmt.Errorf("query or tags required")
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	// Determine which types to search
	searchTypes := collections.NewSetFrom("fragment", "prompt", "profile", "mcp_server")
	if len(req.Types) > 0 {
		searchTypes = collections.NewSetFrom(req.Types...)
	}

	query := strings.ToLower(req.Query)

	// Use injected loader or create default
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	// One searcher per content type; the requested-types set gates which run.
	searchers := []struct {
		typ string
		run func() []SearchResult
	}{
		{"fragment", func() []SearchResult { return searchFragments(loader, query, req.Tags) }},
		{"prompt", func() []SearchResult { return searchPrompts(loader, query) }},
		{"profile", func() []SearchResult { return searchProfiles(cfg, query) }},
		{"mcp_server", func() []SearchResult { return searchMCPServers(cfg, query) }},
	}
	var results []SearchResult
	for _, s := range searchers {
		if searchTypes.Has(s.typ) {
			results = append(results, s.run()...)
		}
	}

	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "relevance" // name matches first, then others
	}
	sortResults(results, sortBy, req.SortOrder == "desc")

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

// searchFragments returns fragments whose name (or, failing that, a tag) matches
// query. When tags are given, the candidate set is the tag-filtered fragments;
// otherwise it is all fragments. Loader errors yield no results (search degrades
// rather than failing).
func searchFragments(loader *bundles.Loader, query string, tags []string) []SearchResult {
	var infos []bundles.ContentInfo
	var err error
	if len(tags) > 0 {
		infos, err = loader.ListByTags(tags)
	} else {
		infos, err = loader.ListAllFragments()
	}
	if err != nil {
		return nil
	}

	var results []SearchResult
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
	return results
}

// searchPrompts returns prompts whose name matches query. Loader errors yield no
// results.
func searchPrompts(loader *bundles.Loader, query string) []SearchResult {
	prompts, err := loader.ListAllPrompts()
	if err != nil {
		return nil
	}
	var results []SearchResult
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
	return results
}

// searchProfiles returns configured profiles matching query by name, then
// description, then tag (first match wins, recorded in Match).
func searchProfiles(cfg *config.Config, query string) []SearchResult {
	var results []SearchResult
	for name, profile := range cfg.Profiles.Definitions {
		matchType := ""
		switch {
		case strings.Contains(strings.ToLower(name), query):
			matchType = "name"
		case strings.Contains(strings.ToLower(profile.Description), query):
			matchType = "description"
		case containsTag(profile.Tags, query):
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
	return results
}

// searchMCPServers returns configured MCP servers whose name or command matches
// query.
func searchMCPServers(cfg *config.Config, query string) []SearchResult {
	var results []SearchResult
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
	return results
}

// sortResults orders results in place by sortBy ("name", "type", or
// "relevance"); reverse flips the order. Relevance ranks name matches above
// tag/description matches.
func sortResults(results []SearchResult, sortBy string, reverse bool) {
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
		sort.Slice(results, func(i, j int) bool {
			scoreI := relevanceScore(results[i].Match)
			scoreJ := relevanceScore(results[j].Match)
			if reverse {
				return scoreI < scoreJ
			}
			return scoreI > scoreJ
		})
	}
}

// relevanceScore ranks match kinds: name (2) above tag (1) above everything
// else (0, e.g. description).
func relevanceScore(match string) int {
	switch match {
	case "name":
		return 2
	case "tag":
		return 1
	default:
		return 0
	}
}
