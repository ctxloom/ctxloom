package config

import (
	"maps"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/resources"
)

// lookPath is the PATH-resolution seam for tests.
var lookPath = exec.LookPath

// SetExecutableTrustGate injects the trust gate consulted when resolving the
// bundle executable surfaces — bundle MCP servers (ResolveBundleMCPServers),
// bundle hooks (ResolveBundleHooks), and prompt command-file exports
// (backends.LoadSkillExports). The operations/run consumers set it before
// writing backend settings (trust rework, TR5) so an untrusted bundle's
// executables are omitted; management/listing paths leave it nil (no gating).
// Builtin bundles are always exempt regardless of the gate.
func (c *Config) SetExecutableTrustGate(gate bundles.ContentGate) {
	c.execGate = gate
}

// ExecutableTrustGate returns the injected executable trust gate (nil when none
// was set — no gating).
func (c *Config) ExecutableTrustGate() bundles.ContentGate {
	return c.execGate
}

// SetLookPathForTesting overrides the companion-binary PATH-resolution seam and
// returns a restore function. Tests in other packages use it to make companion
// detection (built-in bundle hooks/MCP/fragments) deterministic regardless of
// what is installed on the developer's machine.
func SetLookPathForTesting(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}

// missingWarned tracks binaries already warned about, so a missing companion
// produces one hint per process, not one per resolve pass.
var (
	missingWarnedMu sync.Mutex
	missingWarned   = map[string]bool{}
)

// missingCompanion reports whether the command's executable is a companion
// binary absent from PATH. Commands run by ctxloom itself (`ctxloom hook ...`)
// always pass — gating exists for foreign binaries shipped separately
// (taskloom, ltk), whose builtin-bundle entries must degrade to absent rather
// than register a broken server or hook.
func missingCompanion(command string) (string, bool) {
	bin := companionBin(command)
	if bin == "" {
		return "", false
	}
	if _, err := lookPath(bin); err != nil {
		return bin, true
	}
	return "", false
}

// companionBin extracts the companion executable a command invokes: its first
// field, unless the command is empty or runs ctxloom itself.
func companionBin(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] == "ctxloom" {
		return ""
	}
	return fields[0]
}

// warnMissingCompanion emits the one-shot install hint for an absent binary.
func warnMissingCompanion(bin, hint string) {
	missingWarnedMu.Lock()
	defer missingWarnedMu.Unlock()
	if missingWarned[bin] {
		return
	}
	missingWarned[bin] = true
	msg := bin + " not found on PATH; its tools are disabled for this session"
	if hint != "" {
		msg += " (" + strings.TrimSpace(hint) + ")"
	}
	clidiag.Warn("ctxloom", "%s", msg)
}

// ResolveBundleMCPServers loads MCP servers from bundles referenced in the
// caller's selected profiles (or the configured defaults when none are passed),
// plus servers shipped by built-in bundles embedded in the binary
// (resources/builtin_bundles). Mirrors ResolveBundleHooks: any future built-in
// that ships an MCP server is picked up automatically, tagged with
// SCM="bundle:builtin:<name>" so reconciliation can identify it.
func (c *Config) ResolveBundleMCPServers(profileNames []string) map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality and aren't gated on profile membership. Run them
	// first so profile-sourced servers can intentionally override.
	maps.Copy(result, resolveBuiltinBundleMCPServers())

	// Scope to the caller's selected profiles (e.g. `run -p`); when none are
	// passed, fall back to the configured defaults so the `manage`/apply-hooks
	// path keeps its project-default behavior.
	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) == 0 {
		return result
	}

	// Get the base .ctxloom directory
	if len(c.AppPaths) == 0 {
		return result
	}

	// Load each profile and collect MCP servers.
	// SeededBundleLoader includes remote bundles from the active lockfile;
	// without it, MCP servers shipped in remote bundles silently disappear
	// after extraction is removed (see docs/bundle-review-plan.md Phase 1.2).
	profileLoader := c.GetProfileLoader()
	bundleLoader := c.SeededBundleLoader(false)

	for _, profileName := range profiles {
		// Resolve through the recursive resolver so bundles inherited from
		// parent profiles are included — a flat Load would only see this
		// profile's direct Bundles, silently dropping MCP servers shipped by
		// an inherited bundle (while the fragment path, which resolves
		// recursively, still picks them up). See ResolveBundleHooks for the
		// matching pattern.
		resolved, err := profileLoader.ResolveProfile(profileName, nil)
		if err != nil {
			continue
		}

		// Process each bundle URL in the resolved profile, honoring the
		// profile's exclude_mcp list (same name-based filter the inline
		// config-profile path applies in profileBuilder.toProfile).
		excluded := make(map[string]bool, len(resolved.ExcludeMCP))
		for _, name := range resolved.ExcludeMCP {
			excluded[name] = true
		}
		for _, bundleRef := range resolved.Bundles {
			servers := loadMCPFromBundleRef(bundleRef, bundleLoader, c.execGate)
			for name, server := range servers {
				if excluded[name] {
					continue
				}
				result[name] = server
			}
		}
	}

	return result
}

