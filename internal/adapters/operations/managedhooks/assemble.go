package managedhooks

import (
	"fmt"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// itemRefFor mints the canonical "<source>#<kind>/<item>" reference a
// profile's declared hook is addressed by, and REFUSES a source it cannot
// address, NAMING that source. It takes the source as a STRING (unlike
// bundles.ItemRefFor, which callers holding a BundleRead can call directly)
// because a directory profile's identity (profileRefBase, from
// profiles.ResolvedProfile.SourceRef) has no typed sibling. parseSourceRef
// resolves that string into the bundles.ItemRefFor.
//
// The refusal is the boundary: an item whose source will not parse has no
// identity, and inventing one for it would smuggle a parse failure downstream
// through the identity channel for a later stage to refuse. Callers withhold
// that ONE item and keep going.
func itemRefFor(source string, kind trust.ItemKind, item string) (string, error) {
	src, err := parseSourceRef(source)
	if err != nil {
		return "", fmt.Errorf("cannot address source %q: %w", source, err)
	}
	return bundles.ItemRefFor(src, kind, item)
}

// parseSourceRef resolves a bundle-level source ref STRING — a profile's own
// canonical origin ref (profiles.ResolvedProfile.SourceRef) — into the
// structured trust.BundleRef bundles.ItemRefFor needs.
//
// A canonical URI is parsed as one. Anything else is the assembly pipeline's
// own identity spelling (remote.CanonicalBundleRef's "ctxloom:local@bundles/
// <name>", an authored "<url>@bundles/<path>") or a bare local bundle name,
// resolved through the reference grammar that mints those and bridged onto
// trust.BundleRef by trust.Ref.AsBundleRef — the same bridge every other
// holder of such a string uses. It lives here because this is the single
// caller holding a plain string with no typed source to hand across; if that
// caller acquires a typed source, this helper goes with it rather than
// growing users.
func parseSourceRef(source string) (trust.BundleRef, error) {
	if br, err := trust.ParseBundleRef(source); err == nil {
		return br, nil
	}
	parsed, err := remote.ParseReference(source)
	if err != nil {
		if remote.IsSelfContainedRef(source) {
			// A string carrying a scheme marker that does not parse was
			// INTENDED as a qualified reference. Reading it as a bare local
			// bundle name would hand it the first-party exemption an
			// unrecognized source must never get.
			return trust.BundleRef{}, fmt.Errorf("parse %q: %w", source, err)
		}
		return trust.LocalRef(source)
	}
	br, err := trust.Ref{
		RepoURL:     parsed.URL,
		Bundle:      parsed.Path,
		IsLocal:     parsed.IsLocal,
		IsCompanion: parsed.IsCompanion,
	}.AsBundleRef()
	if err != nil {
		return trust.BundleRef{}, fmt.Errorf("convert %q: %w", source, err)
	}
	return br, nil
}

// Assemble builds the COMPLETE ctxloom-managed hook set that every
// writer of a backend settings file must produce identically: config-level
// hooks, default-profile-shipped hooks, bundle-shipped hooks, and ctxloom's
// own hooks.
//
// Both writers route through this via operations.AssemblePackage: the
// `ctxloom run` payload (agent.ManagedConfigFor) and operations.ApplyHooks.
// The context-injection hook is NOT assembled here: its identity is the
// context hash only the writer knows, so each appends it itself —
// BaseLifecycle.MergeManaged from the plugin-side hash, applyHooksToBackend
// from the regenerated one. A hook one writer assembled and the other did not is
// withdrawn by the next delivery of the other. Keeping the full assembly here
// guarantees both writers produce an identical, complete set.
//
// Returns a fresh Hooks each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
//
// The return value is the RESOLVED MODEL (Hooks), not a wire config: it keeps
// each hook's provenance and declared position, which a pure-append wire merge
// discards, and it is what any project-level hook ORDERING has to act on.
// Writers take the projection, Hooks.Wire.
func Assemble(cfg *config.Config, profileNames []string) *Hooks {
	if cfg == nil {
		return newHooks()
	}
	return AssembleFor(cfg, cfg.ResolveProfileSet(profileNames), sessions.MailByHook)
}

// AssembleFor is Assemble over an already resolved
// profile set — the one assembly resolved, so its faults are reported once —
// for a session whose spool mail reads. Assemble's at-rest writers serve the
// sessions a human drives, so they assemble for sessions.MailByHook.
func AssembleFor(cfg *config.Config, set []profiles.ResolvedProfile, mail sessions.MailReader) *Hooks {
	hooks := newHooks()
	if cfg == nil {
		return hooks
	}
	// Selected-profile-shipped hooks: a profile's directly-declared hooks,
	// each addressed under the profile's own source.
	for i := range set {
		resolved := &set[i]
		profileName := resolved.Name
		declared := addressableProfileHooks(profileRefBase(resolved, profileName), resolved.Hooks)
		// Ref carries the ORIGIN BUNDLE for a bundle-shipped profile (empty for
		// a genuinely local one), so the report names the bundle a
		// remote-sourced profile came from rather than only the ref a user
		// pasted into their agent's profile list.
		hooks.mergeHooks(declared, fixedSource(Source{
			Origin:  OriginProfileDirectory,
			Profile: profileName,
			Ref:     resolved.SourceRef,
		}))
	}
	// Bundle-shipped hooks + (optional) the context-injection hook.
	appendManagedDynamicHooks(hooks, cfg, set, mail)
	return hooks
}

// appendManagedDynamicHooks appends the ctxloom-managed hooks that are assembled
// dynamically (rather than read verbatim from one config block): the
// bundle-shipped hooks (SCM-tagged — e.g. `session bind`, `stamp-plan`) and
// ctxloom's own hooks, which belong to no bundle.
//
// The bundle set arrives FLAT — builtins, companion loadouts, and each selected
// profile's bundles in one slice — so it is attributed per hook off the marker
// config.extractHooksFromBundle stamped (bundleSource), not from this call site.
func appendManagedDynamicHooks(m *Hooks, cfg *config.Config, set []profiles.ResolvedProfile, mail sessions.MailReader) {
	if m == nil || cfg == nil {
		return
	}
	m.mergeUnified(cfg.ResolveBundleHooksFor(set), bundleSource)
	// The PostToolUse reflect hook rides the same managed set as context
	// injection, and for the same reason: it exists to keep the distilled
	// essence honest, so it belongs to ctxloom rather than to any bundle.
	if minBytes, enabled := cfg.GetToolReflectBytes(); enabled {
		m.mergeUnified(
			wire.UnifiedHooks{PostTool: []wire.Hook{agent.NewToolReflectHook(minBytes)}},
			fixedSource(Source{Origin: OriginContext}))
	}
	// The PostToolUse skill-mates hook rides the same managed set: link
	// groups are ctxloom's own delivery unit, so the step that follows one
	// up at a skill's completion belongs to ctxloom rather than to any
	// bundle. Ungated -- it is silent for every skill outside a group, so
	// the only thing to configure would be whether a group may be followed.
	m.mergeUnified(
		wire.UnifiedHooks{PostTool: []wire.Hook{agent.NewSkillMatesHook()}},
		fixedSource(Source{Origin: OriginContext}))
	// The TurnEnd next-step hook rides the same managed set, for the same
	// reason as the reflect hook above: it exists to make the distilled
	// essence task-aware, so it belongs to ctxloom rather than to any bundle.
	// Ungated — it takes no configuration, because the only thing there would
	// be to configure is whether distillation is allowed to know what the
	// session was doing.
	m.mergeUnified(
		wire.UnifiedHooks{TurnEnd: []wire.Hook{agent.NewNextStepHook()}},
		fixedSource(Source{Origin: OriginContext}))
	// The turn_start mail-drain hook rides the same managed set: it is the
	// session owner's only spool reader, so it belongs to ctxloom rather than
	// to any bundle. Ungated — a session with no mail is handed nothing, so
	// the only thing to configure would be whether the owner may receive.
	// The OWNER's only: every other run — an interactive delegated child
	// included — is handed its mail by its runner AS its turn, consumed once
	// the turn has started, so a second reader there would claim the same
	// file during that turn and deliver it twice.
	if mail == sessions.MailByHook {
		m.mergeUnified(
			wire.UnifiedHooks{TurnStart: []wire.Hook{agent.NewMailDrainHook()}},
			fixedSource(Source{Origin: OriginContext}))
	}
}

// profileRefBase is the ref a profile's declared hooks are addressed under —
// the profile's own SOURCE, never its display name: resolved.SourceRef
// (profiles.ResolvedProfile) is the canonical ref of the bundle the profile is
// an item of, WITHOUT the "#profiles/<name>" selector, so the composed
// "<SourceRef>#hooks/..." ref carries exactly one '#' and parses. A profile
// without one falls back to its bare name, which addresses it as
// project-local.
func profileRefBase(resolved *profiles.ResolvedProfile, profileName string) string {
	if resolved == nil || resolved.SourceRef == "" {
		return profileName
	}
	return resolved.SourceRef
}

// addressableProfileHooks returns the hooks of a directory-resolved profile
// that can be addressed: each is keyed on itemRefFor(base, trust.KindHook,
// "<event>/<index>") (the SAME identity scheme bundle hooks use,
// bundles.HookEntry); one nothing can address is a named load error and is
// omitted.
func addressableProfileHooks(base string, h wire.HooksConfig) wire.HooksConfig {
	keep := func(event string, hooks []wire.Hook) []wire.Hook {
		var out []wire.Hook
		for i, hook := range hooks {
			if _, err := itemRefFor(base, trust.KindHook, event+"/"+strconv.Itoa(i)); err != nil {
				clidiag.Warn("ctxloom", "profile hook %q withheld: %v", hook.Line(), err)
				continue
			}
			out = append(out, hook)
		}
		return out
	}
	out := wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool:      keep(bundles.HookEventPreTool, h.Unified.PreTool),
			PostTool:     keep(bundles.HookEventPostTool, h.Unified.PostTool),
			SessionStart: keep(bundles.HookEventSessionStart, h.Unified.SessionStart),
			SessionEnd:   keep(bundles.HookEventSessionEnd, h.Unified.SessionEnd),
			PreShell:     keep(bundles.HookEventPreShell, h.Unified.PreShell),
			PostFileEdit: keep(bundles.HookEventPostFileEdit, h.Unified.PostFileEdit),
			TurnEnd:      keep(bundles.HookEventTurnEnd, h.Unified.TurnEnd),
			TurnStart:    keep(bundles.HookEventTurnStart, h.Unified.TurnStart),
			// Not a bundle-authorable event (bundles.BundleHooks), so it is
			// keyed by the wire vocabulary's own spelling.
			PermissionAsk: keep(wire.HookEventPermissionAsk, h.Unified.PermissionAsk),
		},
	}
	// Engine-native (ext) hooks are addressed too; keyed on
	// itemRefFor(base, trust.KindHook, "<engine>/<event>/<index>").
	if len(h.Ext) > 0 {
		out.Ext = make(map[string]wire.BackendHooks, len(h.Ext))
		for engine, backend := range h.Ext {
			bh := make(wire.BackendHooks)
			for event, hooks := range backend {
				if kept := keep(engine+"/"+event, hooks); len(kept) > 0 {
					bh[event] = kept
				}
			}
			if len(bh) > 0 {
				out.Ext[engine] = bh
			}
		}
	}
	return out
}
