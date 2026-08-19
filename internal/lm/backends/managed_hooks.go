package backends

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// This file holds the RESOLVED MODEL of the ctxloom-managed hook set:
// ManagedHooks, what AssembleManagedHooks returns.
//
// # Why a model and not a wire config
//
// wire.Hook carries yaml:/json: tags because it IS what gets written into an
// engine's settings file (.claude/settings.json, codex config.toml, …). It has
// nowhere to put "where did this come from" and must not grow one: provenance
// written into a settings file would leak ctxloom's bookkeeping into another
// tool's configuration. So the assembly used to merge by pure append into a
// wire config and throw the source away as it went — the information existed at
// every merge site and reached nobody, which is why `manage hooks list` could
// only ever say "local" for a hook that was equally likely to be config-level or
// inline-profile.
//
// The model keeps what the merge knows. The wire config becomes a PROJECTION of
// it (Wire), so writers see exactly what they always saw.
//
// # The lifecycle
//
// A ManagedHooks is resolved once (AssembleManagedHooks), inspected (For,
// BackendNative), worked with (Reorder), and only then assembled into the
// serializable form (Wire). Order is a property of the object throughout, so
// the order inspection reports and the order Wire emits cannot be different
// numbers — they are the same slice read twice.

// HookOrigin classifies WHERE a resolved hook was declared. It is as specific as
// the merge can honestly be: each value corresponds to a distinct place a user
// can go and edit, which is the only reason to report provenance at all.
type HookOrigin string

const (
	// HookOriginConfig: the `hooks:` block of .ctxloom/config.yaml.
	HookOriginConfig HookOrigin = "config"
	// HookOriginProfileInline: an inline profile's `hooks:` block, i.e. under
	// `profiles:` in config.yaml. Distinct from HookOriginConfig even though
	// both live in the same file, because they are different blocks to edit.
	HookOriginProfileInline HookOrigin = "profile-inline"
	// HookOriginProfileDirectory: a directory profile's own `hooks:` block
	// (.ctxloom/profiles/<name>.yaml, or a bundle-shipped profile reached
	// through the same loader). These pass the executable trust gate; a hook
	// with this origin was ALLOWED by it.
	HookOriginProfileDirectory HookOrigin = "profile-directory"
	// HookOriginBuiltin: a bundle compiled into the ctxloom binary. Ref is the
	// canonical "ctxloom+builtin:<name>". Unconditional — no profile pulls
	// these in, so there is no profile to name and nothing in the project to
	// edit.
	HookOriginBuiltin HookOrigin = "builtin"
	// HookOriginCompanion: a companion binary's loadout bundle, discovered on
	// PATH. Ref is the canonical "ctxloom+companion:<bin>". Also
	// unconditional: the lever is whether the binary is installed.
	HookOriginCompanion HookOrigin = "companion"
	// HookOriginBundle: a bundle a selected profile references. Ref is the
	// bundle's source ref.
	HookOriginBundle HookOrigin = "bundle"
	// HookOriginContext: ctxloom's own context-injection hook, synthesised per
	// apply from the assembled-context hash rather than authored anywhere.
	// Nothing to go and edit.
	HookOriginContext HookOrigin = "context"
	// HookOriginUnattributed: a hook that arrived through the bundle-resolution
	// pass WITHOUT the "bundle:<ref>" marker config.extractHooksFromBundle
	// stamps. This should be unreachable, and it is a named value rather than a
	// silent fallback to HookOriginBundle precisely so that if it ever happens
	// the report says "I do not know" instead of inventing an origin. A
	// confident wrong attribution sends someone to edit the wrong file.
	HookOriginUnattributed HookOrigin = "unattributed"
)