// resolveBuiltinBundleMCPServers parses every YAML under
// resources/builtin_bundles/ (embedded at build time) and returns the
// merged MCP-server map. Mirrors resolveBuiltinBundleHooks. Each server
// is tagged with SCM="bundle:builtin:<name>" so apply-* reconciliation can
// identify built-in entries. Failures on individual bundles are logged
// to stderr and skipped — built-in bundles must never block startup.
func resolveBuiltinBundleMCPServers() map[string]wire.MCPServer {
	out := make(map[string]wire.MCPServer)
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		clidiag.Warn("ctxloom", "list builtin bundles: %v", err)
		return out
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			clidiag.Warn("ctxloom", "read builtin bundle %q: %v", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			clidiag.Warn("ctxloom", "parse builtin bundle %q: %v", name, err)
			continue
		}
		// Builtin bundles are in-binary and never pass the trust resolver — gate
		// nil (they ship with ctxloom; trusting the binary trusts them). This
		// mirrors the baseline, which also excludes builtins.
		for serverName, server := range extractMCPFromBundle(&b, "builtin:"+name, nil) {
			// Builtin bundles wire in standalone companion binaries; a
			// missing one degrades to no entry (and one install hint)
			// rather than a broken server in every backend.
			if bin, missing := missingCompanion(server.Command); missing {
				warnMissingCompanion(bin, server.Installation)
				continue
			}
			out[serverName] = server
		}
	}
	return out
}

// loadMCPFromBundleRef loads MCP servers from a bundle reference (remote
// "remote/name" or local name). It resolves through loader.Load, which checks
// the seeded-bundle map first: remote bundles are no longer extracted to disk
// (they live only in the SeededBundleLoader seed), so resolving a remote ref by
// a computed filesystem path would silently find nothing and drop its servers.
func loadMCPFromBundleRef(bundleRef string, loader *bundles.Loader, gate bundles.ContentGate) map[string]wire.MCPServer {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		return nil
	}
	return extractMCPFromBundle(bundle, bundleRef, gate)
}

