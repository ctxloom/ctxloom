package backends

import (
	"errors"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

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
// / LoadCommandExports. nil = no gating.
//
// profileNames is the run's SELECTED profile set (the same set AssembleContext
// scoped context to), so the managed mcp/commands/hooks track the chosen profile
// rather than always the configured defaults. An empty set falls back to the
// defaults inside each resolver (scopedProfiles / resolveProfileScope).
func AssembleManagedConfig(backendName, workDir string, gate bundles.ContentGate, profileNames []string) *agent.ManagedConfig {
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
		MCP:              AssembleManagedMCP(cfg, profileNames),
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
	d, ok := descriptors[backendName]
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
	d, ok := descriptors[backendName]
	if !ok || d.skillExports == nil {
		return nil
	}
	return d.skillExports(skills)
}

// AssembleManagedMCP builds the merged MCP server set: config-level servers then
// each default profile's servers (later wins). This is the MCP half of the old
// BaseLifecycle.MergeConfigHooks, lifted host-side now that ctxloom owns config
// resolution.
//
// A default profile resolves the SAME way operations.resolveProfile does:
// inline definitions (config.yaml profiles: map) win, and a name that isn't an
// inline profile falls back to a directory profile (.ctxloom/profiles/<name>.yaml
// via the profile loader) — so a directory profile's inline mcp: servers reach
// the managed set, parity with an inline profile. An inline profile's servers are
// trusted-local config (ungated); a directory profile may be remote-sourced, so
// ITS directly-declared servers pass the executable trust gate first (the SAME
// gate bundle MCP servers pass), never reaching settings unevaluated.
func AssembleManagedMCP(cfg *config.Config, profileNames []string) *wire.MCPConfig {
	mcp := &wire.MCPConfig{
		Servers: make(map[string]wire.MCPServer),
		Plugins: make(map[string]map[string]wire.MCPServer),
	}
	if cfg == nil {
		return mcp
	}
	// Config-level MCP is the project-wide baseline (always merged). The
	// per-profile inline MCP is scoped to the SELECTED profiles, falling back
	// to the configured defaults when none are passed.
	configMCP := cfg.GetMCPConfig()
	wire.MergeMCPConfig(mcp, &configMCP)
	gate := cfg.ExecutableTrustGate()
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		// Inline profile (config.yaml) wins, trusted-local (ungated). A non-nil
		// inlineErr is either "not an inline profile" (errs.ErrProfileNotFound —
		// fall through to the directory loader below, same as before) or a REAL
		// inline-profile defect (circular inheritance, depth exceeded) the
		// directory fallback cannot explain: this used to treat both cases
		// identically, so a broken inline profile's actual cause was
		// discarded in favor of the directory loader's unrelated not-found error.
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			wire.MergeMCPConfig(mcp, &inlineResolved.MCP)
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom", "profile %q: inline resolution failed; its MCP servers omitted: %v", profileName, inlineErr)
			continue
		}
		// Directory profile fallback: drop servers its exclude_mcp removes (parity
		// with the inline toProfile filter), then gate the directly-declared
		// servers before merging.
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "profile %q unresolved; its MCP servers omitted: %v", profileName, err)
			continue
		}
		gated := gateProfileMCP(profileGateRefFor(resolved, profileName), excludeMCPServers(resolved.MCP, resolved.ExcludeMCP), gate)
		wire.MergeMCPConfig(mcp, &gated)
	}
	return mcp
}

