package backends

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/trust"
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
// <name>", a lockfile's "<url>@bundles/<path>") or a bare local bundle name,
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

// This file is the HOST side of the setup seam: ctxloom owns config and bundles
// here, resolves them into the wire-typed agent.ManagedConfig, and ships that to
// the backend over RunStart. The agent's Setup consumes only the result
// (BaseLifecycle.MergeManaged), so the launch backends never import
// config/bundles. The assembly that used to run plugin-side in each agent's
// Setup (config.Load + LoadPrompts + MergeConfigHooks) lives here now.

// AssembleManagedConfig resolves the host-side setup payload for one target
// backend: the slash-command exports (mapped to that backend's enablement +
// metadata), the config+default-profile+bundle hook set WITHOUT context-injection
// (the agent appends that itself from its plugin-side context hash), the merged
// config+default-profile MCP servers, and whether ctxloom manages the statusline.
//
// Fault tolerant per CLAUDE.md: a config load failure yields a nil payload — the
// agent's Setup then writes an empty managed set rather than blocking launch.
// The gate gates the executable surfaces (bundle MCP servers + hooks + prompt
// exports) for the `ctxloom run` setup payload (trust rework). It is built
// by the run command (which can reach operations.EffectiveTrust); attaching it
// to the loaded config flows it to ResolveBundleMCPServers / AssembleManagedHooks
// / LoadCommandExports. bundles.AdmitAll = deliberately no gating.
//
// profileNames is the run's SELECTED profile set (the same set AssembleContext
// scoped context to), so the managed mcp/commands/hooks track the chosen profile
// rather than always the configured defaults. An empty set falls back to the
// defaults inside each resolver (scopedProfiles / resolveProfileScope).
func AssembleManagedConfig(backendName, workDir string, gate bundles.Authorizer, profileNames []string) *agent.ManagedConfig {
	cfg, err := config.Load()
	if err != nil {
		// The agent's Setup writes an EMPTY managed set from a nil payload —
		// the reconciling writers then remove every previously-installed
		// ctxloom hook/command for this run. Degrading is right (never block
		// launch), but it must not be silent.
		clidiag.Warn("ctxloom", "config load failed; launching without managed hooks/commands: %v", err)
		return nil
	}
	cfg.SetExecutableTrustGate(gate)
	return &agent.ManagedConfig{
		Commands:         CommandExportsFor(backendName, LoadCommandExports(cfg, profileNames)),
		Skills:           SkillExportsFor(backendName, LoadSkillExports(cfg, profileNames)),
		Hooks:            AssembleManagedHooks(cfg, workDir, "", profileNames).Wire(),
		BundleMCP:        cfg.ResolveBundleMCPServers(profileNames),
		ManageStatusline: managedStatuslineEnabled(cfg),
		DenyTools:        AssembleManagedDenyTools(cfg, profileNames),
	}
}

// managedStatuslineEnabled reports whether ctxloom manages the HUD statusline,
// via the config accessor (not the raw cfg.Settings field — ShouldManageStatusline
// has a pointer receiver, so it needs a local, addressable copy of the
// accessor's return value).
func managedStatuslineEnabled(cfg *config.Config) bool {
	settings := cfg.GetSettings()
	return settings.ShouldManageStatusline()
}

// CommandExportsFor maps loaded bundle content to the named backend's command
// exports (resolving that backend's per-prompt enablement + metadata), or nil
// for a backend without slash-command export. Reads the descriptor table's
// exports field — the same mapper WriteCommandFilesFor uses — so the two
// paths can't diverge.
func CommandExportsFor(backendName string, prompts []*bundles.LoadedContent) []agent.CommandExport {
	d, ok := lookup(backendName)
	if !ok || d.exports == nil {
		return nil
	}
	return d.exports(prompts)
}

// SkillExportsFor maps loaded bundle skills to the named backend's Agent
// Skill package exports (resolving that backend's per-skill enablement), or
// nil for a backend without skill export. Reads the descriptor table's
// skillExports field — the skills-surface analog of CommandExportsFor.
func SkillExportsFor(backendName string, skills []*bundles.LoadedSkill) []agent.SkillExport {
	d, ok := lookup(backendName)
	if !ok || d.skillExports == nil {
		return nil
	}
	return d.skillExports(skills)
}

// AssembleManagedDenyTools builds the union of deny_tools declared by the
// config's default profile / the caller's selected profiles: config.yaml
// inline profiles (config.ResolveProfile) or, when a name isn't inline, a
// directory profile (cfg.GetProfileLoader().ResolveProfile) — the SAME
// two-source resolution AssembleManagedHooks uses. Order is
// deterministic (first-seen wins position) and entries dedup case-sensitively
// on the exact tool identifier string.
//
// Unlike MCP servers and hooks, a deny_tools entry is never passed through
// the executable trust gate: it names a tool identifier to BLOCK, not an
// executable to run, so even a remote-sourced directory profile's
// directly-declared deny_tools is safe to apply unconditionally — it can
// only narrow what a launch may do.
func AssembleManagedDenyTools(cfg *config.Config, profileNames []string) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(tools []string) {
		for _, t := range tools {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		// Inline profile (config.yaml) wins, trusted-local (ungated) — see the
		// same-shaped loop in AssembleManagedHooks, including the real-vs-
		// not-found error distinction.
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			add(inlineResolved.DenyTools)
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom", "profile %q: inline resolution failed; its deny_tools omitted: %v", profileName, inlineErr)
			continue
		}
		// Directory profile fallback.
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "profile %q unresolved; its deny_tools omitted: %v", profileName, err)
			continue
		}
		add(resolved.DenyTools)
	}
	return out
}

