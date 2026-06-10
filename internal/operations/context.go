package operations

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/cbroglie/mustache"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/shared/collections"
)

// Mustache tag types from cbroglie/mustache.
const (
	tagVariable        = 1
	tagRawVariable     = 2
	tagSection         = 3
	tagInvertedSection = 4
)

// ProfileLoader interface for resolving profiles from directory (allows mocking in tests).
type ProfileLoader interface {
	ResolveProfile(name string, visited map[string]bool) (*profiles.ResolvedProfile, error)
}

// AssembleContextRequest contains parameters for assembling context.
type AssembleContextRequest struct {
	Profile   string   `json:"profile"`
	Fragments []string `json:"fragments"`
	Tags      []string `json:"tags"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`

	// ProfileLoaderFunc is an optional function to get the profile loader (for testing).
	ProfileLoaderFunc func() ProfileLoader `json:"-"`
}

// AssembleContextResult contains the assembled context.
type AssembleContextResult struct {
	Profiles        []string `json:"profiles"`
	FragmentsLoaded []string `json:"fragments_loaded"`
	Context         string   `json:"context"`

	// ProfileLLM is the LLM the resolved profile(s) declared (first non-empty
	// across the resolved set). Empty means no profile preference; callers fall
	// back to the configured primary role. Overridable by -l/--llm at the call
	// site.
	ProfileLLM string `json:"profile_llm,omitempty"`
}

// AssembleContext assembles context from a profile, fragments, and/or tags.
// Fragments are sorted using bookend strategy based on priority:
// highest priority at start, second-highest at end, rest in middle.
func AssembleContext(ctx context.Context, cfg *config.Config, req AssembleContextRequest) (*AssembleContextResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	profileNames := resolveContextProfileNames(cfg, req)

	// Profiles picked up from configured defaults (rather than an explicit
	// --profile ask) degrade per fault-tolerance: a default that fails to
	// resolve is warned about and skipped, never blocking startup.
	fromDefaults := req.Profile == ""

	allFragments, profileVars, profileLLM, err := collectProfileFragments(cfg, loader, profileNames, req.ProfileLoaderFunc, fromDefaults)
	if err != nil {
		return nil, err
	}

	// Add request fragments (priority 0) and request-tag fragments.
	for _, f := range req.Fragments {
		allFragments = append(allFragments, config.FragmentRef{Name: f, Priority: 0})
	}
	reqTagFragments, err := fragmentsFromTags(loader, req.Tags)
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
	}
	allFragments = append(allFragments, reqTagFragments...)

	// Deduplicate (highest priority wins), then bookend-sort for the
	// "lost in the middle" optimization.
	orderedNames := sortFragmentsByPriority(dedupeFragmentRefs(allFragments))

	contextContent, loadedNames, err := loadAssembledContext(loader, orderedNames, profileVars)
	if err != nil {
		return nil, err
	}

	return &AssembleContextResult{
		Profiles:        profileNames,
		FragmentsLoaded: loadedNames,
		Context:         contextContent,
		ProfileLLM:      profileLLM,
	}, nil
}

// resolveContextProfileNames picks the profiles to assemble from: the explicit
// request profile, else (when nothing at all is selected) the configured
// defaults. When the project config has none, defaults inherit the home
// config's defaults.profiles; if neither defines any, assembly degrades to
// empty context. No synthetic profile is ever created.
func resolveContextProfileNames(cfg *config.Config, req AssembleContextRequest) []string {
	if req.Profile != "" {
		return []string{req.Profile}
	}
	if len(req.Fragments) == 0 && len(req.Tags) == 0 {
		EnsureDefaultProfiles(cfg)
		return cfg.GetDefaultProfiles()
	}
	return nil
}

// fragmentsFromTags resolves tag-matched fragments to priority-0 refs. An empty
// tag list yields no refs.
func fragmentsFromTags(loader *bundles.Loader, tags []string) ([]config.FragmentRef, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	taggedInfos, err := loader.ListByTags(tags)
	if err != nil {
		return nil, err
	}
	refs := make([]config.FragmentRef, 0, len(taggedInfos))
	for _, info := range taggedInfos {
		refs = append(refs, config.FragmentRef{Name: info.Name, Priority: 0})
	}
	return refs, nil
}