// AssembleManagedDenyTools builds the union of deny_tools declared by the
// config's default profile / the caller's selected profiles: config.yaml
// inline profiles (config.ResolveProfile) or, when a name isn't inline, a
// directory profile (cfg.GetProfileLoader().ResolveProfile) — the SAME
// two-source resolution AssembleManagedMCP/AssembleManagedHooks use. Order is
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
		// same-shaped loop in AssembleManagedMCP, including the real-vs-
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
	// Config-level hooks.
	hooks.mergeHooks(cfg.GetHooksConfig(), fixedSource(HookSource{Origin: HookOriginConfig}))
	// Selected-profile-shipped hooks (defaults when none are passed). A profile
	// resolves the SAME way operations.resolveProfile / AssembleManagedMCP do:
	// inline definitions (config.yaml) win and are trusted-local (ungated); a name
	// that isn't inline falls back to a directory profile, whose directly-declared
	// hooks pass the executable trust gate first (the SAME gate bundle hooks pass)
	// since the profile may be remote-sourced — so an inline hooks: block reaches
	// the managed set with parity to an inline profile, but never unevaluated.
	gate := cfg.ExecutableTrustGate()
	profiles := scopedProfiles(cfg, profileNames)
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range profiles {
		// Same real-vs-not-found error distinction as AssembleManagedMCP's loop:
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
		gated := gateProfileHooks(profileGateRefFor(resolved, profileName), resolved.Hooks, gate)
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

// excludeMCPServers drops the named unified servers from a directory profile's
// MCP (its exclude_mcp list), mirroring the inline profileBuilder.toProfile
// filter so the directory path honors exclude_mcp on its own declared servers
// exactly like the inline path. Backend-passthrough Plugins are left untouched,
// matching the inline filter's Servers-only scope.
func excludeMCPServers(mcp wire.MCPConfig, exclude []string) wire.MCPConfig {
	if len(exclude) == 0 || len(mcp.Servers) == 0 {
		return mcp
	}
	excluded := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		excluded[n] = true
	}
	out := mcp
	out.Servers = make(map[string]wire.MCPServer, len(mcp.Servers))
	for name, srv := range mcp.Servers {
		if excluded[name] {
			continue
		}
		out.Servers[name] = srv
	}
	return out
}

// profileGateRef is the identity gateProfileMCP/gateProfileHooks key the
// executable trust gate by — the profile's own SOURCE, never its display
// name (a display name is neither honestly local nor a parseable trust
// ref). Base is the ref the gate composes "#<kind>/<name>" onto; Signer is
// the origin bundle's verified publisher identity when known (B2, gateProfileExec parity with
// bundle-declared execs) — empty falls through to local (a genuinely local
// profile) or pending review, never auto-allow.
type profileGateRef struct {
	Base   string
	Signer string
}

// profileGateRefFor derives a directory profile's gate identity from its
// resolved provenance: resolved.SourceRef (profiles.ResolvedProfile) when the
// profile is bundle-shipped — the origin bundle's canonical ref, WITHOUT the
// "#profiles/<name>" selector, so the composed "<SourceRef>#hooks/..." ref
// carries exactly one '#' and parses — and never keys IsLocal for a
// remote origin. A genuinely local/
// project-authored profile has an empty SourceRef, so Base falls back to the
// bare profileName — exactly what trust.ParseItemRef's bare-token fallback
// resolves to IsLocal, honestly, because it IS local.
func profileGateRefFor(resolved *profiles.ResolvedProfile, profileName string) profileGateRef {
	if resolved == nil || resolved.SourceRef == "" {
		return profileGateRef{Base: profileName}
	}
	return profileGateRef{Base: resolved.SourceRef, Signer: resolved.Signer}
}

