package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ProfileEntry represents a profile in operation results.
type ProfileEntry struct {
	Name string `json:"name"`
	// DisplayName is a short, human label for the profile: the segment after
	// "@profiles/" for a remote reference (e.g. "default"), otherwise Name. It
	// lets frontends show a friendly name without parsing refs themselves.
	DisplayName string   `json:"display_name"`
	Description string   `json:"description,omitempty"`
	Parents     []string `json:"parents,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Bundles     []string `json:"bundles,omitempty"`
	Default     bool     `json:"default,omitempty"`
	Path        string   `json:"path,omitempty"`
	// IsRemote reports whether this profile is a seeded reference (its Path
	// carries the "<remote>:" sentinel) rather than a directly-editable local
	// file. Both top-level remote profiles and bundle-shipped profiles are
	// seeded, so both set this; Bundle disambiguates a bundle profile.
	IsRemote bool `json:"is_remote"`
	// Bundle is the canonical bundle ref a bundle-shipped profile came from
	// ("<bundle>" of a "<bundle>#profiles/<name>" identity), empty for top-level
	// or local profiles. It attributes the profile to its owning bundle, the way
	// fragment/command listings carry their source bundle.
	Bundle string `json:"bundle,omitempty"`
}

// profileDisplayName returns a short, human label for a profile reference: the
// bare profile name for a bundle-shipped profile ("<bundle>#profiles/<name>" →
// "<name>"), else the name unchanged. Centralizing this in the backend keeps
// frontends from re-deriving display names by parsing refs. (Top-level
// "@profiles/" distribution was retired.)
func profileDisplayName(name string) string {
	if _, prof, ok := remote.SplitBundleProfileRef(name); ok {
		return prof
	}
	return name
}

// profileBundleSource returns the canonical bundle ref a bundle-shipped profile
// came from ("<bundle>" of a "<bundle>#profiles/<name>" identity), or "" for a
// top-level or local profile.
func profileBundleSource(name string) string {
	bundle, _, ok := remote.SplitBundleProfileRef(name)
	if !ok {
		return ""
	}
	return bundle
}

// ListProfilesRequest contains parameters for listing profiles.
type ListProfilesRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "default"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
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

	// A profile is "default" when it is one of the profiles the default AGENT
	// composes (profiles.defaults was retired — the default set is now whatever
	// Config.DefaultAgent binds).
	defaultSet := cfg.DefaultAgentProfiles()

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
			DisplayName: profileDisplayName(p.Name),
			Description: p.Description,
			Parents:     p.Parents,
			Tags:        p.Tags,
			Bundles:     p.Bundles,
			Default:     slices.Contains(defaultSet, p.Name),
			Path:        p.Path,
			IsRemote:    strings.HasPrefix(p.Path, profiles.SeededProfilePathPrefix),
			Bundle:      profileBundleSource(p.Name),
		})
	}

	// Sort results. An empty sort_by defaults to name; an UNRECOGNISED one also
	// sorts by name, loudly — leaving the slice untouched would ship it in the
	// loader's build order, and Loader.List emits seeded (bundle-shipped)
	// profiles by ranging over a MAP, so the order would be genuinely unstable.
	reverse := req.SortOrder == "desc"
	byName := func() {
		sort.Slice(result, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(result[i].Name), strings.ToLower(result[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	switch req.SortBy {
	case "", "name":
		byName()
	case "default":
		sort.Slice(result, func(i, j int) bool {
			if reverse {
				return !result[i].Default && result[j].Default
			}
			return result[i].Default && !result[j].Default
		})
	default:
		clidiag.Warn("ctxloom", "unknown sort_by %q for profiles; sorting by name (accepted: name, default)", req.SortBy)
		byName()
	}

	return &ListProfilesResult{
		Profiles: result,
		Count:    len(result),
		Defaults: cfg.DefaultAgentProfiles(),
	}, nil
}

// GetProfileRequest contains parameters for getting a profile.
type GetProfileRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
}

// GetProfileResult contains the profile details.
type GetProfileResult struct {
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	LLM              string            `json:"llm,omitempty"`
	Parents          []string          `json:"parents,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Bundles          []string          `json:"bundles,omitempty"`
	Variables        map[string]string `json:"variables,omitempty"`
	ExcludeFragments []string          `json:"exclude_fragments,omitempty"`
	ExcludeMCP       []string          `json:"exclude_mcp,omitempty"`
	Path             string            `json:"path,omitempty"`
	// Bundle is the canonical bundle ref a bundle-shipped profile came from,
	// empty for top-level or local profiles (see ProfileEntry.Bundle).
	Bundle string `json:"bundle,omitempty"`
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
	// Return the loader's error verbatim: it carries the ErrProfileNotFound
	// sentinel plus the actionable detail (the remote-pull hint for a
	// seed-missing bundle profile, the reserved-'#' explanation) that a flat
	// re-wrap used to discard.
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, err
	}

	return &GetProfileResult{
		Name:             profile.Name,
		Description:      profile.Description,
		LLM:              profile.LLM,
		Parents:          profile.Parents,
		Tags:             profile.Tags,
		Bundles:          profile.Bundles,
		Variables:        profile.Variables,
		ExcludeFragments: profile.ExcludeFragments,
		ExcludeMCP:       profile.ExcludeMCP,
		Path:             profile.Path,
		Bundle:           profileBundleSource(profile.Name),
	}, nil
}

