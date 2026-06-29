package config

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/collections"
	"github.com/ctxloom/shared/wire"
)

// profileBuilder collects profile fields using sets to avoid duplicates during inheritance.
type profileBuilder struct {
	Description string
	LLM         string
	Tags        collections.Set[string]
	Bundles     collections.Set[string]
	BundleItems collections.Set[string]
	Fragments   collections.Set[string]
	Prompts     collections.Set[string]
	Variables   map[string]string
	Hooks       wire.HooksConfig
	MCP         wire.MCPConfig
	// Track insertion order for stable output
	tagsOrder        []string
	bundlesOrder     []string
	bundleItemsOrder []string
	promptsOrder     []string
	fragmentsOrder   []FragmentRef
	// Track fragment priorities (keep highest when same fragment referenced multiple times)
	fragmentPriorities map[string]int
	// Track seen hooks by key (command+matcher) for deduplication
	seenHooks collections.Set[string]
	// Exclusion sets - accumulate through inheritance
	ExcludeFragments collections.Set[string]
	ExcludeMCP       collections.Set[string]
}

func newProfileBuilder() *profileBuilder {
	return &profileBuilder{
		Tags:               collections.NewSet[string](),
		Bundles:            collections.NewSet[string](),
		BundleItems:        collections.NewSet[string](),
		Fragments:          collections.NewSet[string](),
		Prompts:            collections.NewSet[string](),
		Variables:          make(map[string]string),
		fragmentPriorities: make(map[string]int),
		Hooks: wire.HooksConfig{
			Plugins: make(map[string]wire.BackendHooks),
		},
		MCP: wire.MCPConfig{
			Servers: make(map[string]wire.MCPServer),
			Plugins: make(map[string]map[string]wire.MCPServer),
		},
		seenHooks:        collections.NewSet[string](),
		ExcludeFragments: collections.NewSet[string](),
		ExcludeMCP:       collections.NewSet[string](),
	}
}

func (b *profileBuilder) addTag(tag string) {
	if !b.Tags.Has(tag) {
		b.Tags.Add(tag)
		b.tagsOrder = append(b.tagsOrder, tag)
	}
}

func (b *profileBuilder) addBundle(bundle string) {
	if !b.Bundles.Has(bundle) {
		b.Bundles.Add(bundle)
		b.bundlesOrder = append(b.bundlesOrder, bundle)
	}
}

func (b *profileBuilder) addBundleItem(item string) {
	if !b.BundleItems.Has(item) {
		b.BundleItems.Add(item)
		b.bundleItemsOrder = append(b.bundleItemsOrder, item)
	}
}

// addPrompt accumulates a curated prompt ref, deduping by its authored string
// (the version-agnostic identity is the stored ref) so a profile and its
// parents union without repeating an entry. Insertion order is preserved for a
// stable export set.
func (b *profileBuilder) addPrompt(prompt string) {
	if !b.Prompts.Has(prompt) {
		b.Prompts.Add(prompt)
		b.promptsOrder = append(b.promptsOrder, prompt)
	}
}

func (b *profileBuilder) addFragment(frag FragmentRef) {
	if !b.Fragments.Has(frag.Name) {
		b.Fragments.Add(frag.Name)
		b.fragmentsOrder = append(b.fragmentsOrder, frag)
		b.fragmentPriorities[frag.Name] = frag.Priority
	} else if frag.Priority > b.fragmentPriorities[frag.Name] {
		// Update priority if higher (child profile can override parent's priority)
		b.fragmentPriorities[frag.Name] = frag.Priority
		// Update the priority in fragmentsOrder
		for i := range b.fragmentsOrder {
			if b.fragmentsOrder[i].Name == frag.Name {
				b.fragmentsOrder[i].Priority = frag.Priority
				break
			}
		}
	}
}

// hookKey returns a unique key for deduplication. Type and Prompt are folded in
// alongside Command and Matcher so that prompt/agent hooks (which carry an empty
// Command) are keyed on their differing Prompt text rather than all collapsing
// to the same "|<matcher>" key.
func hookKey(h wire.Hook) string {
	return h.Type + "|" + h.Command + "|" + h.Prompt + "|" + h.Matcher
}

// addHook adds a hook if not already present, keyed by the lifecycle event plus
// the hook key. The event discriminator keeps the same command+matcher
// registered on two different lifecycles (e.g. a notify hook on both
// SessionStart and SessionEnd) from collapsing onto whichever list merges
// first, mirroring the plugin-specific dedup path below.
func (b *profileBuilder) addHook(event string, hooks *[]wire.Hook, h wire.Hook) {
	key := event + "|" + hookKey(h)
	if !b.seenHooks.Has(key) {
		b.seenHooks.Add(key)
		*hooks = append(*hooks, h)
	}
}