// ResolveBundleHooks aggregates hooks shipped by every bundle referenced
// in the caller's selected profiles (or the configured defaults when none
// are passed), plus the always-on hooks shipped by built-in bundles embedded
// in the binary (resources/builtin_bundles). Mirrors ResolveBundleMCPServers.
// Each emitted hook carries SCM source info so apply-hooks can identify
// ctxloom-managed entries when reconciling the backend's settings.json.
func (c *Config) ResolveBundleHooks(profileNames []string) wire.UnifiedHooks {
	var result wire.UnifiedHooks

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality (session bind, plan-stamping). No profile
	// gating, no remote pull.
	result.Append(resolveBuiltinBundleHooks())

	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) == 0 || len(c.AppPaths) == 0 {
		return result
	}
	profileLoader := c.GetProfileLoader()
	bundleLoader := c.SeededBundleLoader(false)

	for _, profileName := range profiles {
		// Resolve recursively so hooks shipped by bundles inherited from
		// parent profiles are included (matches ResolveBundleMCPServers and
		// the fragment resolution path); a flat Load would drop them.
		resolved, err := profileLoader.ResolveProfile(profileName, nil)
		if err != nil {
			continue
		}
		for _, bundleRef := range resolved.Bundles {
			hooks := loadHooksFromBundleRef(bundleRef, bundleLoader, c.execGate)
			result.Append(hooks)
		}
	}
	return result
}

// resolveProfileScope returns the profile set a bundle-resolution call should
// use: the caller's explicit selection (e.g. `run -p`) when non-empty, else the
// configured defaults. This is the seam that makes mcp/skills/hooks follow the
// SELECTED profile (the same set AssembleContext scopes context to) instead of
// always the defaults, while preserving the default-scoped behavior for the
// `manage`/apply-hooks path that passes nothing.
func (c *Config) resolveProfileScope(profileNames []string) []string {
	if len(profileNames) > 0 {
		return profileNames
	}
	return c.GetDefaultProfiles()
}

// ResolveBundleSkills aggregates the prompts (slash-command/skill exports)
// shipped by every bundle referenced in the caller's selected profiles (or the
// configured defaults when none are passed), in deterministic order, deduped by
// prompt name (first profile/bundle wins). Mirrors ResolveBundleMCPServers /
// ResolveBundleHooks — the profile-scoped replacement for the global
// ListAllSkills sweep, so a session only carries the skills its profile pulls
// in. Built-in embedded commands are added by the caller (LoadSkillExports),
// not here, since they are not bundle-shipped. The opts thread the executable
// trust gate (WithTrustGate) so a withheld skill is not exported.
func (c *Config) ResolveBundleSkills(profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedContent {
	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) == 0 || len(c.AppPaths) == 0 {
		return nil
	}
	profileLoader := c.GetProfileLoader()
	bundleLoader := c.SeededBundleLoader(c.ShouldUseDistilled(), opts...)

	seen := make(map[string]bool)
	var out []*bundles.LoadedContent
	for _, profileName := range profiles {
		resolved, err := profileLoader.ResolveProfile(profileName, nil)
		if err != nil {
			continue
		}
		for _, bundleRef := range resolved.Bundles {
			for _, prompt := range bundleLoader.SkillsFromBundleRef(bundleRef) {
				if seen[prompt.Item] {
					continue
				}
				seen[prompt.Item] = true
				out = append(out, prompt)
			}
		}
	}
	return out
}

// resolveBuiltinBundleHooks parses every YAML under
// resources/builtin_bundles/ (embedded at build time) and returns the
// merged hook set. Each hook is tagged with SCM="bundle:builtin:<name>" so the
// apply-hooks reconciliation can identify built-in entries. Failures on
// individual bundles are logged to stderr and skipped — built-in bundles
// must never block startup.
func resolveBuiltinBundleHooks() wire.UnifiedHooks {
	var out wire.UnifiedHooks
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		clidiag.Warn("ctxloom", "list builtin bundles: %v", err)
		return out
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			clidiag.Warn("ctxloom", "read builtin bundle %q: %v", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			clidiag.Warn("ctxloom", "parse builtin bundle %q: %v", name, err)
			continue
		}
		// Builtin bundles are in-binary and exempt from the trust gate (nil) —
		// they ship with ctxloom and the baseline excludes them.
		out.Append(filterMissingCompanionHooks(extractHooksFromBundle(&b, "builtin:"+name, nil)))
	}
	return out
}

