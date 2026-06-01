package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
)

// FragmentEntry represents a fragment in operation results.
type FragmentEntry struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source"`
}

// ListFragmentsRequest contains parameters for listing fragments.
type ListFragmentsRequest struct {
	Query     string   `json:"query"`
	Tags      []string `json:"tags"`
	SortBy    string   `json:"sort_by"`    // "name" or "source"
	SortOrder string   `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListFragmentsResult contains the list of fragments.
type ListFragmentsResult struct {
	Fragments []FragmentEntry `json:"fragments"`
	Count     int             `json:"count"`
}

// bundleLoader returns the read-path loader for cfg. Delegates to
// cfg.SeededBundleLoader so remote bundles in the lockfile are visible
// without on-disk extraction. Kept as a package-local helper so existing
// call sites don't need to know about the seeding mechanism.
func bundleLoader(cfg *config.Config) *bundles.Loader {
	return cfg.SeededBundleLoader(cfg.Defaults.ShouldUseDistilled())
}

// ListFragments returns all fragments matching the criteria.
func ListFragments(ctx context.Context, cfg *config.Config, req ListFragmentsRequest) (*ListFragmentsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	var infos []bundles.ContentInfo
	var err error

	if len(req.Tags) > 0 {
		infos, err = loader.ListByTags(req.Tags)
	} else {
		infos, err = loader.ListAllFragments()
	}
	if err != nil {
		return nil, err
	}

	// Filter by query if provided
	if req.Query != "" {
		query := strings.ToLower(req.Query)
		var filtered []bundles.ContentInfo
		for _, info := range infos {
			if strings.Contains(strings.ToLower(info.Name), query) ||
				containsTag(info.Tags, query) {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

	// Sort results
	sortContentInfos(infos, req.SortBy, req.SortOrder)

	result := &ListFragmentsResult{
		Fragments: make([]FragmentEntry, 0, len(infos)),
		Count:     len(infos),
	}

	for _, info := range infos {
		result.Fragments = append(result.Fragments, FragmentEntry{
			Name:   info.Name,
			Tags:   info.Tags,
			Source: info.Source,
		})
	}

	return result, nil
}

// GetFragmentRequest contains parameters for getting a fragment.
type GetFragmentRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// GetFragmentResult contains the fragment content.
type GetFragmentResult struct {
	Name    string   `json:"name"`
	Tags    []string `json:"tags,omitempty"`
	Content string   `json:"content"`
}

// GetFragment returns a specific fragment by name.
func GetFragment(ctx context.Context, cfg *config.Config, req GetFragmentRequest) (*GetFragmentResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	content, err := loader.GetFragment(req.Name)
	if err != nil {
		return nil, err
	}

	return &GetFragmentResult{
		Name:    content.Name,
		Tags:    content.Tags,
		Content: content.Content,
	}, nil
}

// containsTag checks if any tag contains the query string.
func containsTag(tags []string, query string) bool {
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

// sortContentInfos sorts content infos by the specified field and order.
func sortContentInfos(infos []bundles.ContentInfo, sortBy, sortOrder string) {
	if sortBy == "" {
		sortBy = "name"
	}
	reverse := sortOrder == "desc"

	switch sortBy {
	case "name":
		sort.Slice(infos, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(infos[i].Name), strings.ToLower(infos[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "source":
		sort.Slice(infos, func(i, j int) bool {
			cmp := strings.Compare(infos[i].Source, infos[j].Source)
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	}
}