// collectProfileFragments resolves each profile (with inheritance) and gathers
// its tag-matched + explicit fragments and variables. It also reports the
// effective declared LLM: the first non-empty profile.LLM across the resolved
// set, warning to stderr if a later profile disagrees.
func collectProfileFragments(cfg *config.Config, loader *bundles.Loader, profileNames []string, profileLoaderFunc func() ProfileLoader, fromDefaults bool) ([]config.FragmentRef, map[string]string, string, error) {
	var allFragments []config.FragmentRef
	profileVars := make(map[string]string)
	effectiveLLM := ""

	for _, pName := range profileNames {
		profile, err := resolveProfile(cfg, pName, loader, profileLoaderFunc)
		if err != nil {
			// An explicitly requested profile failing is the user's signal to
			// fix the ask. A configured default failing must not block startup
			// (CLAUDE.md fault tolerance): warn, skip, assemble what's left.
			if fromDefaults {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: skipping default profile %s: %v\n", pName, err)
				continue
			}
			return nil, nil, "", fmt.Errorf("failed to resolve profile %s: %w", pName, err)
		}

		if profile.LLM != "" {
			if effectiveLLM == "" {
				effectiveLLM = profile.LLM
			} else if profile.LLM != effectiveLLM {
				fmt.Fprintf(os.Stderr,
					"ctxloom: warning: profile %q declares llm %q but %q is already in effect; keeping %q\n",
					pName, profile.LLM, effectiveLLM, effectiveLLM)
			}
		}

		for k, v := range profile.Variables {
			profileVars[k] = v
		}

		tagFragments, err := fragmentsFromTags(loader, profile.Tags)
		if err != nil {
			return nil, nil, "", fmt.Errorf("failed to list fragments by profile tags: %w", err)
		}
		// Exclusions always win (see profiles.md): a tag-matched fragment the
		// profile excludes is dropped, matching the bundle-expansion filter in
		// resolveProfile. Request-level tags/fragments are never filtered —
		// they are explicit user asks, not profile-pushed content.
		excluded := config.NewExclusionSet(profile.ExcludeFragments)
		for _, ref := range tagFragments {
			if config.IsExcludedFragment(ref.Name, excluded) {
				continue
			}
			allFragments = append(allFragments, ref)
		}
		allFragments = append(allFragments, profile.Fragments...)
	}

	return allFragments, profileVars, effectiveLLM, nil
}

// loadAssembledContext loads the ordered fragments and applies variable
// substitution. Returns empty content when there are no fragments.
func loadAssembledContext(loader *bundles.Loader, orderedNames []string, profileVars map[string]string) (string, []string, error) {
	if len(orderedNames) == 0 {
		return "", nil, nil
	}
	content, loadedNames, err := loader.LoadMultiple(orderedNames)
	if err != nil {
		return "", nil, fmt.Errorf("failed to load fragments: %w", err)
	}
	// Suppress substitution warnings in the operations context.
	content = substituteVariables(content, profileVars, func(string) {})
	return content, loadedNames, nil
}

// dedupeFragmentRefs removes duplicates, keeping the highest priority for each fragment.
func dedupeFragmentRefs(fragments []config.FragmentRef) []config.FragmentRef {
	priorities := make(map[string]int)
	order := make(map[string]int) // Track first occurrence order

	for i, f := range fragments {
		if existing, ok := priorities[f.Name]; ok {
			if f.Priority > existing {
				priorities[f.Name] = f.Priority
			}
		} else {
			priorities[f.Name] = f.Priority
			order[f.Name] = i
		}
	}

	// Build result maintaining original order for same priority
	result := make([]config.FragmentRef, 0, len(priorities))
	for name, priority := range priorities {
		result = append(result, config.FragmentRef{Name: name, Priority: priority})
	}

	// Sort by original order (for stable output when priorities are equal)
	slices.SortFunc(result, func(a, b config.FragmentRef) int {
		return order[a.Name] - order[b.Name]
	})

	return result
}

// sortFragmentsByPriority arranges fragments using bookend strategy:
// Highest priority at start, second-highest at end, rest fill middle (descending).
// This addresses the "lost in the middle" problem where Configs poorly attend to middle content.
func sortFragmentsByPriority(fragments []config.FragmentRef) []string {
	if len(fragments) == 0 {
		return nil
	}

	// Sort by priority descending
	sorted := slices.Clone(fragments)
	slices.SortStableFunc(sorted, func(a, b config.FragmentRef) int {
		return b.Priority - a.Priority // Descending
	})

	// For 1-2 fragments, just return in priority order
	if len(sorted) <= 2 {
		names := make([]string, len(sorted))
		for i, f := range sorted {
			names[i] = f.Name
		}
		return names
	}

	// Bookend placement: [highest, middle..., second-highest]
	result := make([]string, len(sorted))
	result[0] = sorted[0].Name             // Highest priority at start
	result[len(result)-1] = sorted[1].Name // Second-highest at end

	// Fill middle with remaining (already sorted descending)
	for i := 2; i < len(sorted); i++ {
		result[i-1] = sorted[i].Name
	}

	return result
}

