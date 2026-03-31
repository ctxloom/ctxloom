package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// ProfileEntry represents a profile in operation results.
type ProfileEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Parents     []string `json:"parents,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Bundles     []string `json:"bundles,omitempty"`
	Default     bool     `json:"default,omitempty"`
	Path        string   `json:"path,omitempty"`
}

// ListProfilesRequest contains parameters for listing profiles.
type ListProfilesRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "default"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *profiles.Loader `json:"-"`
}

// ListProfilesResult contains the list of profiles.
type ListProfilesResult struct {
	Profiles []ProfileEntry `json:"profiles"`
	Count    int            `json:"count"`
	Defaults []string       `json:"defaults"`
}

// ListProfiles returns all profiles matching the criteria.
func ListProfiles(ctx context.Context, cfg *config.Config, req ListProfilesRequest) (*ListProfilesResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = profileLoader(cfg)
	}
	profileList, err := loader.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list profiles: %w", err)
	}

	query := strings.ToLower(req.Query)

	var result []ProfileEntry
	for _, p := range profileList {
		// Filter by query if provided
		if query != "" {
			if !strings.Contains(strings.ToLower(p.Name), query) &&
				!strings.Contains(strings.ToLower(p.Description), query) {
				continue
			}
		}
		result = append(result, ProfileEntry{
			Name:        p.Name,
			Description: p.Description,
			Parents:     p.Parents,
			Tags:        p.Tags,
			Bundles:     p.Bundles,
			Default:     cfg.Defaults.IsDefaultProfile(p.Name),
			Path:        p.Path,
		})
	}

	// Sort results
	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "name"
	}
	reverse := req.SortOrder == "desc"

	switch sortBy {
	case "name":
		sort.Slice(result, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(result[i].Name), strings.ToLower(result[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "default":
		sort.Slice(result, func(i, j int) bool {
			if reverse {
				return !result[i].Default && result[j].Default
			}
			return result[i].Default && !result[j].Default
		})
	}

	return &ListProfilesResult{
		Profiles: result,
		Count:    len(result),
		Defaults: cfg.GetDefaultProfiles(),
	}, nil
}

// GetProfileRequest contains parameters for getting a profile.
type GetProfileRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *profiles.Loader `json:"-"`
}

// GetProfileResult contains the profile details.
type GetProfileResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parents     []string          `json:"parents,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Bundles     []string          `json:"bundles,omitempty"`
	Variables   map[string]string `json:"variables,omitempty"`
	Path        string            `json:"path,omitempty"`
}

// GetProfile returns a specific profile by name.
func GetProfile(ctx context.Context, cfg *config.Config, req GetProfileRequest) (*GetProfileResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = profileLoader(cfg)
	}
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", req.Name)
	}

	return &GetProfileResult{
		Name:        profile.Name,
		Description: profile.Description,
		Parents:     profile.Parents,
		Tags:        profile.Tags,
		Bundles:     profile.Bundles,
		Variables:   profile.Variables,
		Path:        profile.Path,
	}, nil
}

