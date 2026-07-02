package operations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cbroglie/mustache"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
	Profile string `json:"profile"`
	// Profiles composes SEVERAL named profiles into one assembled context (union
	// of fragments, later-wins variables, first-non-empty llm) — the same merge
	// the configured-defaults path already runs, surfaced as an explicit ask so a
	// agent can bind multiple profiles. When non-empty it takes precedence over
	// Profile; an explicit set's resolution failures are hard errors (not the
	// fault-tolerant skip the defaults path uses).
	Profiles  []string `json:"profiles"`
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
		// Exposure surface: gate fragment/prompt content (trust rework, TR5). The
		// gate runs the baseline first (idempotent) so existing content stays
		// exposed, then withholds anything the cascade denies.
		loader = exposureLoader(cfg)
	}

	profileNames := resolveContextProfileNames(cfg, req)

	// Profiles picked up from configured defaults (rather than an explicit
	// --profile / Profiles ask) degrade per fault-tolerance: a default that fails
	// to resolve is warned about and skipped, never blocking startup. An explicit
	// single Profile or a multi-profile Profiles set is the user's ask, so its
	// failures stay hard errors.
	fromDefaults := req.Profile == "" && len(req.Profiles) == 0

	allFragments, profileVars, profileLLM, err := collectProfileFragments(cfg, loader, profileNames, req.ProfileLoaderFunc, fromDefaults)
	if err != nil {
		return nil, err
	}

	// Add request fragments (priority 0) and request-tag fragments. Bare asks
	// resolve to their qualified pipeline name at intake (deterministic pick +
	// warning when the bare name is ambiguous across bundles), so downstream
	// dedup and ordering operate on exact identities only.
	for _, f := range req.Fragments {
		allFragments = append(allFragments, config.FragmentRef{Name: loader.ResolveFragmentAsk(f), Priority: 0})
	}
	reqTagFragments, err := fragmentsFromTags(loader, req.Tags)
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
	}
	allFragments = append(allFragments, reqTagFragments...)

	// Deduplicate (highest priority wins), then bookend-sort for the
	// "lost in the middle" optimization.
	orderedRefs := sortFragmentsByPriority(dedupeFragmentRefs(allFragments))

	contextContent, loadedNames, err := loadAssembledContext(loader, orderedRefs, profileVars)
	if err != nil {
		return nil, err
	}

	// Built-in bundles inject their fragments unconditionally — the always-on
	// counterpart to their hooks/MCP (ResolveBundleHooks/ResolveBundleMCPServers)
	// — independent of profile selection, and skipped when their companion
	// binary is absent. Appended after profile/request content.
	contextContent, loadedNames = appendBuiltinFragments(cfg, contextContent, loadedNames)

	// Surface (content-free) any items the trust gate withheld during this
	// assembly so the user knows content was hidden and how to review it.
	warnWithheld(loader)

	return &AssembleContextResult{
		Profiles:        profileNames,
		FragmentsLoaded: loadedNames,
		Context:         contextContent,
		ProfileLLM:      profileLLM,
	}, nil
}

// appendBuiltinFragments appends the always-on built-in bundle fragments to the
// assembled context, joined with the same separator LoadMultiple uses so the
// output is indistinguishable from loader-sourced fragments.
func appendBuiltinFragments(cfg *config.Config, content string, loaded []string) (string, []string) {
	builtins := cfg.ResolveBuiltinBundleFragments()
	if len(builtins) == 0 {
		return content, loaded
	}
	parts := make([]string, 0, len(builtins))
	for _, f := range builtins {
		parts = append(parts, strings.TrimSpace(f.Content))
		loaded = append(loaded, f.Name)
	}
	joined := strings.Join(parts, "\n\n---\n\n")
	if content == "" {
		return joined, loaded
	}
	return content + "\n\n---\n\n" + joined, loaded
}

