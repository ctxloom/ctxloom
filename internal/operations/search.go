package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
	Types     []string `json:"types"`      // fragment, command, profile, mcp_server, bundle
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
	// TotalMatches is the match count BEFORE Limit truncated Results. It only
	// differs from Count when the limit bit — callers use TotalMatches-Count
	// to tell the user how many matches the limit hid, rather than letting a
	// truncated answer look like the whole answer.
	TotalMatches int `json:"total_matches,omitempty"`
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
	searchTypes := collections.NewSetFrom("fragment", "command", "skill", "profile", "mcp_server")
	if len(req.Types) > 0 {
		searchTypes = collections.NewSetFrom(req.Types...)
	}

	query := strings.ToLower(req.Query)

	// Use injected loader or create default
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	cat := loader.Catalog()

	// Search is LOCAL only (the remote-search branch, gated on
	// SearchContentRequest.SearchRemote, was unreachable in production —
	// remote discovery is search_library's job, and the CLI's own concurrent
	// local+remote fan-out in cli/search.go never routed through here).
	var results []SearchResult
	// One searcher per content type; the requested-types set gates which run.
	searchers := []struct {
		typ string
		run func() []SearchResult
	}{
		{"fragment", func() []SearchResult { return searchFragments(cat, query, req.Tags) }},
		{"command", func() []SearchResult { return searchCommands(cat, query) }},
		{"skill", func() []SearchResult { return searchSkills(cat, query) }},
		{"profile", func() []SearchResult { return searchProfiles(cfg, query) }},
		{"mcp_server", func() []SearchResult { return searchMCPServers(cfg, query) }},
	}
	for _, s := range searchers {
		if !searchTypes.Has(s.typ) {
			continue
		}
		// A tags-only search (empty query) is fragment-scoped: tags filter
		// fragments only, and the prompt/profile/mcp_server matchers gate
		// solely on strings.Contains(name/desc/command, query), which is
		// unconditionally true for an empty query — so they would flood the
		// results with every item. searchFragments handles the tag-filtered
		// set itself.
		if query == "" && s.typ != "fragment" {
			continue
		}
		results = append(results, s.run()...)
	}

	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "relevance" // name matches first, then others
	}
	sortResults(results, sortBy, req.SortOrder == "desc")

	// Apply limit
	totalMatches := len(results)
	if len(results) > req.Limit {
		results = results[:req.Limit]
	}

	return &SearchContentResult{
		Results:      results,
		Count:        len(results),
		Query:        req.Query,
		TotalMatches: totalMatches,
	}, nil
}

// searchFragments returns fragments whose name (or, failing that, a tag) matches
// query. When tags are given, the candidate set is the tag-filtered fragments;
// otherwise it is all fragments. Loader errors yield no results (search degrades
// rather than failing).
func searchFragments(cat bundles.Catalog, query string, tags []string) []SearchResult {
	var infos []bundles.ContentInfo
	var err error
	if len(tags) > 0 {
		infos, err = cat.ByTags(tags)
	} else {
		infos, err = cat.ListAllFragments()
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

// searchCommands returns prompts whose name matches query. Loader errors
// yield no results.
func searchCommands(cat bundles.Catalog, query string) []SearchResult {
	prompts, err := cat.ListAllCommands()
	if err != nil {
		return nil
	}
	var results []SearchResult
	for _, p := range prompts {
		if strings.Contains(strings.ToLower(p.Name), query) {
			results = append(results, SearchResult{
				Type:   "command",
				Name:   p.Name,
				Source: p.Source,
				Match:  "name",
			})
		}
	}
	return results
}

// searchSkills returns Agent Skill packages whose name or description matches
// query. Loader errors yield no results — mirrors searchCommands.
func searchSkills(cat bundles.Catalog, query string) []SearchResult {
	infos, err := cat.ListAllSkills()
	if err != nil {
		return nil
	}
	var results []SearchResult
	for _, info := range infos {
		matchType := ""
		switch {
		case strings.Contains(strings.ToLower(info.Name), query):
			matchType = "name"
		case strings.Contains(strings.ToLower(info.Description), query):
			matchType = "description"
		case containsTag(info.Tags, query):
			matchType = "tag"
		}
		if matchType != "" {
			results = append(results, SearchResult{
				Type:   "skill",
				Name:   info.Name,
				Tags:   info.Tags,
				Source: info.Bundle,
				Match:  matchType,
			})
		}
	}
	return results
}

// searchProfiles returns profiles matching query by name, then description,
// then tag (first match wins, recorded in Match). It searches BOTH sources a
// profile can come from: inline config definitions and the directory/seeded
// loader ListProfiles uses — directory profiles are the common case, and the
// seeded loader is what makes locked remote profiles visible at all. Loader
// errors degrade to the inline definitions only.
func searchProfiles(cfg *config.Config, query string) []SearchResult {
	var results []SearchResult
	seen := collections.NewSet[string]()
	match := func(name, description string, tags []string) {
		if seen.Has(name) {
			return
		}
		matchType := ""
		switch {
		case strings.Contains(strings.ToLower(name), query):
			matchType = "name"
		case strings.Contains(strings.ToLower(description), query):
			matchType = "description"
		case containsTag(tags, query):
			matchType = "tag"
		}
		if matchType != "" {
			seen.Add(name)
			results = append(results, SearchResult{
				Type:  "profile",
				Name:  name,
				Tags:  tags,
				Match: matchType,
			})
		}
	}

	for name, profile := range cfg.GetProfileDefinitions() {
		match(name, profile.Description, profile.Tags)
	}
	if profileList, err := profileLoader(cfg).List(); err == nil {
		for _, p := range profileList {
			match(p.Name, p.Description, p.Tags)
		}
	}
	return results
}

// searchMCPServers returns configured MCP servers whose name or command matches
// query.
func searchMCPServers(cfg *config.Config, query string) []SearchResult {
	var results []SearchResult
	for name, srv := range cfg.GetMCPServers() {
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
//
// "type" and "relevance" tie constantly (relevanceScore has only 3
// distinct values; every same-type result ties under "type"), and some
// producers (searchProfiles, searchMCPServers) range over a Go map — an
// input order that is NOT stable across calls. sort.SliceStable alone only
// preserves whatever that already-random input order was; a deterministic
// tiebreak (name, then type) is what actually makes tied results — and the
// arbitrary subset a subsequent Limit truncation keeps — the same on every
// run of an identical query.
func sortResults(results []SearchResult, sortBy string, reverse bool) {
	byNameThenType := func(i, j int) bool {
		if ni, nj := strings.ToLower(results[i].Name), strings.ToLower(results[j].Name); ni != nj {
			return ni < nj
		}
		return results[i].Type < results[j].Type
	}
	switch sortBy {
	case "name":
		sort.SliceStable(results, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(results[i].Name), strings.ToLower(results[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "type":
		sort.SliceStable(results, func(i, j int) bool {
			cmp := strings.Compare(results[i].Type, results[j].Type)
			if cmp != 0 {
				if reverse {
					return cmp > 0
				}
				return cmp < 0
			}
			return byNameThenType(i, j)
		})
	case "relevance":
		sort.SliceStable(results, func(i, j int) bool {
			scoreI := relevanceScore(results[i].Match)
			scoreJ := relevanceScore(results[j].Match)
			if scoreI != scoreJ {
				if reverse {
					return scoreI < scoreJ
				}
				return scoreI > scoreJ
			}
			return byNameThenType(i, j)
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