// gateProfileMCP returns the MCP servers of a directory-resolved profile that the
// executable trust gate allows. A directory profile may be remote-sourced, so its
// directly-declared MCP servers — unlike a trusted-local config.yaml inline
// profile's — pass the SAME per-item executable gate as bundle MCP servers
// (config.extractMCPFromBundle), keyed "<ref.Base>#mcp/<name>" with the server's
// executable-surface hash. A DENY omits the server (fail-closed). A nil gate
// (management paths) admits everything unchanged.
func gateProfileMCP(ref profileGateRef, mcp wire.MCPConfig, gate bundles.ContentGate) wire.MCPConfig {
	if gate == nil {
		return mcp
	}
	out := wire.MCPConfig{AutoRegisterCtxloom: mcp.AutoRegisterCtxloom}
	if len(mcp.Servers) > 0 {
		out.Servers = make(map[string]wire.MCPServer, len(mcp.Servers))
		for name, srv := range mcp.Servers {
			itemRef := ref.Base + "#mcp/" + name
			if gateProfileExec(gate, itemRef, mcpExecPayload(srv), ref.Signer) {
				out.Servers[name] = srv
			} else {
				// Fail-closed is right, fail-SILENT is not — the gate's
				// decision is unchanged, this only makes the withhold
				// diagnosable.
				clidiag.Warn("ctxloom", "profile MCP server %q withheld by trust gate (%s); its executable is pending review", name, itemRef)
			}
		}
	}
	// Plugin-specific (backend-passthrough) servers gate too — an arbitrary-command
	// executable must never bypass the gate; keyed "<ref.Base>#mcp/<backend>/<name>".
	if len(mcp.Plugins) > 0 {
		out.Plugins = make(map[string]map[string]wire.MCPServer, len(mcp.Plugins))
		for backend, servers := range mcp.Plugins {
			gated := make(map[string]wire.MCPServer)
			for name, srv := range servers {
				itemRef := ref.Base + "#mcp/" + backend + "/" + name
				if gateProfileExec(gate, itemRef, mcpExecPayload(srv), ref.Signer) {
					gated[name] = srv
				} else {
					clidiag.Warn("ctxloom", "profile MCP server %q (backend %s) withheld by trust gate (%s); its executable is pending review", name, backend, itemRef)
				}
			}
			if len(gated) > 0 {
				out.Plugins[backend] = gated
			}
		}
	}
	return out
}

// gateProfileHooks returns the hooks of a directory-resolved profile that the
// executable trust gate allows. Each hook is keyed "<ref.Base>#hooks/<event>/
// <index>" (the SAME identity scheme bundle hooks use, bundles.HookEntry) with
// its executable-surface hash; a DENY omits it (fail-closed). A nil gate
// (management paths) admits everything unchanged.
func gateProfileHooks(ref profileGateRef, h wire.HooksConfig, gate bundles.ContentGate) wire.HooksConfig {
	if gate == nil {
		return h
	}
	keep := func(event string, hooks []wire.Hook) []wire.Hook {
		var out []wire.Hook
		for i, hook := range hooks {
			hookRef := ref.Base + "#hooks/" + event + "/" + strconv.Itoa(i)
			if gateProfileExec(gate, hookRef, hookExecPayload(hook), ref.Signer) {
				out = append(out, hook)
			} else {
				// Same fail-closed-but-diagnosable shape as gateProfileMCP's
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
	// Plugin-specific (backend-native) hooks gate too; keyed
	// "<ref.Base>#hooks/<plugin>/<event>/<index>".
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

// gateProfileExec consults the executable trust gate for one directly-declared
// profile executable, binding the raw form (no distilled variant for executables,
// matching config.extractMCPFromBundle / extractHooksFromBundle).
//
// signer is the origin bundle's VERIFIED publisher identity when the profile
// is bundle-shipped and that bundle is signed by a trusted key
// (profiles.ResolvedProfile.Signer, B2) — empty for a genuinely local profile
// (judged local at step 3) or an unsigned/untrusted bundle (falls through to
// pending review at step 7). This is parity with bundle-declared execs, which
// DO carry their document's verified signer: without it, keying gate refs by
// source alone would send every trusted-publisher profile's inline hooks/mcp
// to manual review even when the publisher key is already trusted — a
// usability regression the ref-keying fix alone would otherwise cause.
//
// A nil payload (the preimage could not be built) withholds: an executable we
// cannot even describe is one we certainly cannot justify running.
func gateProfileExec(gate bundles.ContentGate, ref string, payload []byte, signer string) bool {
	if payload == nil {
		return false
	}
	return gate(ref, payload, string(bundles.FormRaw), signer)
}

// mcpExecPayload builds a profile MCP server's executable-surface preimage via
// the shared bundle primitive (Command+Args+Env+Installation), so a
// profile-declared server and an identical bundle-declared one bind to exactly
// the SAME bytes. nil on an (unreachable) encoding failure — see gateProfileExec.
func mcpExecPayload(s wire.MCPServer) []byte {
	bm := bundles.BundleMCP{Command: s.Command, Args: s.Args, Env: s.Env, Installation: s.Installation}
	payload, err := bm.ContentPayload()
	if err != nil {
		return nil
	}
	return payload
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