// CreateProfileRequest contains parameters for creating a profile.
type CreateProfileRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Parents     []string `json:"parents"`
	Bundles     []string `json:"bundles"`
	Tags        []string `json:"tags"`
	Default     bool     `json:"default"`

	// Exclusions
	ExcludeFragments []string `json:"exclude_fragments"`
	ExcludePrompts   []string `json:"exclude_prompts"`
	ExcludeMCP       []string `json:"exclude_mcp"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *profiles.Loader `json:"-"`
}

// CreateProfileResult contains the result of creating a profile.
type CreateProfileResult struct {
	Status  string `json:"status"`
	Profile string `json:"profile"`
	Path    string `json:"path"`
}

// CreateProfile creates a new profile.
func CreateProfile(ctx context.Context, cfg *config.Config, req CreateProfileRequest) (*CreateProfileResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = profileLoader(cfg)
	}

	// Check if profile already exists
	if loader.Exists(req.Name) {
		return nil, fmt.Errorf("profile %q already exists", req.Name)
	}

	// Validate parents exist
	for _, parent := range req.Parents {
		if !loader.Exists(parent) {
			return nil, fmt.Errorf("parent profile %q not found", parent)
		}
	}

	profile := &profiles.Profile{
		Name:             req.Name,
		Description:      req.Description,
		Parents:          req.Parents,
		Bundles:          req.Bundles,
		Tags:             req.Tags,
		ExcludeFragments: req.ExcludeFragments,
		ExcludePrompts:   req.ExcludePrompts,
		ExcludeMCP:       req.ExcludeMCP,
	}

	if err := loader.Save(profile); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}

	// Set as default if requested
	if req.Default {
		cfg.Defaults.AddDefaultProfile(req.Name)
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save default setting: %w", err)
		}
	}

	return &CreateProfileResult{
		Status:  "created",
		Profile: req.Name,
		Path:    profile.Path,
	}, nil
}

// UpdateProfileRequest contains parameters for updating a profile.
type UpdateProfileRequest struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	AddParents    []string `json:"add_parents"`
	RemoveParents []string `json:"remove_parents"`
	AddBundles    []string `json:"add_bundles"`
	RemoveBundles []string `json:"remove_bundles"`
	AddTags       []string `json:"add_tags"`
	RemoveTags    []string `json:"remove_tags"`
	Default       *bool    `json:"default"`

	// Exclusion management
	AddExcludeFragments    []string `json:"add_exclude_fragments"`
	RemoveExcludeFragments []string `json:"remove_exclude_fragments"`
	AddExcludePrompts      []string `json:"add_exclude_prompts"`
	RemoveExcludePrompts   []string `json:"remove_exclude_prompts"`
	AddExcludeMCP          []string `json:"add_exclude_mcp"`
	RemoveExcludeMCP       []string `json:"remove_exclude_mcp"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *profiles.Loader `json:"-"`
}

// UpdateProfileResult contains the result of updating a profile.
type UpdateProfileResult struct {
	Status  string   `json:"status"` // "updated" or "no_changes"
	Profile string   `json:"profile"`
	Changes []string `json:"changes,omitempty"`
	Path    string   `json:"path,omitempty"`
}

