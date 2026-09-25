package managedhooks

import (
	"fmt"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// itemRefFor mints the canonical "<source>#<kind>/<item>" reference this
// file's executable-surface producers key their gate on, and REFUSES a source
// it cannot address, NAMING that source. It takes the source as a STRING
// (unlike bundles.ItemRefFor, which callers holding a BundleRead can call
// directly) because a directory profile's gate identity (profileGateRef.Base,
// from profiles.ResolvedProfile.SourceRef) has no typed sibling — see
// gateProfileHooks's tests, which build a profileGateRef with a
// Base and no Read at all. parseSourceRef resolves that string into the
// bundles.ItemRefFor.
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
// hooks, default-profile-shipped hooks, bundle-shipped hooks, and (when
// contextHash is non-empty) the context-injection hook.
//
// Both writers route through this — the `ctxloom run` setup payload
// (AssembleManagedConfig, which passes contextHash "" so the agent appends its
// own injection hook) and operations.ApplyHooks (which passes the resolved
// hash). WriteSettings reconciles by removing ALL ctxloom hooks and re-adding
// only the writer's assembled set, so any divergence between the writers
// silently drops whatever one assembled but the other didn't — the failure
// class that once broke forward-bind. Keeping the full assembly here guarantees
// both writers produce an identical, complete set.
//
// Returns a fresh Hooks each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
//
// The return value is the RESOLVED MODEL (managed_hooks.go), not a wire config:
// it keeps each hook's provenance and declared position, which the pure-append
// merge used to discard at every step, and it is what any project-level hook
// ORDERING has to act on. Writers take the projection, Hooks.Wire, which
// is byte-for-byte the wire config this function used to return.
func Assemble(rep report.Reporter, cfg *config.Config, workDir, contextHash string, profileNames []string) *Hooks {
	if cfg == nil {
		return newHooks()
	}
	return AssembleFor(rep, cfg, workDir, contextHash, cfg.ResolveProfileSet(profileNames))
}

// AssembleFor is Assemble over an already resolved
// profile set — the one assembly resolved, so its faults are reported once.
func AssembleFor(rep report.Reporter, cfg *config.Config, workDir, contextHash string, set []profiles.ResolvedProfile) *Hooks {
	hooks := newHooks()
	if cfg == nil {
		return hooks
	}
	// Selected-profile-shipped hooks. A profile's directly-declared hooks
	// pass the executable trust gate first — the SAME gate bundle hooks pass
	// — since the profile may be remote-sourced. There is no ungated arm:
	// every declared hook is evaluated.
	gate := cfg.ExecutableTrustGate()
	for i := range set {
		resolved := &set[i]
		profileName := resolved.Name
		gated := gateProfileHooks(profileGateRefFor(cfg, resolved, profileName), resolved.Hooks, gate)
		// Ref carries the ORIGIN BUNDLE for a bundle-shipped profile (empty for
		// a genuinely local one) — the same distinction the gate keys on, so the
		// report names the bundle a remote-sourced profile came from rather than
		// only the ref a user pasted into their agent's profile list.
		hooks.mergeHooks(gated, fixedSource(Source{
			Origin:  OriginProfileDirectory,
			Profile: profileName,
			Ref:     resolved.SourceRef,
		}))
	}
	// Bundle-shipped hooks + (optional) the context-injection hook.
	appendManagedDynamicHooks(rep, hooks, cfg, workDir, contextHash, set)
	return hooks
}

// appendManagedDynamicHooks appends the ctxloom-managed hooks that are assembled
// dynamically (rather than read verbatim from one config block): the
// bundle-shipped hooks (SCM-tagged — e.g. `session bind`, `stamp-plan`) and,
// when contextHash is non-empty, the SessionStart context-injection hook. The
// `ctxloom run` path passes contextHash "" here and lets the agent append its
// own injection hook from the plugin-side hash; apply-hooks passes the hash.
//
// The bundle set arrives FLAT — builtins, companion loadouts, and each selected
// profile's bundles in one slice — so it is attributed per hook off the marker
// config.extractHooksFromBundle stamped (bundleSource), not from this call site.
func appendManagedDynamicHooks(rep report.Reporter, m *Hooks, cfg *config.Config, workDir, contextHash string, set []profiles.ResolvedProfile) {
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
	m.mergeUnified(
		wire.UnifiedHooks{TurnStart: []wire.Hook{agent.NewMailDrainHook()}},
		fixedSource(Source{Origin: OriginContext}))
	if contextHash != "" {
		m.mergeUnified(
			wire.UnifiedHooks{SessionStart: agent.NewContextInjectionHooks(rep, contextHash, workDir)},
			fixedSource(Source{Origin: OriginContext}))
	}
}

// profileGateRef is the identity gateProfileHooks keys the
// executable trust gate by — the profile's own SOURCE, never its display
// name (a display name is neither honestly local nor a parseable trust
// ref). Base is the ref the gate composes "#<kind>/<name>" onto; Signer is
// the origin bundle's verified publisher identity when known (B2, gateProfileExec parity with
// bundle-declared execs) — empty falls through to local (a genuinely local
// profile) or pending review, never auto-allow.
type profileGateRef struct {
	Base string
	// Read is the trust posture the decision keys on: the ORIGIN BUNDLE's read
	// for a bundle-shipped profile, or the project's own posture for a
	// genuinely project-authored one (bundles.ProjectAuthoredRead).
	//
	// A verified principal string alone cannot say whether the signature still
	// covers the bytes, and an empty one means BOTH "unsigned" and "signed by a
	// key we do not trust" — so this carries the read's own axes instead. An
	// unresolvable origin leaves it UNCLAIMED, which every Authorizer
	// withholds: fail-closed.
	Read bundles.BundleRead
}

// profileGateRefFor derives a directory profile's gate identity from its
// resolved provenance: resolved.SourceRef (profiles.ResolvedProfile) when the
// profile is bundle-shipped — the origin bundle's canonical ref, WITHOUT the
// "#profiles/<name>" selector, so the composed "<SourceRef>#hooks/..." ref
// carries exactly one '#' and parses — and never keys IsLocal for a
// remote origin. A genuinely local/
// project-authored profile has an empty SourceRef, so Base falls back to the
// bare profileName — exactly what parseSourceRef's bare-token fallback
// resolves to IsLocal, honestly, because it IS local.
func profileGateRefFor(cfg *config.Config, resolved *profiles.ResolvedProfile, profileName string) profileGateRef {
	if resolved == nil || resolved.SourceRef == "" {
		// Genuinely project-authored: a .ctxloom/profiles/<name>.yaml file in
		// this project's own tree. That posture is stated out loud now — it used
		// to be asserted by handing the gate a bare-token ref and letting the ref
		// grammar resolve it to IsLocal, which is the same claim made where
		// nothing could see it.
		return profileGateRef{Base: profileName, Read: bundles.ProjectAuthoredRead(profileName, &bundles.Bundle{Name: profileName})}
	}
	ref := profileGateRef{Base: resolved.SourceRef}
	if cfg != nil {
		// The ORIGIN BUNDLE's own read, from the loader that read it — not a
		// posture this call site invents. An origin that will not resolve leaves
		// the read unclaimed, and an unclaimed read withholds.
		if read, err := cfg.BundleLoader().Read(resolved.SourceRef); err == nil {
			ref.Read = read
		}
	}
	return ref
}

// gateProfileHooks returns the hooks of a directory-resolved profile that the
// executable trust gate allows. Each hook is keyed on itemRefFor(ref.Base,
// trust.KindHook, "<event>/<index>") (the SAME identity scheme bundle hooks
// use, bundles.HookEntry) with
// its executable-surface hash; a DENY omits it (fail-closed). An ungated
// authorizer (a listing that named composite.Ungated) admits everything
// unchanged.
func gateProfileHooks(ref profileGateRef, h wire.HooksConfig, gate bundles.Authorizer) wire.HooksConfig {
	if !bundles.Gates(gate) {
		return h
	}
	keep := func(event string, hooks []wire.Hook) []wire.Hook {
		var out []wire.Hook
		for i, hook := range hooks {
			hookRef, err := itemRefFor(ref.Base, trust.KindHook, event+"/"+strconv.Itoa(i))
			if err != nil {
				clidiag.Warn("ctxloom", "profile hook %q withheld: %v", hook.Command, err)
				continue
			}
			if gateProfileExec(gate, ref, hookRef, hookExecPayload(hook)) {
				out = append(out, hook)
			} else {
				// Same fail-closed-but-diagnosable shape as gateProfileHooks's
				// warn — the gate's decision is unchanged.
				clidiag.Warn("ctxloom", "profile hook %q withheld by trust gate (%s); its executable is pending review", hook.Command, hookRef)
			}
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
		},
	}
	// Engine-native (ext) hooks gate too; keyed on
	// itemRefFor(ref.Base, trust.KindHook, "<engine>/<event>/<index>").
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

// gateProfileExec consults the executable trust filter for one directly-declared
// profile executable, binding the raw form (no distilled variant for
// executables, matching config.extractMCPFromBundle / extractHooksFromBundle).
//
// The POSTURE comes from ref.Read — the origin bundle's own read for a
// bundle-shipped profile, the project's for a project-authored one. That is
// parity with bundle-declared execs, which are decided on their document's own
// read: without it, a trusted publisher's profile would send its inline
// hooks/mcp to manual review even when the publisher key is already trusted.
//
// A nil payload (the preimage could not be built) withholds: an executable we
// cannot even describe is one we certainly cannot justify running.
func gateProfileExec(gate bundles.Authorizer, ref profileGateRef, itemRef string, payload []byte) bool {
	if payload == nil {
		return false
	}
	return bundles.Decide(report.To(strictness.Sink("ctxloom")), gate, ref.Read, itemRef, payload, bundles.FormRaw).Allow
}

// hookExecPayload builds a profile hook's executable-surface preimage via the
// shared bundle primitive (Matcher+Type+Command+Prompt+PreToolFallback), so a
// profile-declared hook and an identical bundle-declared one bind to exactly the
// SAME bytes. nil on an (unreachable) encoding failure — see gateProfileExec.
func hookExecPayload(h wire.Hook) []byte {
	bh := bundles.BundleHook{Matcher: h.Matcher, Command: h.Command, Type: h.Type, Prompt: h.Prompt, PreToolFallback: h.PreToolFallback}
	payload, err := bh.ContentPayload()
	if err != nil {
		return nil
	}
	return payload
}
