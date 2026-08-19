package config

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// orderedSet accumulates comparable values into an insertion-ordered,
// deduplicated set — the shape every one of profileBuilder's addX methods
// used to hand-roll separately as a (collections.Set[T], []T) field pair.
type orderedSet[T comparable] struct {
	seen  collections.Set[T]
	order []T
}

func newOrderedSet[T comparable]() orderedSet[T] {
	return orderedSet[T]{seen: collections.NewSet[T]()}
}

// Add inserts v if not already present, preserving first-seen order.
func (s *orderedSet[T]) Add(v T) {
	if !s.seen.Has(v) {
		s.seen.Add(v)
		s.order = append(s.order, v)
	}
}

// Items returns the values in insertion order.
func (s orderedSet[T]) Items() []T { return s.order }

// profileBuilder collects profile fields using sets to avoid duplicates during inheritance.
type profileBuilder struct {
	Description    string
	LLM            string
	Tags           orderedSet[string]
	SelectTags     orderedSet[string]
	Bundles        orderedSet[string]
	BundleItems    orderedSet[string]
	Fragments      collections.Set[string]
	Commands       orderedSet[string]
	Skills         orderedSet[string]
	Variables      map[string]string
	Hooks          wire.HooksConfig
	MCP            wire.MCPConfig
	fragmentsOrder []FragmentRef
	// Track fragment priorities (keep highest when same fragment referenced multiple times)
	fragmentPriorities map[string]int
	// Track seen hooks by key (command+matcher) for deduplication
	seenHooks collections.Set[string]
	// Exclusion sets - accumulate through inheritance
	ExcludeFragments collections.Set[string]
	ExcludeMCP       collections.Set[string]
	// DenyTools accumulates through inheritance like the exclusion sets above
	// (a child cannot un-deny what a parent denied) — see config_types.go's
	// Profile.DenyTools doc for the tool-deny semantics.
	DenyTools collections.Set[string]
}

func newProfileBuilder() *profileBuilder {
	return &profileBuilder{
		Tags:               newOrderedSet[string](),
		SelectTags:         newOrderedSet[string](),
		Bundles:            newOrderedSet[string](),
		BundleItems:        newOrderedSet[string](),
		Fragments:          collections.NewSet[string](),
		Commands:           newOrderedSet[string](),
		Skills:             newOrderedSet[string](),
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
		DenyTools:        collections.NewSet[string](),
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
		// An exclusion whose bundle part will not canonicalize is added as
		// AUTHORED rather than dropped. Dropping it would silently widen the
		// context: an exclusion that fails to parse must still exclude
		// something, and its own text is the only honest candidate.
		canonical, err := remote.CanonicalFragmentRef(e)
		if err != nil {
			canonical = e
		}
		set.Add(canonical)
	}
	return set
}

// IsExcludedFragment reports whether a fragment name is excluded by a set
// built with NewExclusionSet. The name's bundle part is canonicalized before
// comparison, so any reference spelling of the same bundle matches; a name
// matches on its canonical qualified form (qualified exclusions) or on its
// bare fragment name (bare exclusions).
func IsExcludedFragment(name string, excluded collections.Set[string]) bool {
	canonical, err := remote.CanonicalFragmentRef(name)
	if err != nil {
		// Matched on the authored spelling, the same fallback NewExclusionSet
		// makes, so an unparseable ref on either side still meets the other.
		canonical = name
	}
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
		Description: b.Description,
		LLM:         b.LLM,
		Tags:        b.Tags.Items(),
		SelectTags:  b.SelectTags.Items(),
		Bundles:     b.Bundles.Items(),
		BundleItems: b.BundleItems.Items(),
		Fragments:   filteredFragments,
		Commands:    b.Commands.Items(),
		Skills:      b.Skills.Items(),
		Variables:   b.Variables,
		Hooks:       b.Hooks,
		MCP:         filteredMCP,
		// These three accumulate through inheritance in a collections.Set, whose
		// Items() is MAP-ITERATION order — Go randomizes it, so an unsorted read
		// would spell a resolved profile differently on every run. They are not
		// internal bookkeeping: DenyTools rides SurfaceInputs into each engine's
		// settings surface, and AssembleManagedDenyTools documents the order as
		// deterministic. A Set carries no insertion order to restore, so the
		// stable order is the sorted one.
		ExcludeFragments: collections.SortedKeys(b.ExcludeFragments),
		ExcludeMCP:       collections.SortedKeys(b.ExcludeMCP),
		DenyTools:        collections.SortedKeys(b.DenyTools),
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
	// resolved is shared across the WHOLE resolution (unlike visited, which is
	// cloned per parent for cycle detection): a profile reached again through a
	// second DAG path has already contributed its subtree to the builder, so it
	// is short-circuited. Without this a diamond chain costs O(2^depth) — the
	// maxProfileDepth guard bounds depth, not total work, so a malformed config
	// could hang (U049-F13).
	resolved := collections.NewSet[string]()
	builder := newProfileBuilder()
	if err := resolveProfileRecursive(profiles, name, visited, resolved, builder, 0); err != nil {
		return nil, err
	}
	return builder.toProfile(), nil
}