// UpdateProfile updates an existing profile.
func UpdateProfile(ctx context.Context, cfg *config.Config, req UpdateProfileRequest) (*UpdateProfileResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = profileLoader(cfg)
	}
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found", req.Name)
	}

	changes := []string{}

	// Update description
	if req.Description != nil {
		profile.Description = *req.Description
		changes = append(changes, "updated description")
	}

	// Update default flag
	if req.Default != nil {
		if *req.Default {
			if cfg.Defaults.AddDefaultProfile(req.Name) {
				changes = append(changes, "set as default")
			}
		} else if cfg.Defaults.IsDefaultProfile(req.Name) {
			cfg.Defaults.RemoveDefaultProfile(req.Name)
			changes = append(changes, "unset default")
		}
	}

	// Add parents
	for _, parent := range req.AddParents {
		if !loader.Exists(parent) {
			return nil, fmt.Errorf("parent profile %q not found", parent)
		}
		if !contains(profile.Parents, parent) {
			profile.Parents = append(profile.Parents, parent)
			changes = append(changes, fmt.Sprintf("added parent: %s", parent))
		}
	}

	// Remove parents
	for _, parent := range req.RemoveParents {
		if idx := indexOf(profile.Parents, parent); idx >= 0 {
			profile.Parents = append(profile.Parents[:idx], profile.Parents[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed parent: %s", parent))
		}
	}

	// Add bundles
	for _, b := range req.AddBundles {
		if !contains(profile.Bundles, b) {
			profile.Bundles = append(profile.Bundles, b)
			changes = append(changes, fmt.Sprintf("added bundle: %s", b))
		}
	}

	// Remove bundles
	for _, b := range req.RemoveBundles {
		if idx := indexOf(profile.Bundles, b); idx >= 0 {
			profile.Bundles = append(profile.Bundles[:idx], profile.Bundles[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed bundle: %s", b))
		}
	}

	// Add tags
	for _, t := range req.AddTags {
		if !contains(profile.Tags, t) {
			profile.Tags = append(profile.Tags, t)
			changes = append(changes, fmt.Sprintf("added tag: %s", t))
		}
	}

	// Remove tags
	for _, t := range req.RemoveTags {
		if idx := indexOf(profile.Tags, t); idx >= 0 {
			profile.Tags = append(profile.Tags[:idx], profile.Tags[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed tag: %s", t))
		}
	}

	// Add exclude fragments
	for _, f := range req.AddExcludeFragments {
		if !contains(profile.ExcludeFragments, f) {
			profile.ExcludeFragments = append(profile.ExcludeFragments, f)
			changes = append(changes, fmt.Sprintf("added exclude fragment: %s", f))
		}
	}

	// Remove exclude fragments
	for _, f := range req.RemoveExcludeFragments {
		if idx := indexOf(profile.ExcludeFragments, f); idx >= 0 {
			profile.ExcludeFragments = append(profile.ExcludeFragments[:idx], profile.ExcludeFragments[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed exclude fragment: %s", f))
		}
	}

	// Add exclude prompts
	for _, p := range req.AddExcludePrompts {
		if !contains(profile.ExcludePrompts, p) {
			profile.ExcludePrompts = append(profile.ExcludePrompts, p)
			changes = append(changes, fmt.Sprintf("added exclude prompt: %s", p))
		}
	}

	// Remove exclude prompts
	for _, p := range req.RemoveExcludePrompts {
		if idx := indexOf(profile.ExcludePrompts, p); idx >= 0 {
			profile.ExcludePrompts = append(profile.ExcludePrompts[:idx], profile.ExcludePrompts[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed exclude prompt: %s", p))
		}
	}

	// Add exclude MCP
	for _, m := range req.AddExcludeMCP {
		if !contains(profile.ExcludeMCP, m) {
			profile.ExcludeMCP = append(profile.ExcludeMCP, m)
			changes = append(changes, fmt.Sprintf("added exclude mcp: %s", m))
		}
	}

	// Remove exclude MCP
	for _, m := range req.RemoveExcludeMCP {
		if idx := indexOf(profile.ExcludeMCP, m); idx >= 0 {
			profile.ExcludeMCP = append(profile.ExcludeMCP[:idx], profile.ExcludeMCP[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed exclude mcp: %s", m))
		}
	}

	if len(changes) == 0 {
		return &UpdateProfileResult{
			Status:  "no_changes",
			Profile: req.Name,
			Path:    profile.Path,
		}, nil
	}

	// Save profile changes
	if err := loader.Save(profile); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}

	// Save config changes (for default setting)
	if req.Default != nil {
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &UpdateProfileResult{
		Status:  "updated",
		Profile: req.Name,
		Changes: changes,
		Path:    profile.Path,
	}, nil
}

// DeleteProfileRequest contains parameters for deleting a profile.
type DeleteProfileRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *profiles.Loader `json:"-"`
}

// DeleteProfileResult contains the result of deleting a profile.
type DeleteProfileResult struct {
	Status  string `json:"status"`
	Profile string `json:"profile"`
}

// DeleteProfile deletes a profile.
func DeleteProfile(ctx context.Context, cfg *config.Config, req DeleteProfileRequest) (*DeleteProfileResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		loader = profileLoader(cfg)
	}

	if err := loader.Delete(req.Name); err != nil {
		return nil, fmt.Errorf("failed to delete profile: %w", err)
	}

	// Clear default if deleting the default profile
	if cfg.Defaults.IsDefaultProfile(req.Name) {
		cfg.Defaults.RemoveDefaultProfile(req.Name)
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &DeleteProfileResult{
		Status:  "deleted",
		Profile: req.Name,
	}, nil
}

// profileLoader creates a profile loader using the config.
func profileLoader(cfg *config.Config) *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 && len(cfg.AppPaths) > 0 {
		// Create profiles directory in first ctxloom path
		profileDirs = []string{filepath.Join(cfg.AppPaths[0], "profiles")}
	}
	return profiles.NewLoader(profileDirs)
}

// Helper functions
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}