// filterMissingCompanionHooks drops hooks whose executable is a companion
// binary absent from PATH (one install hint per binary). ctxloom's own hooks
// always pass; see missingCompanion.
func filterMissingCompanionHooks(in wire.UnifiedHooks) wire.UnifiedHooks {
	keep := func(hooks []wire.Hook) []wire.Hook {
		var out []wire.Hook
		for _, h := range hooks {
			if bin, missing := missingCompanion(h.Command); missing {
				warnMissingCompanion(bin, "")
				continue
			}
			out = append(out, h)
		}
		return out
	}
	return wire.UnifiedHooks{
		PreTool:      keep(in.PreTool),
		PostTool:     keep(in.PostTool),
		SessionStart: keep(in.SessionStart),
		SessionEnd:   keep(in.SessionEnd),
		PreShell:     keep(in.PreShell),
		PostFileEdit: keep(in.PostFileEdit),
	}
}

// BuiltinFragment is one always-on fragment shipped by a built-in bundle,
// ready for unconditional injection into assembled context.
type BuiltinFragment struct {
	Name         string // reporting identity ("builtin:<bundle>#fragments/<name>")
	Content      string
	Installation string
}

// ResolveBuiltinBundleFragments returns the fragments shipped by built-in
// bundles embedded in the binary (resources/builtin_bundles), for unconditional
// injection into assembled context — the always-on counterpart to
// ResolveBundleHooks / ResolveBundleMCPServers, which wire in those same
// bundles' hooks and MCP servers. A bundle whose companion binary (the
// executable its hooks/MCP invoke, e.g. ltk, taskloom) is absent from PATH is
// skipped: with the tool not installed, briefing the agent about it is noise,
// and its hooks/MCP are skipped for the same reason. ctxloom-only built-in
// bundles have no companion and always inject. Order is deterministic (bundle
// name, then fragment name) for a stable context hash. The missing-companion
// install hint is emitted by the hooks/MCP path; this resolver stays silent.
func (c *Config) ResolveBuiltinBundleFragments() []BuiltinFragment {
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		clidiag.Warn("ctxloom", "list builtin bundles: %v", err)
		return nil
	}
	var out []BuiltinFragment
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			clidiag.Warn("ctxloom", "read builtin bundle %q: %v", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			clidiag.Warn("ctxloom", "parse builtin bundle %q: %v", name, err)
			continue
		}
		if _, missing := builtinBundleCompanionMissing(&b); missing {
			continue
		}
		fragNames := make([]string, 0, len(b.Fragments))
		for fragName := range b.Fragments {
			fragNames = append(fragNames, fragName)
		}
		sort.Strings(fragNames)
		for _, fragName := range fragNames {
			frag := b.Fragments[fragName]
			content := frag.EffectiveContent(c.ShouldUseDistilled())
			if strings.TrimSpace(content) == "" {
				continue
			}
			out = append(out, BuiltinFragment{
				Name:         "builtin:" + name + "#fragments/" + fragName,
				Content:      content,
				Installation: frag.Installation,
			})
		}
	}
	return out
}

// builtinBundleCompanionMissing reports the companion binary a built-in bundle
// depends on (the first executable its hooks or MCP servers invoke that isn't
// ctxloom itself) and whether it is absent from PATH. A bundle with no companion
// returns ("", false). Mirrors the gating applied to the bundle's hooks/MCP.
func builtinBundleCompanionMissing(b *bundles.Bundle) (string, bool) {
	for _, hs := range [][]bundles.BundleHook{
		b.Hooks.PreTool, b.Hooks.PostTool, b.Hooks.SessionStart,
		b.Hooks.SessionEnd, b.Hooks.PreShell, b.Hooks.PostFileEdit,
	} {
		for _, h := range hs {
			if bin, missing := missingCompanion(h.Command); missing {
				return bin, true
			}
		}
	}
	for _, m := range b.MCP {
		if bin, missing := missingCompanion(m.Command); missing {
			return bin, true
		}
	}
	return "", false
}