// resolveProfile resolves a profile from config or directory and expands its
// bundle references into fragment references.
//
// Profile.Bundles entries (whole bundles like "remote/bundle") and
// Profile.BundleItems entries (cherry-picked items like
// "remote/bundle:fragments/name") are expanded via the bundle loader and
// appended to Fragments so a single downstream pipeline (GetFragment per
// FragmentRef.Name) can load everything regardless of how the profile
// referenced it.
//
// loader may be nil, in which case bundle expansion is skipped — callers
// that don't have a bundle loader handy still get the inline Tags/Fragments
// behavior, which is enough for the lightweight callers that only inspect
// metadata.
func resolveProfile(cfg *config.Config, name string, loader *bundles.Loader, profileLoaderFunc func() ProfileLoader) (*config.Profile, error) {
	var profile *config.Profile

	// First try config-based resolution (inline `profiles:` map in config.yaml).
	if p, err := config.ResolveProfile(cfg.Profiles.Definitions, name); err == nil {
		profile = p
	} else {
		// Fall back to directory-based resolution (.ctxloom/profiles/<name>.yaml).
		var pLoader ProfileLoader
		if profileLoaderFunc != nil {
			pLoader = profileLoaderFunc()
		} else {
			pLoader = cfg.GetProfileLoader()
		}
		resolved, rerr := pLoader.ResolveProfile(name, nil)
		if rerr != nil {
			return nil, fmt.Errorf("profile %s: %w", name, rerr)
		}
		profile = &config.Profile{
			Tags:             resolved.Tags,
			Bundles:          resolved.Bundles,
			Variables:        resolved.Variables,
			LLM:              resolved.LLM,
			ExcludeFragments: resolved.ExcludeFragments,
			ExcludeMCP:       resolved.ExcludeMCP,
		}
	}

	// Expand Bundles and BundleItems into FragmentRefs via the bundle loader.
	// Without this, profiles that only list bundles (the common case for
	// directory profiles) would resolve to zero fragments.
	//
	// The profile's exclude_fragments filter applies here: the inline-profile
	// resolver only filters fragments declared inline (profileBuilder.toProfile),
	// so bundle-expanded fragments — the only kind a directory profile has —
	// must be filtered at this expansion seam or exclusions silently no-op.
	if loader != nil {
		excluded := config.NewExclusionSet(profile.ExcludeFragments)
		refs := make([]string, 0, len(profile.Bundles)+len(profile.BundleItems))
		refs = append(refs, profile.Bundles...)
		refs = append(refs, profile.BundleItems...)
		for _, expandedName := range loader.ExpandBundleRefs(refs) {
			if config.IsExcludedFragment(expandedName, excluded) {
				continue
			}
			profile.Fragments = append(profile.Fragments, config.FragmentRef{Name: expandedName, Priority: 0})
		}
	}

	return profile, nil
}

// substituteVariables applies mustache variable substitution to content.
func substituteVariables(content string, vars map[string]string, warnFunc func(string)) string {
	// Parse the template using the mustache library (handles delimiter changes correctly)
	tmpl, err := mustache.ParseString(content)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to parse template: %v", err))
		return content
	}

	// Check for undefined variables by walking the parsed tags
	seen := collections.NewSet[string]()
	checkTags(tmpl.Tags(), vars, seen, warnFunc)

	data := make(map[string]interface{})
	for k, v := range vars {
		data[k] = v
	}

	rendered, err := tmpl.Render(data)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to render template: %v", err))
		return content
	}

	return rendered
}

// referencesVariable reports whether a tag type references a variable name
// (plain/raw variables and section tags, which key off a variable).
func referencesVariable(t mustache.TagType) bool {
	return t == tagVariable || t == tagRawVariable || t == tagSection || t == tagInvertedSection
}

// hasChildTags reports whether a tag type can contain nested tags (sections).
func hasChildTags(t mustache.TagType) bool {
	return t == tagSection || t == tagInvertedSection
}

// checkTags recursively walks mustache tags to find undefined variables.
func checkTags(tags []mustache.Tag, vars map[string]string, seen collections.Set[string], warnFunc func(string)) {
	for _, tag := range tags {
		name := tag.Name()
		tagType := tag.Type()

		if referencesVariable(tagType) && !seen.Has(name) {
			seen.Add(name)
			if _, exists := vars[name]; !exists {
				warnFunc(fmt.Sprintf("undefined variable: {{%s}}", name))
			}
		}

		// Recursively check nested tags (only sections have children).
		if hasChildTags(tagType) {
			if children := tag.Tags(); len(children) > 0 {
				checkTags(children, vars, seen, warnFunc)
			}
		}
	}
}