// scopedProfiles returns the caller's selected profiles, or the default agent's
// composed profiles when none are passed — the host-side mirror of
// config.resolveProfileScope (MUST stay byte-identical to it), so the
// managed-config assembly scopes to the SAME set the bundle resolvers do.
func scopedProfiles(cfg *config.Config, profileNames []string) []string {
	if len(profileNames) > 0 {
		return profileNames
	}
	return cfg.DefaultAgentProfiles()
}

// AssembleManagedHooks builds the COMPLETE ctxloom-managed hook set that every
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
// Returns a fresh ManagedHooks each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
//
// The return value is the RESOLVED MODEL (managed_hooks.go), not a wire config:
// it keeps each hook's provenance and declared position, which the pure-append
// merge used to discard at every step, and it is what any project-level hook
// ORDERING has to act on. Writers take the projection, ManagedHooks.Wire, which
// is byte-for-byte the wire config this function used to return.
func AssembleManagedHooks(cfg *config.Config, workDir, contextHash string, profileNames []string) *ManagedHooks {
	hooks := newManagedHooks()
	if cfg == nil {
		return hooks
	}
	// Selected-profile-shipped hooks (defaults when none are passed). A profile
	// resolves the SAME way operations.resolveProfile / AssembleManagedDenyTools do:
	// inline definitions (config.yaml) win and are trusted-local (ungated); a name
	// that isn't inline falls back to a directory profile, whose directly-declared
	// hooks pass the executable trust gate first (the SAME gate bundle hooks pass)
	// since the profile may be remote-sourced — so an inline hooks: block reaches
	// the managed set with parity to an inline profile, but never unevaluated.
	gate := cfg.ExecutableTrustGate()
	profiles := scopedProfiles(cfg, profileNames)
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range profiles {
		// Same real-vs-not-found error distinction as AssembleManagedDenyTools's loop:
		// a broken inline profile must not be silently retried as a
		// directory profile.
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			hooks.mergeHooks(inlineResolved.Hooks,
				fixedSource(HookSource{Origin: HookOriginProfileInline, Profile: profileName}))
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom", "profile %q: inline resolution failed; its hooks omitted: %v", profileName, inlineErr)
			continue
		}
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "profile %q unresolved; its hooks omitted: %v", profileName, err)
			continue
		}
		gated := gateProfileHooks(profileGateRefFor(cfg, resolved, profileName), resolved.Hooks, gate)
		// Ref carries the ORIGIN BUNDLE for a bundle-shipped profile (empty for
		// a genuinely local one) — the same distinction the gate keys on, so the
		// report names the bundle a remote-sourced profile came from rather than
		// only the ref a user pasted into their agent's profile list.
		hooks.mergeHooks(gated, fixedSource(HookSource{
			Origin:  HookOriginProfileDirectory,
			Profile: profileName,
			Ref:     resolved.SourceRef,
		}))
	}
	// Bundle-shipped hooks + (optional) the context-injection hook.
	appendManagedDynamicHooks(hooks, cfg, workDir, contextHash, profiles)
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
func appendManagedDynamicHooks(m *ManagedHooks, cfg *config.Config, workDir, contextHash string, profileNames []string) {
	if m == nil || cfg == nil {
		return
	}
	m.mergeUnified(cfg.ResolveBundleHooks(profileNames), bundleSource)
	if contextHash != "" {
		m.mergeUnified(
			wire.UnifiedHooks{SessionStart: agent.NewContextInjectionHooks(contextHash, workDir)},
			fixedSource(HookSource{Origin: HookOriginContext}))
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
// its executable-surface hash; a DENY omits it (fail-closed). An AdmitAll gate
// (management paths) admits everything unchanged.
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
		},
	}
	// Plugin-specific (backend-native) hooks gate too; keyed on
	// itemRefFor(ref.Base, trust.KindHook, "<plugin>/<event>/<index>").
	if len(h.Plugins) > 0 {
		out.Plugins = make(map[string]wire.BackendHooks, len(h.Plugins))
		for plugin, backend := range h.Plugins {
			bh := make(wire.BackendHooks)
			for event, hooks := range backend {
				if kept := keep(plugin+"/"+event, hooks); len(kept) > 0 {
					bh[event] = kept
				}
			}
			if len(bh) > 0 {
				out.Plugins[plugin] = bh
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
	return bundles.Decide(gate, ref.Read, itemRef, payload, bundles.FormRaw).Allow
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