func resolveProfileRecursive(profiles map[string]Profile, name string, visited, resolved collections.Set[string], builder *profileBuilder, depth int) error {
	if resolveVisitHook != nil {
		resolveVisitHook(name)
	}
	profile, err := guardProfileResolution(profiles, name, visited, depth)
	if err != nil {
		return err
	}

	// Already fully merged into the builder on an earlier DAG path. The
	// depth/cycle guards above still run first, so a genuine cycle is caught
	// before this short-circuit; a shared ancestor is merged exactly once,
	// which both bounds total work and stops a distant ancestor from re-winning
	// last-write over a nearer profile on a second visit.
	if resolved.Has(name) {
		return nil
	}

	if err := resolveProfileParents(profiles, profile, name, visited, resolved, builder, depth); err != nil {
		return err
	}

	mergeProfileValues(builder, profile)
	resolved.Add(name)
	return nil
}

// resolveVisitHook, when non-nil, is invoked with each profile name as it is
// (re)entered in resolveProfileRecursive. It is a test-only seam (nil in
// production, one nil check per entry) used to prove the memoization bounds
// total work (U049-F13).
var resolveVisitHook func(string)

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

// resolveProfileParents resolves each parent depth-first. An unresolvable
// parent is fatal-class in strict mode (the configured chain is an explicit
// ask; a silently thinner profile is what fail-loudly exists to catch): the
// branch is skipped so the rest still resolves, the warning streams either
// way, and the startup choke owner aborts on the collected finding. In
// degraded mode this stays warn-and-skip, matching
// profiles.Loader.ResolveProfile. Circular references and depth overruns stay
// hard errors in both modes: continuing would mask a real misconfiguration or
// risk runaway recursion.
func resolveProfileParents(profiles map[string]Profile, profile Profile, name string, visited, resolved collections.Set[string], builder *profileBuilder, depth int) error {
	for _, parentName := range profile.Parents {
		err := resolveProfileRecursive(profiles, parentName, visited.Clone(), resolved, builder, depth+1)
		if err == nil {
			continue
		}
		if errors.Is(err, errs.ErrProfileNotFound) {
			strictness.Fail(strictness.ClassRef, "fix the parents of the inline profile in .ctxloom/config.yaml",
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
		builder.Tags.Add(tag)
	}
	for _, tag := range profile.SelectTags {
		builder.SelectTags.Add(tag)
	}
	for _, bundle := range profile.Bundles {
		builder.Bundles.Add(bundle)
	}
	for _, item := range profile.BundleItems {
		builder.BundleItems.Add(item)
	}
	for _, frag := range profile.Fragments {
		builder.addFragment(frag)
	}
	for _, command := range profile.Commands {
		builder.Commands.Add(command)
	}
	for _, skill := range profile.Skills {
		builder.Skills.Add(skill)
	}
	for k, v := range profile.Variables {
		builder.Variables[k] = v
	}

	// Merge hooks (deduplicated by command+matcher) and MCP (later wins).
	builder.mergeHooks(profile.Hooks)
	mcpSource := profile.MCP
	wire.MergeMCPConfig(&builder.MCP, &mcpSource)

	for _, frag := range profile.ExcludeFragments {
		builder.ExcludeFragments.Add(frag)
	}
	for _, mcp := range profile.ExcludeMCP {
		builder.ExcludeMCP.Add(mcp)
	}
	for _, tool := range profile.DenyTools {
		builder.DenyTools.Add(tool)
	}

	builder.Description = profile.Description
	// Non-empty wins so a child without an llm inherits its parent's; the leaf
	// is merged last, so it overrides.
	if profile.LLM != "" {
		builder.LLM = profile.LLM
	}
}