// CreateProfileRequest contains parameters for creating a profile.
type CreateProfileRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	LLM         string   `json:"llm"`
	Parents     []string `json:"parents"`
	Bundles     []string `json:"bundles"`
	Tags        []string `json:"tags"`

	// Exclusions
	ExcludeFragments []string `json:"exclude_fragments"`
	ExcludeMCP       []string `json:"exclude_mcp"`

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
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

	// Canonicalize-on-store (decision B): a per-remote short bundle/parent ref
	// ("<remote>/<bundle>[#profiles/<name>]") is expanded to its canonical URL so
	// the stored profile never carries a machine-local alias. Bare names stay
	// local (decision A). Done before the parent-existence check so a resolved
	// remote parent is recognized as remote (isRemoteReference) rather than looked
	// up on the local fs.
	aliasToURL := aliasToURLResolver(cfg)
	req.Bundles = canonicalizeBundleRefs(req.Bundles, aliasToURL)
	req.Parents = canonicalizeProfileRefs(req.Parents, aliasToURL)

	// Validate that local parents exist.
	if err := requireProfilesExist(loader, req.Parents); err != nil {
		return nil, err
	}

	profile := &profiles.Profile{
		Name:             req.Name,
		Description:      req.Description,
		LLM:              req.LLM,
		Parents:          req.Parents,
		Bundles:          req.Bundles,
		Tags:             req.Tags,
		ExcludeFragments: req.ExcludeFragments,
		ExcludeMCP:       req.ExcludeMCP,
	}

	if err := loader.Save(profile); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}

	// A newly-created profile is no longer auto-promoted to a config default
	// (profiles.defaults was retired): compose it into an agent to make it the
	// default context (`ctxloom agent set` / `ctxloom agent default`).
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
	LLM           *string  `json:"llm"`
	AddParents    []string `json:"add_parents"`
	RemoveParents []string `json:"remove_parents"`
	AddBundles    []string `json:"add_bundles"`
	RemoveBundles []string `json:"remove_bundles"`
	AddTags       []string `json:"add_tags"`
	RemoveTags    []string `json:"remove_tags"`

	// Exclusion management
	AddExcludeFragments    []string `json:"add_exclude_fragments"`
	RemoveExcludeFragments []string `json:"remove_exclude_fragments"`
	AddExcludeMCP          []string `json:"add_exclude_mcp"`
	RemoveExcludeMCP       []string `json:"remove_exclude_mcp"`

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
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
	// Return the loader's error verbatim, exactly as GetProfile does: it carries
	// the errs.ErrProfileNotFound sentinel callers branch on plus the actionable
	// detail (the remote-pull hint for a seed-missing bundle profile, the
	// reserved-'#' explanation) that a flat re-wrap discards.
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, err
	}

	// A seeded remote profile is the shared in-memory copy of a read-only
	// reference: reject before ANY mutation, or the edits would corrupt the
	// seed for every later reader this run and then evaporate (Save refuses
	// the sentinel path anyway).
	if profiles.IsSeededPath(profile.Path) {
		return nil, fmt.Errorf("profile %q is a remote profile and read-only; edit it at its source and run 'ctxloom deps pull'", req.Name)
	}

	// Canonicalize-on-store (decision B): short "<remote>/<bundle>[#profiles/...]"
	// refs expand to canonical URLs so the stored profile carries no machine-local
	// alias. Removals are canonicalized the SAME way so a "remove <remote>/<bundle>"
	// matches the canonical form already on disk. Bare names stay local (decision A).
	aliasToURL := aliasToURLResolver(cfg)
	req.AddBundles = canonicalizeBundleRefs(req.AddBundles, aliasToURL)
	req.RemoveBundles = canonicalizeBundleRefs(req.RemoveBundles, aliasToURL)
	req.AddParents = canonicalizeProfileRefs(req.AddParents, aliasToURL)
	req.RemoveParents = canonicalizeProfileRefs(req.RemoveParents, aliasToURL)

	// Validate new parents up front so a bad parent halts before any mutation —
	// including the cfg default-flag change, which an unrelated cfg.Save()
	// would otherwise persist despite this update failing.
	if err := requireProfilesExist(loader, req.AddParents); err != nil {
		return nil, err
	}

	changes := []string{}

	// Update description.
	if req.Description != nil {
		profile.Description = *req.Description
		changes = append(changes, "updated description")
	}

	// Update the preferred LLM (empty clears it).
	if req.LLM != nil {
		profile.LLM = *req.LLM
		if profile.LLM == "" {
			changes = append(changes, "cleared llm")
		} else {
			changes = append(changes, fmt.Sprintf("set llm to %q", profile.LLM))
		}
	}

	// Every list field shares one add/remove primitive (see applyListEdits).
	profile.Parents, changes = applyListEdits(profile.Parents, req.AddParents, req.RemoveParents, "parent", changes)
	profile.Bundles, changes = applyListEdits(profile.Bundles, req.AddBundles, req.RemoveBundles, "bundle", changes)
	profile.Tags, changes = applyListEdits(profile.Tags, req.AddTags, req.RemoveTags, "tag", changes)
	profile.ExcludeFragments, changes = applyListEdits(profile.ExcludeFragments, req.AddExcludeFragments, req.RemoveExcludeFragments, "exclude fragment", changes)
	profile.ExcludeMCP, changes = applyListEdits(profile.ExcludeMCP, req.AddExcludeMCP, req.RemoveExcludeMCP, "exclude mcp", changes)

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

	return &UpdateProfileResult{
		Status:  "updated",
		Profile: req.Name,
		Changes: changes,
		Path:    profile.Path,
	}, nil
}

