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
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	if !cfg.Profiles.AddDefaultProfile(profileName) {
		return false
	}
	if err := cfg.Save(); err != nil {
		clidiag.Warn("ctxloom", "set %q as default profile in memory but failed to persist: %v", profileName, err)
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
	if err != nil || rel == "." || isOutsideRel(rel) {
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
//   - Otherwise load the HOME config and, if it has defaults.profiles, record
//     them as cfg's run-only INHERITED defaults (SetInheritedDefaults).
//     GetDefaultProfiles falls back to them, but they are kept out of
//     cfg.Profiles.Defaults so a later cfg.Save() from an unrelated operation
//     never materializes them into the project config.yaml, and
//     ExplicitDefaultProfiles stays empty so first-profile auto-promotion is
//     not suppressed. Nothing is written to disk and no synthetic profile is
//     created.
//   - If home has none either, surface the choice: print a clear stderr
//     instruction telling the user to set defaults.profiles (or run discovery).
//     Per fault-tolerance we still return cleanly so the LLM starts; context
//     assembly simply has no profile to expand (degraded, not crashed).
//
// The lookup runs at most once per cfg: subsequent calls are read-only, so
// concurrent context assembly (operations.MapProfiles) is safe after the main
// goroutine resolves once.
func EnsureDefaultProfiles(cfg *config.Config) {
	if cfg == nil {
		return
	}

	// Project already has an explicit choice — respect it untouched.
	if len(cfg.ExplicitDefaultProfiles()) > 0 {
		return
	}

	// Already resolved this run (possibly to "none") — read-only fast path.
	if cfg.Profiles.InheritedDefaultsResolved() {
		return
	}

	// Inherit from the home config if it defines defaults.profiles.
	if homeProfiles := homeDefaultProfiles(); len(homeProfiles) > 0 {
		cfg.Profiles.SetInheritedDefaults(homeProfiles)
		return
	}
	cfg.Profiles.SetInheritedDefaults(nil) // looked, found none — don't look again

	// Neither project nor home defines defaults.profiles — surface the choice.
	clidiag.Warn("ctxloom", "no default profiles configured (project or home).")
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
	// fragment/skill listings carry their source bundle.
	Bundle string `json:"bundle,omitempty"`
}

// profileDisplayName returns a short, human label for a profile reference: the
// bare profile name for a bundle-shipped profile ("<bundle>#profiles/<name>" →
// "<name>"), the segment after "@profiles/" for a top-level remote ref (e.g.
// "default"), else the name unchanged. Centralizing this in the backend keeps
// frontends from re-deriving display names by parsing refs.
func profileDisplayName(name string) string {
	if _, prof, ok := remote.SplitBundleProfileRef(name); ok {
		return prof
	}
	const marker = "@profiles/"
	if i := strings.LastIndex(name, marker); i >= 0 {
		return name[i+len(marker):]
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
			Default:     cfg.Profiles.IsDefaultProfile(p.Name),
			Path:        p.Path,
			IsRemote:    strings.HasPrefix(p.Path, profiles.SeededProfilePathPrefix),
			Bundle:      profileBundleSource(p.Name),
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
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", req.Name)
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
	Default     bool     `json:"default"`

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

	// Set as default if requested
	if req.Default {
		cfg.Profiles.AddDefaultProfile(req.Name)
		if err := cfg.Save(); err != nil {
			// The profile is already written to disk; reporting the whole
			// creation as failed here would contradict the on-disk state. Warn
			// and return success for the part that took (partial success is
			// success — CLAUDE.md fault tolerance); the user can re-run
			// `ctxloom profile default` to set it.
			clidiag.Warn("ctxloom", "profile %q created but setting it as default failed: %v", req.Name, err)
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
	LLM           *string  `json:"llm"`
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
	profile, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found", req.Name)
	}

	// A seeded remote profile is the shared in-memory copy of a read-only
	// reference: reject before ANY mutation, or the edits would corrupt the
	// seed for every later reader this run and then evaporate (Save refuses
	// the sentinel path anyway).
	if profiles.IsSeededPath(profile.Path) {
		return nil, fmt.Errorf("profile %q is a remote profile and read-only; edit it at its source and run 'ctxloom remote pull'", req.Name)
	}

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

	// Reflect the default flag into cfg (persisted below via cfg.Save).
	changes = append(changes, applyDefaultFlag(cfg, req.Name, req.Default)...)

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

// applyDefaultFlag reflects a requested default-flag state into cfg's
// default-profile set and returns the change lines to record — nil when want is
// nil or the state already matches the request. It mutates cfg.Defaults in
// memory only; the caller persists with cfg.Save once all edits are applied.
func applyDefaultFlag(cfg *config.Config, name string, want *bool) []string {
	if want == nil {
		return nil
	}
	if *want {
		if cfg.Profiles.AddDefaultProfile(name) {
			return []string{"set as default"}
		}
		return nil
	}
	if cfg.Profiles.IsDefaultProfile(name) {
		cfg.Profiles.RemoveDefaultProfile(name)
		return []string{"unset default"}
	}
	return nil
}

// requireProfilesExist returns an error for the first LOCAL name the loader
// cannot resolve, so parent additions are validated before create/update
// mutates or saves anything. Remote refs are skipped: in the reference-only
// model a remote parent only exists locally after `remote pull`, and pull only
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

	// Clear default if deleting the default profile
	if cfg.Profiles.IsDefaultProfile(req.Name) {
		cfg.Profiles.RemoveDefaultProfile(req.Name)
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &DeleteProfileResult{
		Status:  "deleted",
		Profile: req.Name,
	}, nil
}

// SetDefaultProfileRequest adds or removes a default profile.
type SetDefaultProfileRequest struct {
	Name  string `json:"name"`
	Unset bool   `json:"unset"`
	// Exclusive makes Name the SOLE default, unsetting every other default in one
	// atomic step. Mutually exclusive with Unset. This is the "make this THE
	// default" operation, owned here so a client never has to orchestrate an
	// unset-each-then-add sequence (which a crash mid-way could leave inconsistent).
	Exclusive bool `json:"exclusive"`

	// Loader is an optional pre-configured loader (for testing).
	Loader profiles.Store `json:"-"`
}

// SetDefaultProfileResult reports the outcome of a default-profile change and
// the resulting default set.
type SetDefaultProfileResult struct {
	Status   string   `json:"status"` // "added", "removed", "set" (exclusive), "unchanged"
	Name     string   `json:"name"`
	Defaults []string `json:"defaults"`
}

// SetDefaultProfile adds (or, with Unset, removes) a profile from the config
// defaults list. defaults is a LIST, so multiple defaults may coexist and a
// default may be a bare local profile name OR a remote ref — both are valid
// targets for `run`/`weave` when no profile is given. A bare local name is
// validated against the loader; a reference (canonical URL or ctxloom:local) is
// accepted as-is, since its target lives in a remote the loader can't resolve
// synchronously. This is the supported replacement for the default-setting that
// `install --profile` did, minus the parent-graft.
func SetDefaultProfile(ctx context.Context, cfg *config.Config, req SetDefaultProfileRequest) (*SetDefaultProfileResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Unset && req.Exclusive {
		return nil, fmt.Errorf("unset and exclusive are mutually exclusive")
	}

	// Validate existence only for a bare local name we're adding: removals must
	// stay possible even after the underlying profile is gone, and refs point
	// into remotes the loader cannot resolve here.
	if !req.Unset {
		if _, err := remote.ParseReference(req.Name); err != nil {
			loader := req.Loader
			if loader == nil {
				loader = profileLoader(cfg)
			}
			if !loader.Exists(req.Name) {
				return nil, fmt.Errorf("profile %q not found", req.Name)
			}
		}
	}

	status := "unchanged"
	switch {
	case req.Unset:
		if cfg.Profiles.RemoveDefaultProfile(req.Name) {
			status = "removed"
		}
	case req.Exclusive:
		if cfg.Profiles.SetExclusiveDefaultProfile(req.Name) {
			status = "set"
		}
	default:
		if cfg.Profiles.AddDefaultProfile(req.Name) {
			status = "added"
		}
	}

	if status != "unchanged" {
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &SetDefaultProfileResult{
		Status:   status,
		Name:     req.Name,
		Defaults: cfg.ExplicitDefaultProfiles(),
	}, nil
}

// profileLoader creates a profile loader using the config. It wires the remote
// resolvers so read paths (show/list) canonicalize legacy bare/alias bundle refs
// the same way assembly does, and the same lockfile-built remote-profile seed
// as config.GetProfileLoader so locked remote profiles are visible here too —
// without it `profile list`/`profile show <canonical-ref>` would not see what
// assembly resolves.
func profileLoader(cfg *config.Config) *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 && len(cfg.AppPaths) > 0 {
		// Create profiles directory in first ctxloom path
		profileDirs = []string{filepath.Join(cfg.AppPaths[0], "profiles")}
	}
	var opts []profiles.LoaderOption
	if resolve := cfg.ProfileRemoteResolver(); resolve != nil {
		opts = append(opts, profiles.WithRemoteResolver(resolve))
	}
	if resolveURL := cfg.ProfileRemoteURLResolver(); resolveURL != nil {
		opts = append(opts, profiles.WithRemoteURLResolver(resolveURL))
	}
	opts = append(opts, cfg.ProfileSeedOptions()...)
	return profiles.NewLoader(profileDirs, opts...)
}
