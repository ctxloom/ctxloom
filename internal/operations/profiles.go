package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// PromoteToDefaultIfFirst adds profileName to defaults.profiles in config.yaml
// if no explicit default is currently configured. Uses
// ExplicitDefaultProfiles rather than GetDefaultProfiles so the single-profile
// fallback does not suppress the promotion — auto-promote's job is to make
// the implicit choice explicit on disk.
// Save errors are warned but non-fatal — the caller's primary operation has
// already succeeded by the time this is called.
// Returns true if the profile was added and the config saved.
func PromoteToDefaultIfFirst(cfg *config.Config, profileName string) bool {
	if cfg == nil || profileName == "" {
		return false
	}
	if len(cfg.ExplicitDefaultProfiles()) > 0 {
		return false
	}
	if !cfg.Defaults.AddDefaultProfile(profileName) {
		return false
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: set %q as default profile in memory but failed to persist: %v\n", profileName, err)
		return false
	}
	return true
}

// LocalProfileNameFromPath returns the profile name (as used by the profile
// loader) for a profile file path under cfg's base ctxloom directory.
// Returns ("", false) if the path is not within the profiles directory.
func LocalProfileNameFromPath(cfg *config.Config, localPath string) (string, bool) {
	profilesDir := paths.ProfilesPath(getBaseDir(cfg))
	rel, err := filepath.Rel(profilesDir, localPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, ".yaml")
	rel = strings.TrimSuffix(rel, ".yml")
	if rel == "" {
		return "", false
	}
	return rel, true
}

// EnsureDefaultProfiles makes sure cfg has a usable set of default profiles
// before context assembly, WITHOUT ever silently inventing one.
//
// Config resolution is project-XOR-home: when a project .ctxloom exists it is
// used wholesale and the home (~/.ctxloom) config is NOT merged. That means a
// project with no defaults.profiles would otherwise shadow the user's home
// defaults entirely. To honor the user's intent ("inherit home, else prompt")
// without mutating the project config on disk:
//
//   - If the project config already lists explicit defaults.profiles, do
//     nothing — the user has made their choice.
//   - Otherwise load the HOME config and, if it has defaults.profiles, copy
//     them into the in-memory cfg.Defaults.Profiles for this run only. Nothing
//     is written to disk and no synthetic profile is created.
//   - If home has none either, surface the choice: print a clear stderr
//     instruction telling the user to set defaults.profiles (or run discovery).
//     Per fault-tolerance we still return cleanly so the LLM starts; context
//     assembly simply has no profile to expand (degraded, not crashed).
func EnsureDefaultProfiles(cfg *config.Config) {
	if cfg == nil {
		return
	}

	// Project already has an explicit choice — respect it untouched.
	if len(cfg.ExplicitDefaultProfiles()) > 0 {
		return
	}

	// Inherit from the home config if it defines defaults.profiles.
	if homeProfiles := homeDefaultProfiles(); len(homeProfiles) > 0 {
		cfg.Defaults.Profiles = append([]string(nil), homeProfiles...)
		return
	}

	// Neither project nor home defines defaults.profiles — surface the choice.
	fmt.Fprintln(os.Stderr, "ctxloom: warning: no default profiles configured (project or home).")
	fmt.Fprintln(os.Stderr, "ctxloom: set defaults.profiles in .ctxloom/config.yaml (or ~/.ctxloom/config.yaml),")
	fmt.Fprintln(os.Stderr, "ctxloom: or run discovery to install one. Continuing without a profile.")
}

// homeDefaultProfiles loads the user's home config (~/.ctxloom/config.yaml)
// and returns its explicit defaults.profiles, or nil if the home config is
// absent, unreadable, or defines none. Errors are swallowed (fault-tolerance):
// a missing/broken home config simply means "nothing to inherit".
func homeDefaultProfiles() []string {
	homeDir, err := config.HomeConfigDir()
	if err != nil {
		return nil
	}
	homeCfg, err := config.Load(config.WithAppDir(homeDir))
	if err != nil {
		return nil
	}
	return homeCfg.ExplicitDefaultProfiles()
}

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
	} else {
		// Auto-promote: if there is no default profile yet, this becomes it so
		// that `ctxloom run` doesn't launch with empty context.
		PromoteToDefaultIfFirst(cfg, req.Name)
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
		if !slices.Contains(profile.Parents, parent) {
			profile.Parents = append(profile.Parents, parent)
			changes = append(changes, fmt.Sprintf("added parent: %s", parent))
		}
	}

	// Remove parents
	for _, parent := range req.RemoveParents {
		if idx := slices.Index(profile.Parents, parent); idx >= 0 {
			profile.Parents = append(profile.Parents[:idx], profile.Parents[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed parent: %s", parent))
		}
	}

	// Add bundles
	for _, b := range req.AddBundles {
		if !slices.Contains(profile.Bundles, b) {
			profile.Bundles = append(profile.Bundles, b)
			changes = append(changes, fmt.Sprintf("added bundle: %s", b))
		}
	}

	// Remove bundles
	for _, b := range req.RemoveBundles {
		if idx := slices.Index(profile.Bundles, b); idx >= 0 {
			profile.Bundles = append(profile.Bundles[:idx], profile.Bundles[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed bundle: %s", b))
		}
	}

	// Add tags
	for _, t := range req.AddTags {
		if !slices.Contains(profile.Tags, t) {
			profile.Tags = append(profile.Tags, t)
			changes = append(changes, fmt.Sprintf("added tag: %s", t))
		}
	}

	// Remove tags
	for _, t := range req.RemoveTags {
		if idx := slices.Index(profile.Tags, t); idx >= 0 {
			profile.Tags = append(profile.Tags[:idx], profile.Tags[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed tag: %s", t))
		}
	}

	// Add exclude fragments
	for _, f := range req.AddExcludeFragments {
		if !slices.Contains(profile.ExcludeFragments, f) {
			profile.ExcludeFragments = append(profile.ExcludeFragments, f)
			changes = append(changes, fmt.Sprintf("added exclude fragment: %s", f))
		}
	}

	// Remove exclude fragments
	for _, f := range req.RemoveExcludeFragments {
		if idx := slices.Index(profile.ExcludeFragments, f); idx >= 0 {
			profile.ExcludeFragments = append(profile.ExcludeFragments[:idx], profile.ExcludeFragments[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed exclude fragment: %s", f))
		}
	}

	// Add exclude prompts
	for _, p := range req.AddExcludePrompts {
		if !slices.Contains(profile.ExcludePrompts, p) {
			profile.ExcludePrompts = append(profile.ExcludePrompts, p)
			changes = append(changes, fmt.Sprintf("added exclude prompt: %s", p))
		}
	}

	// Remove exclude prompts
	for _, p := range req.RemoveExcludePrompts {
		if idx := slices.Index(profile.ExcludePrompts, p); idx >= 0 {
			profile.ExcludePrompts = append(profile.ExcludePrompts[:idx], profile.ExcludePrompts[idx+1:]...)
			changes = append(changes, fmt.Sprintf("removed exclude prompt: %s", p))
		}
	}

	// Add exclude MCP
	for _, m := range req.AddExcludeMCP {
		if !slices.Contains(profile.ExcludeMCP, m) {
			profile.ExcludeMCP = append(profile.ExcludeMCP, m)
			changes = append(changes, fmt.Sprintf("added exclude mcp: %s", m))
		}
	}

	// Remove exclude MCP
	for _, m := range req.RemoveExcludeMCP {
		if idx := slices.Index(profile.ExcludeMCP, m); idx >= 0 {
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