// HookSource is one resolved hook's provenance.
type HookSource struct {
	// Origin is which KIND of place declared the hook.
	Origin HookOrigin

	// Profile names the profile a hook was declared in, for the two profile
	// origins. It is EMPTY for the bundle origins, and that is a real limit
	// rather than an oversight: cfg.ResolveBundleHooks resolves every selected
	// profile's bundles in a single pass and returns one flat set, so which
	// profile pulled a bundle in does not survive — and for a bundle that two
	// selected profiles both reference there is no single answer to survive.
	// Ref names the bundle, which is what a user acts on anyway.
	Profile string

	// Ref is the source ref of the bundle a hook came from, for the three
	// bundle origins ("ctxloom+builtin:<name>", "ctxloom+companion:<bin>", or
	// a bundle's own canonical ref). Empty otherwise.
	Ref string
}

// String renders a source as "<origin>" or "<origin> <name>", where name is the
// profile or the bundle ref, whichever the origin carries.
func (s HookSource) String() string {
	switch {
	case s.Profile != "":
		return string(s.Origin) + " " + s.Profile
	case s.Ref != "":
		return string(s.Origin) + " " + s.Ref
	default:
		return string(s.Origin)
	}
}

// ResolvedHook is one hook in the assembled set, with everything the merge knew
// about it at the point it was merged in.
type ResolvedHook struct {
	// Hook is the wire form, exactly as a writer will serialize it. Provenance
	// lives beside it, never inside it.
	Hook wire.Hook

	// Source is where the hook was declared.
	Source HookSource

	// Declared is the hook's 1-based position within its event as the MERGE
	// produced it, before any Reorder. A hook's effective position is its index
	// in the slice For returns; the two differing is exactly the information a
	// user debugging "why does my hook run third" needs, and it is only
	// available because the object keeps both.
	Declared int
}

// BackendNativeHooks is one backend-native (plugin passthrough) event's hooks.
// These bypass the six unified events entirely, so they are kept — and reported
// — separately: folding them in would imply an ordering relationship with
// unified hooks that does not exist.
type BackendNativeHooks struct {
	Backend string
	Event   string
	Hooks   []ResolvedHook
}

// ManagedHooks is the resolved ctxloom-managed hook set: every hook that will
// fire, per event, in the order it will fire in, with where each came from.
//
// It is MUTABLE and reordered IN PLACE (see Reorder). That is deliberate. The
// property this whole seam exists for is that inspection cannot disagree with
// what runs, and a Reorder that returned a new object would let a caller keep
// the pre-reorder one and hand THAT to a writer — reintroducing exactly the
// divergence the single shared resolution point was built to prevent. One
// object, one order, both callers reading the same slice.
type ManagedHooks struct {
	// events maps a unified event name to its hooks in EFFECTIVE order. An
	// absent key and an empty slice mean the same thing (no hooks).
	events map[string][]ResolvedHook

	// plugins maps backend → native event → hooks. Key PRESENCE is meaningful
	// and is preserved: the append merge creates a backend key for a source
	// that declares one even with no events, and an event key whose value is a
	// nil slice for an event declared empty. Wire reproduces that skeleton, so
	// a writer sees the same structure it always did.
	plugins map[string]map[string][]ResolvedHook
}

// HookEvents returns the six unified lifecycle event names in canonical order —
// bundles' own hook-identity order, so a reader comparing this against a
// bundle's hooks does not have to re-map anything. A fresh slice each call:
// callers range over it, and a shared package-level slice is one stray
// assignment away from silently reordering every hook report in the process.
func HookEvents() []string {
	return []string{
		bundles.HookEventPreTool, bundles.HookEventPostTool, bundles.HookEventSessionStart,
		bundles.HookEventSessionEnd, bundles.HookEventPreShell, bundles.HookEventPostFileEdit,
	}
}

// IsHookEvent reports whether name is one of the six unified events.
func IsHookEvent(name string) bool {
	for _, e := range HookEvents() {
		if e == name {
			return true
		}
	}
	return false
}

// newManagedHooks returns an empty model.
func newManagedHooks() *ManagedHooks {
	return &ManagedHooks{
		events:  make(map[string][]ResolvedHook),
		plugins: make(map[string]map[string][]ResolvedHook),
	}
}