// mergeMCP merges MCP config from source into the builder.
// Later sources override earlier ones for the same server name.
func (b *profileBuilder) mergeMCP(source wire.MCPConfig) {
	wire.MergeMCPConfig(&b.MCP, &source)
}

// mergeHookSlice merges source hooks into dest, deduping within the given
// lifecycle event so the same hook is not collapsed across distinct events.
func (b *profileBuilder) mergeHookSlice(event string, source []wire.Hook, dest *[]wire.Hook) {
	for _, h := range source {
		b.addHook(event, dest, h)
	}
}

// mergeHooks merges hooks from source into the builder.
func (b *profileBuilder) mergeHooks(source wire.HooksConfig) {
	// Merge unified hooks
	b.mergeHookSlice("PreTool", source.Unified.PreTool, &b.Hooks.Unified.PreTool)
	b.mergeHookSlice("PostTool", source.Unified.PostTool, &b.Hooks.Unified.PostTool)
	b.mergeHookSlice("SessionStart", source.Unified.SessionStart, &b.Hooks.Unified.SessionStart)
	b.mergeHookSlice("SessionEnd", source.Unified.SessionEnd, &b.Hooks.Unified.SessionEnd)
	b.mergeHookSlice("PreShell", source.Unified.PreShell, &b.Hooks.Unified.PreShell)
	b.mergeHookSlice("PostFileEdit", source.Unified.PostFileEdit, &b.Hooks.Unified.PostFileEdit)

	// Merge plugin-specific hooks
	for pluginName, backendHooks := range source.Plugins {
		if b.Hooks.Plugins[pluginName] == nil {
			b.Hooks.Plugins[pluginName] = make(wire.BackendHooks)
		}
		for eventName, hooks := range backendHooks {
			for _, h := range hooks {
				key := pluginName + ":" + eventName + ":" + hookKey(h)
				if !b.seenHooks.Has(key) {
					b.seenHooks.Add(key)
					b.Hooks.Plugins[pluginName][eventName] = append(b.Hooks.Plugins[pluginName][eventName], h)
				}
			}
		}
	}
}

// NewExclusionSet builds the match set for exclude_fragments entries. Bare
// names are kept verbatim — a bare exclusion matches its fragment name in
// any bundle, by design (the inherit-three-bundles case shouldn't need three
// qualified exclusions). Qualified refs canonicalize their bundle part
// (remote.CanonicalFragmentRef) so they compare exactly against pipeline
// names, which always carry canonical bundle identities: a qualified
// exclusion drops only that bundle's fragment, leaving same-named fragments
// from other bundles intact. Every exclusion seam must build its set here
// and match via IsExcludedFragment.
func NewExclusionSet(exclusions []string) collections.Set[string] {
	set := collections.NewSet[string]()
	for _, e := range exclusions {
		set.Add(remote.CanonicalFragmentRef(e))
	}
	return set
}

// IsExcludedFragment reports whether a fragment name is excluded by a set
// built with NewExclusionSet. The name's bundle part is canonicalized before
// comparison, so any reference spelling of the same bundle matches; a name
// matches on its canonical qualified form (qualified exclusions) or on its
// bare fragment name (bare exclusions).
func IsExcludedFragment(name string, excluded collections.Set[string]) bool {
	canonical := remote.CanonicalFragmentRef(name)
	if excluded.Has(canonical) {
		return true
	}
	if bare, ok := remote.FragmentName(canonical); ok {
		return excluded.Has(bare)
	}
	return false
}

func (b *profileBuilder) toProfile() *Profile {
	// Filter excluded fragments
	excluded := NewExclusionSet(b.ExcludeFragments.Items())
	var filteredFragments []FragmentRef
	for _, frag := range b.fragmentsOrder {
		if !IsExcludedFragment(frag.Name, excluded) {
			filteredFragments = append(filteredFragments, frag)
		}
	}

	// Filter excluded MCP servers
	filteredMCP := b.MCP
	if len(b.ExcludeMCP.Items()) > 0 && filteredMCP.Servers != nil {
		filteredServers := make(map[string]wire.MCPServer)
		for name, server := range filteredMCP.Servers {
			if !b.ExcludeMCP.Has(name) {
				filteredServers[name] = server
			}
		}
		filteredMCP.Servers = filteredServers
	}

	return &Profile{
		Description:      b.Description,
		LLM:              b.LLM,
		Tags:             b.tagsOrder,
		Bundles:          b.bundlesOrder,
		BundleItems:      b.bundleItemsOrder,
		Fragments:        filteredFragments,
		Prompts:          b.promptsOrder,
		Variables:        b.Variables,
		Hooks:            b.Hooks,
		MCP:              filteredMCP,
		ExcludeFragments: b.ExcludeFragments.Items(),
		ExcludeMCP:       b.ExcludeMCP.Items(),
	}
}