// applyListEdits applies add then remove edits to a profile's string-slice
// field. Adds are deduplicated (an item already present is skipped); removes
// drop the first matching item. Each real mutation appends a human-readable
// line ("added <label>: <item>" / "removed <label>: <item>") to changes.
// Callers must reassign both returned slices.
func applyListEdits(list, add, remove []string, label string, changes []string) (newList, newChanges []string) {
	for _, item := range add {
		if !slices.Contains(list, item) {
			list = append(list, item)
			changes = append(changes, fmt.Sprintf("added %s: %s", label, item))
		}
	}
	for _, item := range remove {
		if idx := slices.Index(list, item); idx >= 0 {
			list = slices.Delete(list, idx, idx+1)
			changes = append(changes, fmt.Sprintf("removed %s: %s", label, item))
		}
	}
	return list, changes
}

// requireProfilesExist returns an error for the first LOCAL name the loader
// cannot resolve, so parent additions are validated before create/update
// mutates or saves anything. Remote refs are skipped: in the reference-only
// model a remote parent only exists locally after `deps pull`, and pull only
// fetches what a profile references — validating remote refs here would
// deadlock the bootstrap order. They are validated at pull/lock time.
func requireProfilesExist(loader profiles.Source, names []string) error {
	for _, name := range names {
		if isRemoteReference(name) {
			continue
		}
		if !loader.Exists(name) {
			return fmt.Errorf("parent profile %q not found", name)
		}
	}
	return nil
}

// DeleteProfileRequest contains parameters for deleting a profile.
type DeleteProfileRequest struct {
	Name string `json:"name"`

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
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

	return &DeleteProfileResult{
		Status:  "deleted",
		Profile: req.Name,
	}, nil
}

// profileLoader creates a profile loader using the config. It is
// config.GetProfileLoader with ONE difference, and only one: on a fresh install
// no profiles directory exists yet, so GetProfileDirs finds none and this
// factory synthesizes <appPath>/profiles to give a Save somewhere to land. The
// option set comes from cfg.ProfileLoaderOptions so the two factories cannot
// disagree about which filesystem is read, how a bundle ref canonicalizes, or
// which remote/bundle profiles exist.
func profileLoader(cfg *config.Config) *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(cfg.FS(), cfg.GetAppPaths())
	if len(profileDirs) == 0 && len(cfg.GetAppPaths()) > 0 {
		profileDirs = []string{filepath.Join(cfg.GetAppPaths()[0], "profiles")}
	}
	return profiles.NewLoader(profileDirs, cfg.ProfileLoaderOptions()...)
}