// For returns one unified event's hooks in EFFECTIVE (post-Reorder) order, or
// nil for an event with no hooks or a name that is not an event. Callers that
// need a typo to be loud validate with IsHookEvent first — this returns the
// hooks that will fire, and for a name that names nothing that is honestly none.
//
// The returned slice aliases the model's own: callers read it, and reorder
// through Reorder rather than by sorting it, so that a reorder cannot happen
// where the object does not know about it.
func (m *ManagedHooks) For(event string) []ResolvedHook {
	if m == nil {
		return nil
	}
	return m.events[event]
}

// BackendNative returns the backend-native hooks, sorted by backend then event.
// Sorted because a map's range order is random and a report that reshuffles
// between runs cannot be diffed. Events with no hooks are omitted — the empty
// keys the merge creates are structure Wire has to reproduce, not hooks anyone
// asked about.
func (m *ManagedHooks) BackendNative() []BackendNativeHooks {
	if m == nil {
		return nil
	}
	var out []BackendNativeHooks
	for backend, events := range m.plugins {
		for event, hooks := range events {
			if len(hooks) == 0 {
				continue
			}
			out = append(out, BackendNativeHooks{Backend: backend, Event: event, Hooks: hooks})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Backend != out[j].Backend {
			return out[i].Backend < out[j].Backend
		}
		return out[i].Event < out[j].Event
	})
	return out
}

// HookRanker assigns one hook its sort rank within an event. ok=false leaves the
// hook UNRANKED: it keeps its relative order and sits after every ranked hook —
// the same reasoning as wire.HookOrderLess's absent-sorts-last, that an explicit
// claim beats no claim and a hook nobody mentioned must not overtake one someone
// did.
type HookRanker func(ResolvedHook) (rank int, ok bool)

// Reorder stable-sorts one unified event's hooks by rank, in place.
//
// # Why a ranker and not a list
//
// The output is a PERMUTATION of the input by construction: a ranker can only
// answer questions about hooks that are already there, so this signature has no
// way to express dropping one, inventing one, or editing one. An API that took
// the new list would have all three, and a reorder that can drop a hook is a
// silent security change — a pre_tool guard that stops running — wearing the
// costume of a formatting preference. The invariant is worth having in the type
// rather than in a comment and a test.
//
// # A rule that names something absent
//
// Is not expressible either, and that is the answer to "error or continue": a
// ranker is ASKED about each hook rather than naming hooks itself, so a caller
// whose rule matched nothing learns that by looking — For returns everything it
// needs, before or after. Reorder does not guess on the caller's behalf, and
// does not fail an assembly over a preference whose subject a remote bundle
// renamed.
//
// # Where this may be called from
//
// Anything that orders hooks for the PROJECT — the config-level override the
// apply-time-reorder design describes — must run inside AssembleManagedHooks,
// before either caller observes the object. A caller that reorders after
// assembly reorders only its own copy, and then inspection and apply are two
// different answers again, which is the one failure this seam exists to make
// impossible.
//
// An unknown event name is an ERROR rather than a no-op: a typo that silently
// reorders nothing is indistinguishable from a rule that had no effect.
func (m *ManagedHooks) Reorder(event string, rank HookRanker) error {
	if m == nil {
		return fmt.Errorf("reorder %q: no assembled hook set", event)
	}
	if !IsHookEvent(event) {
		return fmt.Errorf("reorder: unknown hook event %q; the lifecycle events are %s",
			event, strings.Join(HookEvents(), ", "))
	}
	if rank == nil {
		return fmt.Errorf("reorder %q: nil ranker", event)
	}
	hooks := m.events[event]
	if len(hooks) < 2 {
		return nil
	}

	// Sort a permutation of INDICES, then materialize. Sorting the hooks
	// directly would invalidate the rank lookups mid-sort, and building the
	// result by selection is what makes "same elements, new order" true by
	// construction rather than by review.
	ranks := make([]int, len(hooks))
	ranked := make([]bool, len(hooks))
	for i, h := range hooks {
		ranks[i], ranked[i] = rank(h)
	}
	order := make([]int, len(hooks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if ranked[ia] != ranked[ib] {
			return ranked[ia] // ranked hooks precede unranked ones
		}
		if !ranked[ia] {
			return false // both unranked: stability keeps their relative order
		}
		return ranks[ia] < ranks[ib]
	})
	out := make([]ResolvedHook, len(hooks))
	for i, idx := range order {
		out[i] = hooks[idx]
	}
	m.events[event] = out
	return nil
}

