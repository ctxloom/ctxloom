package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
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

// bundleLoader creates a bundles.Loader using the config's bundle directories.
func bundleLoader(cfg *config.Config) *bundles.Loader {
	return bundles.NewLoader(cfg.GetBundleDirs(), cfg.Defaults.ShouldUseDistilled())
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

// CreateFragmentRequest contains parameters for creating a fragment.
type CreateFragmentRequest struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Version string   `json:"version"`
	FS      afero.Fs `json:"-"` // Optional filesystem (defaults to OS filesystem if nil)
}

// CreateFragmentResult contains the result of creating a fragment.
type CreateFragmentResult struct {
	Status      string `json:"status"` // "created" or "updated"
	Fragment    string `json:"fragment"`
	Path        string `json:"path"`
	Overwritten bool   `json:"overwritten"`
}

// CreateFragment creates or updates a fragment in a "local" bundle.
// Fragments are stored in bundles; this creates/updates a "local" bundle for user fragments.
func CreateFragment(ctx context.Context, cfg *config.Config, req CreateFragmentRequest) (*CreateFragmentResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Content == "" {
		return nil, fmt.Errorf("content is required")
	}

	if req.Version == "" {
		req.Version = "1.0"
	}

	// Use provided filesystem or default to OS
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	// Use config's SCM path
	baseDir := getBaseDir(cfg)
	bundleDir := filepath.Join(baseDir, config.BundlesDir)
	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}

	// Use a "local" bundle for user-created fragments
	bundlePath := filepath.Join(bundleDir, "local.yaml")

	// Load existing bundle or create new one
	var bundle bundles.Bundle
	if data, err := afero.ReadFile(fs, bundlePath); err == nil {
		if err := yaml.Unmarshal(data, &bundle); err != nil {
			return nil, fmt.Errorf("failed to parse existing local bundle: %w", err)
		}
	}

	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]bundles.BundleFragment)
	}
	if bundle.Version == "" {
		bundle.Version = "1.0"
	}

	// Check if fragment exists
	_, overwritten := bundle.Fragments[req.Name]

	// Add/update the fragment
	bundle.Fragments[req.Name] = bundles.BundleFragment{
		Tags:    req.Tags,
		Content: req.Content,
	}

	// Save the bundle
	yamlContent, err := yaml.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal bundle: %w", err)
	}

	if err := afero.WriteFile(fs, bundlePath, yamlContent, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	status := "created"
	if overwritten {
		status = "updated"
	}

	return &CreateFragmentResult{
		Status:      status,
		Fragment:    req.Name,
		Path:        bundlePath,
		Overwritten: overwritten,
	}, nil
}

// DeleteFragmentRequest contains parameters for deleting a fragment.
type DeleteFragmentRequest struct {
	Name string `json:"name"`
}

// DeleteFragmentResult contains the result of deleting a fragment.
type DeleteFragmentResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// DeleteFragment attempts to delete a fragment.
// Currently returns an error as fragments are part of bundles.
func DeleteFragment(ctx context.Context, cfg *config.Config, req DeleteFragmentRequest) (*DeleteFragmentResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Fragments are now part of bundles and cannot be deleted individually
	return nil, fmt.Errorf("individual fragments cannot be deleted; they are part of bundles. Use bundle management instead")
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
