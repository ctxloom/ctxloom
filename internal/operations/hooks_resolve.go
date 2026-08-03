package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// Hook source kinds, as reported by ResolvedHook.SourceKind. Each is a distinct
// place a user can go and edit, which is the only reason to report a source at
// all — and each is carried by the resolved model (backends.HookOrigin) rather
// than guessed after the fact.
//
// These used to be three coarse labels, because the merge appended into a wire
// config and discarded the source as it went, leaving config-level and
// inline-profile hooks genuinely indistinguishable. The model keeps what the
// merge knows, so the limit is gone rather than papered over.
const (
	// SourceKindConfig: the `hooks:` block of .ctxloom/config.yaml.
	SourceKindConfig = string(backends.HookOriginConfig)
	// SourceKindProfileInline: an inline profile's `hooks:` block (under
	// `profiles:` in config.yaml). Source names the profile.
	SourceKindProfileInline = string(backends.HookOriginProfileInline)
	// SourceKindProfileDirectory: a directory profile's own `hooks:` block.
	// Source names the profile.
	SourceKindProfileDirectory = string(backends.HookOriginProfileDirectory)
	// SourceKindBuiltin: a bundle compiled into the ctxloom binary. Source is
	// "builtin:<name>".
	SourceKindBuiltin = string(backends.HookOriginBuiltin)
	// SourceKindCompanion: a companion binary's loadout, discovered on PATH.
	// Source is "ctxloom:companion@<bin>".
	SourceKindCompanion = string(backends.HookOriginCompanion)
	// SourceKindBundle: a bundle a selected profile references, and Source
	// names it.
	SourceKindBundle = string(backends.HookOriginBundle)
	// SourceKindContext: ctxloom's own context-injection hook, synthesised per
	// apply rather than authored anywhere. Nothing to go and edit.
	SourceKindContext = string(backends.HookOriginContext)
	// SourceKindUnattributed: a bundle-resolved hook carrying no origin marker.
	// Unreachable in practice, and reported as "I do not know" rather than
	// rounded to the nearest plausible source — see backends.HookOriginUnattributed.
	SourceKindUnattributed = string(backends.HookOriginUnattributed)
)

// ResolveHooksRequest asks what hooks will actually fire, and in what order.
type ResolveHooksRequest struct {
	// Event narrows the report to one lifecycle event. Empty means all six. An
	// unrecognised name is an ERROR, never an empty result.
	Event string `json:"event,omitempty"`
	// Profiles overrides the profile set to resolve against. Empty means the
	// configured defaults — the same set an apply would use.
	Profiles []string `json:"profiles,omitempty"`

	ConfigLoader ConfigLoaderFunc `json:"-"` // test seam, mirrors ApplyHooksRequest
	WorkDir      string           `json:"-"`
}

// ResolvedHook is one hook in its final position.
type ResolvedHook struct {
	// Position is 1-based within its event: the order this hook fires in.
	Position int `json:"position"`
	// Declared is the 1-based position the MERGE gave this hook, before any
	// reordering. It equals Position unless something moved the hook, and
	// reporting both is what turns "why does my hook run third" into an answer
	// rather than a fact.
	Declared int `json:"declared,omitempty"`
	// SourceKind is one of the SourceKind* constants; Source names the profile
	// or bundle the kind refers to, and is empty for the kinds that name
	// nothing (config, context).
	SourceKind string `json:"source_kind"`
	Source     string `json:"source,omitempty"`

	Type    string `json:"type,omitempty"`
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Async   bool   `json:"async,omitempty"`
}

// ResolvedHookEvent is one lifecycle event's fully resolved hook list.
type ResolvedHookEvent struct {
	Event string         `json:"event"`
	Hooks []ResolvedHook `json:"hooks"`
}

// ResolvedBackendHooks is one backend-native event's hooks. These bypass the six
// unified events, so they are reported separately rather than folded in — folding
// them in would imply an ordering relationship with unified hooks that does not
// exist.
type ResolvedBackendHooks struct {
	Backend string         `json:"backend"`
	Event   string         `json:"event"`
	Hooks   []ResolvedHook `json:"hooks"`
}

// ResolveHooksResult is the whole answer.
type ResolveHooksResult struct {
	Events        []ResolvedHookEvent    `json:"events"`
	BackendNative []ResolvedBackendHooks `json:"backend_native,omitempty"`
}

// resolvedHookEventOrder is the order events are reported in: the resolved
// model's own canonical order, so the report cannot enumerate a different set
// of events than the assembly resolved.
func resolvedHookEventOrder() []string { return backends.HookEvents() }