// Wire projects the resolved model onto the serializable hook config: exactly
// what a backend settings writer consumes, and nothing else. Provenance stops
// here by design — it is ctxloom's bookkeeping and has no business in another
// tool's settings file.
//
// The nil-vs-empty structure is reproduced, not normalized: an event with no
// hooks is a nil slice (never an empty one), a backend key declared with no
// events is an empty map, and an event key declared empty is a present key with
// a nil value — all of which is what the pure-append merge produced before the
// model existed. TestAssembleManagedHooks_WireMatchesFrozenReference holds this
// to deep equality against a frozen copy of that merge.
func (m *ManagedHooks) Wire() *wire.HooksConfig {
	out := &wire.HooksConfig{Plugins: make(map[string]wire.BackendHooks)}
	if m == nil {
		return out
	}
	for _, event := range HookEvents() {
		setUnifiedEventHooks(&out.Unified, event, wireHooks(m.events[event]))
	}
	for backend, events := range m.plugins {
		bh := make(wire.BackendHooks, len(events))
		for event, hooks := range events {
			bh[event] = wireHooks(hooks)
		}
		out.Plugins[backend] = bh
	}
	return out
}

// wireHooks strips the model down to the wire form. nil for an empty list —
// see Wire on why that distinction is preserved rather than smoothed over.
func wireHooks(hooks []ResolvedHook) []wire.Hook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]wire.Hook, len(hooks))
	for i, h := range hooks {
		out[i] = h.Hook
	}
	return out
}

// --- assembly ---------------------------------------------------------------

// hookAttributor decides one merged hook's provenance. Sources whose identity is
// known at the merge site use fixedSource; the bundle-resolution pass, which
// arrives as one flat set of several kinds of bundle, uses bundleSource.
type hookAttributor func(wire.Hook) HookSource

func fixedSource(s HookSource) hookAttributor {
	return func(wire.Hook) HookSource { return s }
}

// bundleHookMarkerPrefix is the marker config.extractHooksFromBundle stamps into
// wire.Hook.SCM for every bundle-shipped hook ("bundle:" + the canonical
// BundleIdentity of the source ref). It is the ONLY provenance the
// bundle-resolution pass carries, since that pass returns builtin, companion,
// and profile-referenced bundle hooks as one flat set.
const bundleHookMarkerPrefix = "bundle:"

// bundleSource reads a bundle-resolved hook's origin back off its SCM marker and
// classifies the ref: a builtin, a companion loadout, or a bundle a profile
// referenced. These are three genuinely different answers to "what do I change"
// — reinstall nothing, install or remove a binary, or edit a profile's bundle
// list — so they are three origins rather than one.
//
// Classification is by PARSED CLASS (trust.ParseBundleRef), never a string
// prefix test: the marker is a canonical bundle reference
// ("ctxloom+builtin:<name>", "ctxloom+companion:<bin>", …), and a class
// carried in the URI scheme cannot be spoofed by a bundle NAME that happens to
// start with the same characters the way a raw prefix test could be. A marker
// that fails to parse is Unattributed with the ref reported unattributed
// rather than misclassified, matching the "I do not know" posture
// HookOriginUnattributed documents.
func bundleSource(h wire.Hook) HookSource {
	ref, ok := strings.CutPrefix(h.SCM, bundleHookMarkerPrefix)
	if !ok {
		return HookSource{Origin: HookOriginUnattributed}
	}
	br, err := trust.ParseBundleRef(ref)
	if err != nil {
		return HookSource{Origin: HookOriginUnattributed, Ref: ref}
	}
	switch br.Class {
	case trust.ClassBuiltin:
		return HookSource{Origin: HookOriginBuiltin, Ref: ref}
	case trust.ClassCompanion:
		return HookSource{Origin: HookOriginCompanion, Ref: ref}
	default:
		return HookSource{Origin: HookOriginBundle, Ref: ref}
	}
}

