package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

	// Scope flags for unified search. When neither is set, the search is
	// LOCAL only — the historical default, and what the MCP search_content
	// tool advertises (remote discovery is search_library's job).
	SearchLocal  bool `json:"search_local"`  // Search local content (fragments, prompts, profiles, mcp_servers)
	SearchRemote bool `json:"search_remote"` // Also search configured remotes' bundles/profiles (via SearchRemotes; clone-cache reads, no network)

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
	searchTypes := collections.NewSetFrom("fragment", "command", "profile", "mcp_server")
	if len(req.Types) > 0 {
		searchTypes = collections.NewSetFrom(req.Types...)
	}

	query := strings.ToLower(req.Query)

	// Use injected loader or create default
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	// Scope: local unless explicitly narrowed; remote is opt-in.
	searchLocal := req.SearchLocal || !req.SearchRemote

	var results []SearchResult
	if searchLocal {
		// One searcher per content type; the requested-types set gates which run.
		searchers := []struct {
			typ string
			run func() []SearchResult
		}{
			{"fragment", func() []SearchResult { return searchFragments(loader, query, req.Tags) }},
			{"command", func() []SearchResult { return searchCommands(loader, query) }},
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
			// results with every item. Skip them, mirroring the empty-query
			// guard searchRemoteEntries already uses. searchFragments handles
			// the tag-filtered set itself.
			if query == "" && s.typ != "fragment" {
				continue
			}
			results = append(results, s.run()...)
		}
	}
	if req.SearchRemote {
		results = append(results, searchRemoteEntries(ctx, cfg, req, searchTypes)...)
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

// searchCommands returns prompts whose name matches query. Loader errors
// yield no results.
func searchCommands(loader *bundles.Loader, query string) []SearchResult {
	prompts, err := loader.ListAllCommands()
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

	for name, profile := range cfg.Profiles.Definitions {
		match(name, profile.Description, profile.Tags)
	}
	if profileList, err := profileLoader(cfg).List(); err == nil {
		for _, p := range profileList {
			match(p.Name, p.Description, p.Tags)
		}
	}
	return results
}

// searchRemoteEntries maps remote bundle/profile matches (SearchRemotes over
// the local clone caches) into SearchResults, honoring the requested-types
// filter. A remote failure degrades to local-only results with a warning —
// search must not break because one remote is unreachable.
func searchRemoteEntries(ctx context.Context, cfg *config.Config, req SearchContentRequest, searchTypes collections.Set[string]) []SearchResult {
	if req.Query == "" {
		return nil // tags-only searches have no remote counterpart
	}
	// "fragment" maps to bundles, mirroring searchTypeList: fragments live
	// inside bundles, so a fragment search surfaces the bundles to pull.
	wantBundle := searchTypes.Has("bundle") || searchTypes.Has("fragment")
	wantProfile := searchTypes.Has("profile")
	itemType := ""
	switch {
	case wantBundle && !wantProfile:
		itemType = "bundle"
	case wantProfile && !wantBundle:
		itemType = "profile"
	case !wantBundle && !wantProfile:
		return nil
	}
	res, err := SearchRemotes(ctx, cfg, SearchRemotesRequest{Query: req.Query, ItemType: itemType})
	if err != nil {
		clidiag.Warn("ctxloom", "remote search failed: %v", err)
		return nil
	}
	out := make([]SearchResult, 0, len(res.Results))
	for _, e := range res.Results {
		out = append(out, SearchResult{
			Type:   e.Type,
			Name:   e.Name,
			Tags:   e.Tags,
			Source: e.Remote,
			Match:  "name",
		})
	}
	return out
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
