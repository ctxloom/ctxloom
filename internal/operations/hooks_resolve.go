package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Hook source kinds, as reported by ResolvedHook.SourceKind.
//
// These are deliberately COARSE, because the merge path genuinely does not
// preserve more. Claiming a precision the data does not have would be worse than
// admitting the limit: a user acting on a confident wrong attribution edits the
// wrong file and concludes the tool lied.
const (
	// SourceKindBundle: the hook came from a bundle, and Source names it.
	SourceKindBundle = "bundle"
	// SourceKindContext: ctxloom's own context-injection hook, synthesised per
	// apply rather than authored anywhere. Nothing to go and edit.
	SourceKindContext = "context"
	// SourceKindLocal: config.yaml or an inline profile. These are
	// INDISTINGUISHABLE after the merge — neither stamps a marker on its hooks —
	// so this one label honestly covers both rather than guessing between them.
	SourceKindLocal = "local"
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
	// SourceKind is one of the SourceKind* constants; Source names the bundle
	// when SourceKind is SourceKindBundle, and is empty otherwise.
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

// resolvedHookEventOrder is the order events are reported in. It matches
// bundles' canonical event order so a reader comparing this output against a
// bundle's own hooks does not have to re-map anything.
var resolvedHookEventOrder = []string{
	bundles.HookEventPreTool, bundles.HookEventPostTool, bundles.HookEventSessionStart,
	bundles.HookEventSessionEnd, bundles.HookEventPreShell, bundles.HookEventPostFileEdit,
}

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
	if req.Event != "" && !isKnownHookEvent(req.Event) {
		return nil, fmt.Errorf("unknown hook event %q; the lifecycle events are %s",
			req.Event, strings.Join(resolvedHookEventOrder, ", "))
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
	for _, event := range resolvedHookEventOrder {
		if req.Event != "" && event != req.Event {
			continue
		}
		out.Events = append(out.Events, ResolvedHookEvent{
			Event: event,
			Hooks: describeHooks(unifiedEventHooks(assembled.Unified, event)),
		})
	}
	out.BackendNative = describeBackendNative(assembled.Plugins, req.Event)
	return out, nil
}

func isKnownHookEvent(event string) bool {
	for _, e := range resolvedHookEventOrder {
		if e == event {
			return true
		}
	}
	return false
}

// unifiedEventHooks selects one event's slice. A switch rather than reflection so
// a seventh event added to wire.UnifiedHooks and not added here is a compile-time
// hole a reader can see, not a silently absent row in the report.
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

// describeHooks numbers a resolved slice and attributes each entry. Position is
// the slice index because on this path position IS the answer: merge order is
// execution order, and the per-bundle order field has already been consumed
// upstream in config.extractHooksFromBundle.
func describeHooks(hooks []wire.Hook) []ResolvedHook {
	out := make([]ResolvedHook, 0, len(hooks))
	for i, h := range hooks {
		kind, source := attributeHook(h)
		out = append(out, ResolvedHook{
			Position:   i + 1,
			SourceKind: kind,
			Source:     source,
			Type:       h.Type,
			Matcher:    h.Matcher,
			Command:    h.Command,
			Prompt:     h.Prompt,
			Timeout:    h.Timeout,
			Async:      h.Async,
		})
	}
	return out
}

// attributeHook reads back the only provenance the merge preserves: the
// "bundle:<ref>" marker config.extractHooksFromBundle stamps into wire.Hook.SCM.
func attributeHook(h wire.Hook) (kind, source string) {
	if h.ContextHash != "" {
		return SourceKindContext, ""
	}
	if ref, ok := strings.CutPrefix(h.SCM, "bundle:"); ok {
		return SourceKindBundle, ref
	}
	return SourceKindLocal, ""
}

// describeBackendNative flattens the backend-keyed plugin map into a sorted
// slice. Sorted because a map's range order is random and an inspect command
// whose output reshuffles between runs cannot be diffed.
//
// The event filter is applied by NAME here too, even though these are a
// backend's own event names rather than the unified six — so `--event pre_tool`
// on a backend that happens to spell an event that way still narrows honestly,
// and one that does not simply reports nothing native.
func describeBackendNative(plugins map[string]wire.BackendHooks, eventFilter string) []ResolvedBackendHooks {
	var out []ResolvedBackendHooks
	for backend, events := range plugins {
		for event, hooks := range events {
			if eventFilter != "" && event != eventFilter {
				continue
			}
			if len(hooks) == 0 {
				continue
			}
			out = append(out, ResolvedBackendHooks{
				Backend: backend,
				Event:   event,
				Hooks:   describeHooks(hooks),
			})
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