// ResolveHooks reports the hooks that will fire, per event, in their final order,
// with where each came from.
//
// # Why this resolves through the APPLY path rather than reading bundles
//
// The order a user gets is emergent: config-level hooks, then inline profiles,
// then gated directory profiles, then builtins, then companions, then each
// profile's bundles — merged by pure APPEND, with each bundle's own `order:`
// sequencing only its own hooks within an event. No single input states the
// result, which is exactly why the result was invisible.
//
// So this calls backends.AssembleManagedHooks — the SAME function
// applyHooksToBackend calls — rather than re-deriving the set from bundles. An
// inspect surface that computed the answer its own way would be a second
// implementation of the merge, and the two would drift; the failure mode is an
// inspect command that confidently reports an order the engine does not run,
// which is worse than no inspect command at all. It is also the constraint any
// apply-time REORDER has to respect: the override must land inside this
// resolution, upstream of this function's read, so what is reported is the
// post-override effect and never the pre-override order.
//
// The executable trust gate is attached, so a hook withheld from apply is
// withheld from the report too. Showing a hook that will not run is the same lie
// in the other direction.
func ResolveHooks(ctx context.Context, req ResolveHooksRequest) (*ResolveHooksResult, error) {
	if req.Event != "" && !backends.IsHookEvent(req.Event) {
		return nil, fmt.Errorf("unknown hook event %q; the lifecycle events are %s",
			req.Event, strings.Join(resolvedHookEventOrder(), ", "))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg, err := resolveHookConfig(ApplyHooksRequest{ConfigLoader: req.ConfigLoader})
	if err != nil {
		return nil, err
	}
	// Same gate the apply path builds, for the same reason: a hook the gate
	// denies never reaches backend settings, so it must not be reported as
	// something that will fire.
	gate := NewExecutableTrustGate(cfg)
	cfg.SetExecutableTrustGate(gate.Gate())

	workDir := req.WorkDir
	if workDir == "" {
		workDir = resolveHookWorkDir(ApplyHooksRequest{})
	}

	// contextHash is deliberately EMPTY. A non-empty one would make
	// AssembleManagedHooks synthesise a session_start context-injection hook
	// keyed to a hash this call just invented, and reporting a hook whose
	// identity is an artefact of having asked the question is not inspection.
	// Apply computes a real hash because it is about to write it down; this is
	// not, and says so rather than faking one.
	assembled := backends.AssembleManagedHooks(cfg, workDir, "", req.Profiles)

	out := &ResolveHooksResult{}
	for _, event := range resolvedHookEventOrder() {
		if req.Event != "" && event != req.Event {
			continue
		}
		out.Events = append(out.Events, ResolvedHookEvent{
			Event: event,
			Hooks: describeHooks(assembled.For(event)),
		})
	}
	out.BackendNative = describeBackendNative(assembled.BackendNative(), req.Event)
	return out, nil
}

// describeHooks projects the resolved model onto the reportable DTO. Position is
// the slice index because on this path position IS the answer: the model holds
// each event's hooks in the order they will fire, and the per-bundle order field
// has already been consumed upstream in config.extractHooksFromBundle.
func describeHooks(hooks []backends.ResolvedHook) []ResolvedHook {
	out := make([]ResolvedHook, 0, len(hooks))
	for i, h := range hooks {
		out = append(out, ResolvedHook{
			Position:   i + 1,
			Declared:   h.Declared,
			SourceKind: string(h.Source.Origin),
			Source:     hookSourceName(h.Source),
			Type:       h.Hook.Type,
			Matcher:    h.Hook.Matcher,
			Command:    h.Hook.Command,
			Prompt:     h.Hook.Prompt,
			Timeout:    h.Hook.Timeout,
			Async:      h.Hook.Async,
		})
	}
	return out
}

// hookSourceName is the thing a SourceKind refers to: the profile for the two
// profile kinds, the bundle ref for the bundle kinds, nothing for the kinds that
// name nothing. A bundle-shipped directory profile carries both; the PROFILE is
// what a user selected and what they would deselect, so it wins.
func hookSourceName(s backends.HookSource) string {
	if s.Profile != "" {
		return s.Profile
	}
	return s.Ref
}

// describeBackendNative projects the model's backend-native hooks, already
// sorted by backend then event (a map's range order is random, and an inspect
// command whose output reshuffles between runs cannot be diffed).
//
// The event filter is applied by NAME here too, even though these are a
// backend's own event names rather than the unified six — so `--event pre_tool`
// on a backend that happens to spell an event that way still narrows honestly,
// and one that does not simply reports nothing native.
func describeBackendNative(native []backends.BackendNativeHooks, eventFilter string) []ResolvedBackendHooks {
	var out []ResolvedBackendHooks
	for _, n := range native {
		if eventFilter != "" && n.Event != eventFilter {
			continue
		}
		out = append(out, ResolvedBackendHooks{
			Backend: n.Backend,
			Event:   n.Event,
			Hooks:   describeHooks(n.Hooks),
		})
	}
	return out
}