// resolveContextProfileNames picks the profiles to assemble from: the explicit
// request profile, else (when nothing at all is selected) the configured
// defaults. When the project config has none, defaults inherit the home
// config's defaults.profiles; if neither defines any, assembly degrades to
// empty context. No synthetic profile is ever created.
func resolveContextProfileNames(cfg *config.Config, req AssembleContextRequest) []string {
	// A multi-profile compose ask wins over the single-profile field: collected
	// in order and merged downstream by collectProfileFragments (the same loop
	// the configured-defaults path uses), so the constituent profiles of a
	// agent fold into one context.
	if len(req.Profiles) > 0 {
		return req.Profiles
	}
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
// tag list yields no refs. Names are emitted fully qualified
// ("<canonical-bundle>#fragments/<name>") — the origin bundle is known here,
// and discarding it would make same-named fragments from different bundles
// indistinguishable downstream (exclusion matching, dedup).
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
		name := remote.CanonicalBundleRef(info.Bundle) + remote.FragmentSelector + info.Name
		refs = append(refs, config.FragmentRef{Name: name, Priority: 0})
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
				clidiag.Warn("ctxloom", "skipping default profile %s: %v", pName, err)
				continue
			}
			return nil, nil, "", fmt.Errorf("failed to resolve profile %s: %w", pName, err)
		}

		if profile.LLM != "" {
			if effectiveLLM == "" {
				effectiveLLM = profile.LLM
			} else if profile.LLM != effectiveLLM {
				clidiag.Warn("ctxloom",
					"profile %q declares llm %q but %q is already in effect; keeping %q",
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
		// Profile fragment refs may pin a content version ("@<commit>"); split it
		// into FragmentRef.Version (canonicalizing the version-agnostic Name) so
		// dedup/ordering stay version-agnostic while the load step honors the pin.
		// Bundle-expanded refs (resolveProfile) already carry Version and a
		// canonical Name, so normalization is a no-op for them.
		for _, ref := range profile.Fragments {
			allFragments = append(allFragments, normalizeFragmentRef(ref))
		}
	}

	return allFragments, profileVars, effectiveLLM, nil
}

// normalizeFragmentRef splits a "@<commit>" content version off a profile
// fragment ref into FragmentRef.Version and canonicalizes the version-agnostic
// Name. A ref that already carries a Version (e.g. emitted by ExpandBundleRefs
// with a version-agnostic Name) is returned untouched so re-normalization never
// clobbers it.
func normalizeFragmentRef(ref config.FragmentRef) config.FragmentRef {
	if ref.Version != "" {
		return ref
	}
	ref.Name, ref.Version = remote.SplitFragmentVersion(ref.Name)
	return ref
}

// loadAssembledContext loads the ordered fragments (honoring per-ref content
// versions) and applies variable substitution. Returns empty content when there
// are no fragments. A fragment that fails to load — not found, gate-withheld, or
// a pinned version that fails to fetch — is skipped so the rest still assemble
// (fault tolerance), matching the tolerant LoadMultiple/GetFragment behavior.
func loadAssembledContext(loader *bundles.Loader, ordered []config.FragmentRef, profileVars map[string]string) (string, []string, error) {
	if len(ordered) == 0 {
		return "", nil, nil
	}
	var parts, loadedNames []string
	for _, ref := range ordered {
		lc, err := loadFragmentRef(loader, ref)
		if err != nil {
			warnVersionFetchFailure(ref, err)
			continue
		}
		parts = append(parts, strings.TrimSpace(lc.Content))
		loadedNames = append(loadedNames, ref.Name)
	}
	// Joined with the same separator LoadMultiple uses so the output is
	// indistinguishable regardless of which load path produced each fragment.
	content := strings.Join(parts, "\n\n---\n\n")
	// Suppress substitution warnings in the operations context.
	content = substituteVariables(content, profileVars, func(string) {})
	return content, loadedNames, nil
}

// loadFragmentRef resolves one fragment ref, honoring a pinned content version.
// An unversioned ref takes the lockfile-pinned default path (GetFragment,
// untouched); a "@<commit>"-pinned ref resolves that exact historical version
// (GetFragmentAtVersion), gated by ITS OWN effective-content hash. A version
// fetch/resolve failure fails closed (withholds the item) via the returned
// error.
func loadFragmentRef(loader *bundles.Loader, ref config.FragmentRef) (*bundles.LoadedContent, error) {
	if ref.Version == "" {
		return loader.GetFragment(ref.Name)
	}
	return loader.GetFragmentAtVersion(ref.Name, ref.Version)
}

// warnVersionFetchFailure surfaces a single warning when a version-pinned ref
// fails to fetch/resolve (the safe withhold direction, but new info worth
// diagnosing). A gate withhold is already surfaced content-free by warnWithheld,
// and an unversioned not-found stays silent (existing tolerant behavior).
func warnVersionFetchFailure(ref config.FragmentRef, err error) {
	if ref.Version == "" || errors.Is(err, errs.ErrFragmentWithheld) {
		return
	}
	clidiag.Warn("ctxloom", "withholding %s@%s: %v", ref.Name, ref.Version, err)
}

// dedupeFragmentRefs removes duplicates, keeping the highest priority for each
// fragment. Dedup identity is the version-agnostic Name (so a versioned and an
// unversioned spelling of one item collapse); among the collapsed entries an
// explicit "@<commit>" version wins over the default (one version per item).
func dedupeFragmentRefs(fragments []config.FragmentRef) []config.FragmentRef {
	priorities := make(map[string]int)
	versions := make(map[string]string)
	order := make(map[string]int) // Track first occurrence order

	for i, f := range fragments {
		if _, ok := priorities[f.Name]; ok {
			if f.Priority > priorities[f.Name] {
				priorities[f.Name] = f.Priority
			}
			// Explicit @commit wins over a default-version entry; never carry
			// two versions of one item into the assembly.
			if versions[f.Name] == "" && f.Version != "" {
				versions[f.Name] = f.Version
			}
		} else {
			priorities[f.Name] = f.Priority
			versions[f.Name] = f.Version
			order[f.Name] = i
		}
	}

	// Build result maintaining original order for same priority
	result := make([]config.FragmentRef, 0, len(priorities))
	for name, priority := range priorities {
		result = append(result, config.FragmentRef{Name: name, Priority: priority, Version: versions[name]})
	}

	// Sort by original order (for stable output when priorities are equal)
	slices.SortFunc(result, func(a, b config.FragmentRef) int {
		return order[a.Name] - order[b.Name]
	})

	return result
}

// sortFragmentsByPriority arranges fragments using bookend strategy:
// Highest priority at start, second-highest at end, rest fill middle (descending).
// This addresses the "lost in the middle" problem where Configs poorly attend to
// middle content. The returned refs preserve each FragmentRef.Version so the
// load step can honor a pinned content version.
func sortFragmentsByPriority(fragments []config.FragmentRef) []config.FragmentRef {
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
		return sorted
	}

	// Bookend placement: [highest, middle..., second-highest]
	result := make([]config.FragmentRef, len(sorted))
	result[0] = sorted[0]             // Highest priority at start
	result[len(result)-1] = sorted[1] // Second-highest at end

	// Fill middle with remaining (already sorted descending)
	for i := 2; i < len(sorted); i++ {
		result[i-1] = sorted[i]
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
			Tags:    resolved.Tags,
			Bundles: resolved.Bundles,
			// BundleItems are expanded via ExpandBundleRefs below (honoring any
			// "@<commit>" pin), exactly like Bundles — the directory-profile mirror
			// of the inline cherry-pick path.
			BundleItems: resolved.BundleItems,
			Prompts:     resolved.Prompts,
			// Direct fragments carry into the same Fragments pipeline inline
			// profiles use (collectProfileFragments → normalizeFragmentRef honors
			// "@<commit>"); filtered by exclude_fragments here, parity with the
			// inline toProfile filter.
			Fragments: convertProfileFragments(resolved.Fragments, resolved.ExcludeFragments),
			// Directly-declared hooks/mcp are executable surfaces; they reach the
			// SAME managed-hooks/MCP resolution + executable trust gate as inline
			// profiles via backends.AssembleManagedHooks / assembleManagedMCP.
			Hooks:            resolved.Hooks,
			MCP:              resolved.MCP,
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
		for _, er := range loader.ExpandBundleRefs(refs) {
			if config.IsExcludedFragment(er.Name, excluded) {
				continue
			}
			profile.Fragments = append(profile.Fragments, config.FragmentRef{Name: er.Name, Priority: 0, Version: er.Version})
		}
	}

	return profile, nil
}

// convertProfileFragments maps directory-profile fragment refs to
// config.FragmentRef for the shared assembly pipeline, preserving Name (any
// "@<commit>" pin rides along to be split transiently by normalizeFragmentRef)
// and Priority, and dropping any the profile's exclude_fragments removes — the
// directory-profile mirror of the inline toProfile exclusion filter.
func convertProfileFragments(frags []profiles.FragmentRef, exclude []string) []config.FragmentRef {
	if len(frags) == 0 {
		return nil
	}
	excluded := config.NewExclusionSet(exclude)
	out := make([]config.FragmentRef, 0, len(frags))
	for _, f := range frags {
		if config.IsExcludedFragment(f.Name, excluded) {
			continue
		}
		out = append(out, config.FragmentRef{Name: f.Name, Priority: f.Priority})
	}
	return out
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
