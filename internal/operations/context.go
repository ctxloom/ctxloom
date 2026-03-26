package operations

import (
	"context"
	"fmt"
	"slices"

	"github.com/cbroglie/mustache"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/profiles"
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
}

// AssembleContext assembles context from a profile, fragments, and/or tags.
// Fragments are sorted using bookend strategy based on priority:
// highest priority at start, second-highest at end, rest in middle.
func AssembleContext(ctx context.Context, cfg *config.Config, req AssembleContextRequest) (*AssembleContextResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	var allFragments []config.FragmentRef
	profileVars := make(map[string]string)

	profileName := req.Profile
	var profileNames []string
	if profileName == "" && len(req.Fragments) == 0 && len(req.Tags) == 0 {
		profileNames = cfg.GetDefaultProfiles()
	} else if profileName != "" {
		profileNames = []string{profileName}
	}

	// Process all profiles
	for _, pName := range profileNames {
		// Resolve profile with inheritance
		profile, err := resolveProfile(cfg, pName, req.ProfileLoaderFunc)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve profile %s: %w", pName, err)
		}

		// Collect variables from profile
		for k, v := range profile.Variables {
			profileVars[k] = v
		}

		// Add fragments from tags (priority 0)
		if len(profile.Tags) > 0 {
			taggedInfos, err := loader.ListByTags(profile.Tags)
			if err != nil {
				return nil, fmt.Errorf("failed to list fragments by profile tags: %w", err)
			}
			for _, info := range taggedInfos {
				allFragments = append(allFragments, config.FragmentRef{Name: info.Name, Priority: 0})
			}
		}

		// Add explicit fragments with their priorities
		allFragments = append(allFragments, profile.Fragments...)
	}

	// Add request fragments (priority 0)
	for _, f := range req.Fragments {
		allFragments = append(allFragments, config.FragmentRef{Name: f, Priority: 0})
	}

	// Add fragments from request tags (priority 0)
	if len(req.Tags) > 0 {
		taggedInfos, err := loader.ListByTags(req.Tags)
		if err != nil {
			return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
		}
		for _, info := range taggedInfos {
			allFragments = append(allFragments, config.FragmentRef{Name: info.Name, Priority: 0})
		}
	}

	// Deduplicate, keeping highest priority for each fragment
	uniqueFragments := dedupeFragmentRefs(allFragments)

	// Sort using bookend strategy for "lost in the middle" optimization
	orderedNames := sortFragmentsByPriority(uniqueFragments)

	var contextContent string
	if len(orderedNames) > 0 {
		var err error
		contextContent, err = loader.LoadMultiple(orderedNames)
		if err != nil {
			return nil, fmt.Errorf("failed to load fragments: %w", err)
		}
		// Apply variable substitution (suppress warnings in operations context)
		contextContent = substituteVariables(contextContent, profileVars, func(string) {})
	}

	return &AssembleContextResult{
		Profiles:        profileNames,
		FragmentsLoaded: orderedNames,
		Context:         contextContent,
	}, nil
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
// This addresses the "lost in the middle" problem where LLMs poorly attend to middle content.
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

// resolveProfile resolves a profile from config or directory.
func resolveProfile(cfg *config.Config, name string, profileLoaderFunc func() ProfileLoader) (*config.Profile, error) {
	// First try config-based resolution
	profile, err := config.ResolveProfile(cfg.Profiles, name)
	if err == nil {
		return profile, nil
	}

	// Fall back to directory-based resolution
	var loader ProfileLoader
	if profileLoaderFunc != nil {
		loader = profileLoaderFunc()
	} else {
		loader = cfg.GetProfileLoader()
	}
	resolved, err := loader.ResolveProfile(name, nil)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}

	// Convert to config.Profile
	// Convert string bundles to FragmentRef (priority 0 from directory profiles)
	fragments := make([]config.FragmentRef, len(resolved.Bundles))
	for i, b := range resolved.Bundles {
		fragments[i] = config.FragmentRef{Name: b, Priority: 0}
	}

	return &config.Profile{
		Tags:      resolved.Tags,
		Fragments: fragments,
		Variables: resolved.Variables,
	}, nil
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
	seen := make(map[string]bool)
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

// checkTags recursively walks mustache tags to find undefined variables.
func checkTags(tags []mustache.Tag, vars map[string]string, seen map[string]bool, warnFunc func(string)) {
	for _, tag := range tags {
		name := tag.Name()
		tagType := tag.Type()

		// Check variable tags and section tags that reference variables
		if tagType == tagVariable || tagType == tagRawVariable || tagType == tagSection || tagType == tagInvertedSection {
			if !seen[name] {
				seen[name] = true
				if _, exists := vars[name]; !exists {
					warnFunc(fmt.Sprintf("undefined variable: {{%s}}", name))
				}
			}
		}

		// Recursively check nested tags (only sections have children)
		if tagType == tagSection || tagType == tagInvertedSection {
			if children := tag.Tags(); len(children) > 0 {
				checkTags(children, vars, seen, warnFunc)
			}
		}
	}
}