// mergeHooks merges one source's whole hook config into the model, attributing
// every hook as it goes. This is the model's spelling of the pure-append merge
// (agent.MergeHooksConfig → wire.HooksConfig.Append): same order, same
// per-event concatenation, same plugin key skeleton — with the source recorded
// instead of discarded.
func (m *ManagedHooks) mergeHooks(src wire.HooksConfig, attribute hookAttributor) {
	m.mergeUnified(src.Unified, attribute)
	for backend, events := range src.Plugins {
		if m.plugins[backend] == nil {
			m.plugins[backend] = make(map[string][]ResolvedHook)
		}
		for event, hooks := range events {
			// Assigning unconditionally (rather than only when hooks is
			// non-empty) is what preserves an empty event key as a PRESENT key,
			// matching the append merge Wire has to reproduce.
			m.plugins[backend][event] = append(m.plugins[backend][event],
				m.resolve(hooks, len(m.plugins[backend][event]), attribute)...)
		}
	}
}

// mergeUnified merges the six unified events.
func (m *ManagedHooks) mergeUnified(u wire.UnifiedHooks, attribute hookAttributor) {
	for _, event := range HookEvents() {
		hooks := unifiedEventHooks(u, event)
		if len(hooks) == 0 {
			continue
		}
		m.events[event] = append(m.events[event], m.resolve(hooks, len(m.events[event]), attribute)...)
	}
}

// resolve pairs each wire hook with its provenance and its declared position,
// numbering from base (the count already merged into that event).
func (m *ManagedHooks) resolve(hooks []wire.Hook, base int, attribute hookAttributor) []ResolvedHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]ResolvedHook, 0, len(hooks))
	for i, h := range hooks {
		out = append(out, ResolvedHook{Hook: h, Source: attribute(h), Declared: base + i + 1})
	}
	return out
}

// unifiedEventHooks selects one event's slice. A switch rather than reflection
// so a seventh event added to wire.UnifiedHooks and not added here is a hole a
// reader can see — and TestManagedHooks_EveryUnifiedEventIsCovered makes it a
// failing test rather than a silently absent row in every hook report.
func unifiedEventHooks(u wire.UnifiedHooks, event string) []wire.Hook {
	switch event {
	case bundles.HookEventPreTool:
		return u.PreTool
	case bundles.HookEventPostTool:
		return u.PostTool
	case bundles.HookEventSessionStart:
		return u.SessionStart
	case bundles.HookEventSessionEnd:
		return u.SessionEnd
	case bundles.HookEventPreShell:
		return u.PreShell
	case bundles.HookEventPostFileEdit:
		return u.PostFileEdit
	}
	return nil
}

// setUnifiedEventHooks is unifiedEventHooks' write half, used by Wire.
func setUnifiedEventHooks(u *wire.UnifiedHooks, event string, hooks []wire.Hook) {
	switch event {
	case bundles.HookEventPreTool:
		u.PreTool = hooks
	case bundles.HookEventPostTool:
		u.PostTool = hooks
	case bundles.HookEventSessionStart:
		u.SessionStart = hooks
	case bundles.HookEventSessionEnd:
		u.SessionEnd = hooks
	case bundles.HookEventPreShell:
		u.PreShell = hooks
	case bundles.HookEventPostFileEdit:
		u.PostFileEdit = hooks
	}
}
