package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// cfg.BundleLoader so remote bundles in the lockfile are visible
// without on-disk extraction. Kept as a package-local helper so existing
// call sites don't need to know about the seeding mechanism.
func bundleLoader(cfg *config.Config) *bundles.Loader {
	return cfg.BundleLoader()
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

	// Pipeline is an optional pre-configured process stage (for testing). It
	// carries both policies this surface applies — the trust gate and the form
	// — so an injected one decides exactly what the production one would.
	Pipeline *bundles.Pipeline `json:"-"`
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

	pipe := req.Pipeline
	if pipe == nil {
		// Exposure surface (ctxloom://fragments/{name}, saved-prompt runs): gate
		// the resolved content (trust rework, TR5). A withheld fragment surfaces
		// as errs.ErrFragmentWithheld so the resource omits it.
		pipe = exposurePipeline(cfg)
	}

	content, err := pipe.GetFragment(req.Name)
	if err != nil {
		return nil, err
	}

	return &GetFragmentResult{
		Name:    content.Name,
		Tags:    content.Tags,
		Content: content.Content,
	}, nil
}

// containsTag reports whether any tag contains query, case-insensitively.
// Both operands are folded HERE. Folding only the tag left an
// undocumented "caller must lowercase query" precondition — every current
// caller happens to honour it, so the next one would inherit a silent
// false-negative with nothing to warn them.
func containsTag(tags []string, query string) bool {
	q := strings.ToLower(query)
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

// sortContentInfos sorts content infos by the specified field and order.
// An unrecognised sortBy used to fall out of the switch and leave the
// slice in loader order — a silent no-op for a caller that explicitly asked for
// an ordering. It now warns and falls back to name, matching ListProfiles'
// existing taxonomy for the same mistake (profiles.go).
func sortContentInfos(infos []bundles.ContentInfo, sortBy, sortOrder string) {
	reverse := sortOrder == "desc"

	byName := func() {
		sort.Slice(infos, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(infos[i].Name), strings.ToLower(infos[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	switch sortBy {
	case "", "name":
		byName()
	case "source":
		sort.Slice(infos, func(i, j int) bool {
			cmp := strings.Compare(infos[i].Source, infos[j].Source)
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	default:
		clidiag.Warn("ctxloom", "unknown sort_by %q; sorting by name (accepted: name, source)", sortBy)
		byName()
	}
}