// loadHooksFromBundleRef loads hooks from a bundle reference. Like
// loadMCPFromBundleRef it resolves via loader.Load (seed-aware) rather than a
// computed fs path, so remote bundles' hooks aren't silently dropped.
func loadHooksFromBundleRef(bundleRef string, loader *bundles.Loader, gate bundles.ContentGate) wire.UnifiedHooks {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		return wire.UnifiedHooks{}
	}
	return extractHooksFromBundle(bundle, bundleRef, gate)
}

// extractHooksFromBundle converts a bundle's hooks to wire.Hooks. When gate is
// non-nil (the executable trust gate, TR5), each hook's executable surface is
// hashed (BundleHook.ComputeContentHash) and run through the cascade keyed
// "<bundle>#hooks/<event>/<index>"; a DENY omits the hook — a bundle hook is an
// arbitrary-command executable that must never be applied unevaluated
// (fail-closed). Builtin callers pass nil (in-binary, exempt). The identity
// scheme is bundles.HookEntry, shared with the migration baseline so a baselined
// hook's ref matches.
func extractHooksFromBundle(bundle *bundles.Bundle, source string, gate bundles.ContentGate) wire.UnifiedHooks {
	if !bundle.Hooks.HasAny() {
		return wire.UnifiedHooks{}
	}
	marker := "bundle:" + source
	convert := func(event string, in []bundles.BundleHook) []wire.Hook {
		if len(in) == 0 {
			return nil
		}
		out := make([]wire.Hook, 0, len(in))
		for i := range in {
			h := in[i]
			if gate != nil {
				ref := bundle.Name + "#hooks/" + bundles.HookEntry{Event: event, Index: i}.ID()
				if !gate(ref, h.ComputeContentHash(), string(bundles.FormRaw)) {
					continue // withheld by the trust gate
				}
			}
			out = append(out, wire.Hook{
				Matcher:         h.Matcher,
				Command:         h.Command,
				Type:            h.Type,
				Prompt:          h.Prompt,
				Timeout:         h.Timeout,
				Async:           h.Async,
				SCM:             marker,
				PreToolFallback: h.PreToolFallback,
			})
		}
		return out
	}
	return wire.UnifiedHooks{
		PreTool:      convert(bundles.HookEventPreTool, bundle.Hooks.PreTool),
		PostTool:     convert(bundles.HookEventPostTool, bundle.Hooks.PostTool),
		SessionStart: convert(bundles.HookEventSessionStart, bundle.Hooks.SessionStart),
		SessionEnd:   convert(bundles.HookEventSessionEnd, bundle.Hooks.SessionEnd),
		PreShell:     convert(bundles.HookEventPreShell, bundle.Hooks.PreShell),
		PostFileEdit: convert(bundles.HookEventPostFileEdit, bundle.Hooks.PostFileEdit),
	}
}

// extractMCPFromBundle extracts MCP servers from a loaded bundle. When gate is
// non-nil (the executable trust gate, TR5), each server's executable surface
// (Command+Args+Env+Installation) is hashed and run through the cascade keyed
// "<bundle>#mcp/<name>"; a DENY omits the server entirely — an arbitrary-command
// executable must never reach settings unevaluated (fail-closed). Builtin
// callers pass nil (in-binary, exempt).
func extractMCPFromBundle(bundle *bundles.Bundle, source string, gate bundles.ContentGate) map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	for name, mcp := range bundle.MCP {
		if gate != nil {
			ref := bundle.Name + "#mcp/" + name
			if !gate(ref, mcp.ComputeContentHash(), string(bundles.FormRaw)) {
				continue // withheld by the trust gate
			}
		}
		result[name] = wire.MCPServer{
			Command:      mcp.Command,
			Args:         mcp.Args,
			Env:          mcp.Env,
			Notes:        mcp.Notes,
			Installation: mcp.Installation,
			SCM:          "bundle:" + source, // Mark as coming from a bundle
		}
	}

	return result
}
