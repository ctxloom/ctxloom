package operations

import (
	"context"
	"fmt"

	"github.com/cbroglie/mustache"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/collections"
	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/profiles"
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
func AssembleContext(ctx context.Context, cfg *config.Config, req AssembleContextRequest) (*AssembleContextResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	var allFragments []string
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

		if len(profile.Tags) > 0 {
			taggedInfos, err := loader.ListByTags(profile.Tags)
			if err != nil {
				return nil, fmt.Errorf("failed to list fragments by profile tags: %w", err)
			}
			for _, info := range taggedInfos {
				allFragments = append(allFragments, info.Name)
			}
		}

		allFragments = append(allFragments, profile.Fragments...)
	}

	allFragments = append(allFragments, req.Fragments...)

	if len(req.Tags) > 0 {
		taggedInfos, err := loader.ListByTags(req.Tags)
		if err != nil {
			return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
		}
		for _, info := range taggedInfos {
			allFragments = append(allFragments, info.Name)
		}
	}

	seen := collections.NewSet[string]()
	var uniqueFragments []string
	for _, f := range allFragments {
		if !seen.Has(f) {
			seen.Add(f)
			uniqueFragments = append(uniqueFragments, f)
		}
	}

	var contextContent string
	if len(uniqueFragments) > 0 {
		var err error
		contextContent, err = loader.LoadMultiple(uniqueFragments)
		if err != nil {
			return nil, fmt.Errorf("failed to load fragments: %w", err)
		}
		// Apply variable substitution (suppress warnings in operations context)
		contextContent = substituteVariables(contextContent, profileVars, func(string) {})
	}

	return &AssembleContextResult{
		Profiles:        profileNames,
		FragmentsLoaded: uniqueFragments,
		Context:         contextContent,
	}, nil
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
		return nil, fmt.Errorf("unknown profile: %s", name)
	}

	// Convert to config.Profile
	return &config.Profile{
		Tags:      resolved.Tags,
		Fragments: resolved.Bundles,
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

		// Only check variable tags (types 1 and 2 are Variable and RawVariable)
		// Section tags (3) and inverted sections (4) also reference variables
		if tagType == 1 || tagType == 2 || tagType == 3 || tagType == 4 {
			if !seen[name] {
				seen[name] = true
				if _, exists := vars[name]; !exists {
					warnFunc(fmt.Sprintf("undefined variable: {{%s}}", name))
				}
			}
		}

		// Recursively check nested tags (only sections have children)
		// Type 3 = Section, Type 4 = Inverted Section
		if tagType == 3 || tagType == 4 {
			if children := tag.Tags(); len(children) > 0 {
				checkTags(children, vars, seen, warnFunc)
			}
		}
	}
}