// maxProfileDepth is the maximum allowed depth for profile inheritance.
// This prevents stack overflow from deeply nested or malformed configurations.
// The value 64 is arbitrary but well beyond any reasonable inheritance chain.
const maxProfileDepth = 64

// ResolveProfile resolves a profile by recursively merging all parent profiles.
// Parents are processed depth-first, with later parents and the child overriding earlier values.
// Uses sets internally to handle diamond inheritance (shared ancestors) without duplicates.
// Returns an error if the profile doesn't exist or if circular dependencies are detected.
func ResolveProfile(profiles map[string]Profile, name string) (*Profile, error) {
	visited := collections.NewSet[string]()
	builder := newProfileBuilder()
	if err := resolveProfileRecursive(profiles, name, visited, builder, 0); err != nil {
		return nil, err
	}
	return builder.toProfile(), nil
}

func resolveProfileRecursive(profiles map[string]Profile, name string, visited collections.Set[string], builder *profileBuilder, depth int) error {
	profile, err := guardProfileResolution(profiles, name, visited, depth)
	if err != nil {
		return err
	}

	if err := resolveProfileParents(profiles, profile, name, visited, builder, depth); err != nil {
		return err
	}

	mergeProfileValues(builder, profile)
	return nil
}

// guardProfileResolution enforces the depth limit and circular-dependency
// guard, marks name visited, and returns the profile (or ErrProfileNotFound).
func guardProfileResolution(profiles map[string]Profile, name string, visited collections.Set[string], depth int) (Profile, error) {
	if depth > maxProfileDepth {
		return Profile{}, fmt.Errorf("profile inheritance depth exceeds maximum (%d): possible misconfiguration", maxProfileDepth)
	}
	if visited.Has(name) {
		return Profile{}, fmt.Errorf("profile %q: %w", name, errs.ErrCircularInheritance)
	}
	visited.Add(name)

	profile, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile %q: %w", name, errs.ErrProfileNotFound)
	}
	return profile, nil
}

// resolveProfileParents resolves each parent depth-first. Per ctxloom's
// fault-tolerance philosophy (CLAUDE.md) — and matching
// profiles.Loader.ResolveProfile — an unresolvable parent is a stderr warning
// and that branch is skipped, so the rest of the profile still resolves and the
// user reaches their LLM. Circular references and depth overruns stay fatal:
// continuing would mask a real misconfiguration or risk runaway recursion.
func resolveProfileParents(profiles map[string]Profile, profile Profile, name string, visited collections.Set[string], builder *profileBuilder, depth int) error {
	for _, parentName := range profile.Parents {
		err := resolveProfileRecursive(profiles, parentName, visited.Clone(), builder, depth+1)
		if err == nil {
			continue
		}
		if errors.Is(err, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom",
				"profile %q: parent %q not found; skipping",
				name, parentName)
			continue
		}
		return fmt.Errorf("failed to resolve parent %s: %w", parentName, err)
	}
	return nil
}

// mergeProfileValues folds one profile's values into the builder. Children
// override parents for variables; exclusions always accumulate (cannot
// un-exclude); description comes from the leaf (overwritten by each child).
func mergeProfileValues(builder *profileBuilder, profile Profile) {
	for _, tag := range profile.Tags {
		builder.addTag(tag)
	}
	for _, bundle := range profile.Bundles {
		builder.addBundle(bundle)
	}
	for _, item := range profile.BundleItems {
		builder.addBundleItem(item)
	}
	for _, frag := range profile.Fragments {
		builder.addFragment(frag)
	}
	for _, prompt := range profile.Prompts {
		builder.addPrompt(prompt)
	}
	for k, v := range profile.Variables {
		builder.Variables[k] = v
	}

	// Merge hooks (deduplicated by command+matcher) and MCP (later wins).
	builder.mergeHooks(profile.Hooks)
	builder.mergeMCP(profile.MCP)

	for _, frag := range profile.ExcludeFragments {
		builder.ExcludeFragments.Add(frag)
	}
	for _, mcp := range profile.ExcludeMCP {
		builder.ExcludeMCP.Add(mcp)
	}

	builder.Description = profile.Description
	// Non-empty wins so a child without an llm inherits its parent's; the leaf
	// is merged last, so it overrides.
	if profile.LLM != "" {
		builder.LLM = profile.LLM
	}
}
