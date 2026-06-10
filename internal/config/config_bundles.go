package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/wire"
)

// ResolveBundleMCPServers loads MCP servers from bundles referenced in the
// default profiles, plus servers shipped by built-in bundles embedded in
// the binary (resources/builtin_bundles). Mirrors ResolveBundleHooks: any
// future built-in that ships an MCP server is picked up automatically,
// tagged with SCM="builtin:<name>" so reconciliation can identify it.
func (c *Config) ResolveBundleMCPServers() map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality and aren't gated on profile membership. Run them
	// first so profile-sourced servers can intentionally override.
	for name, server := range resolveBuiltinBundleMCPServers() {
		result[name] = server
	}

	defaultProfiles := c.GetDefaultProfiles()
	if len(defaultProfiles) == 0 {
		return result
	}

	// Get the base .ctxloom directory
	if len(c.AppPaths) == 0 {
		return result
	}

	// Load each default profile and collect MCP servers.
	// SeededBundleLoader includes remote bundles from the active lockfile;
	// without it, MCP servers shipped in remote bundles silently disappear
	// after extraction is removed (see docs/bundle-review-plan.md Phase 1.2).
	profileLoader := c.GetProfileLoader()
	bundleLoader := c.SeededBundleLoader(false)

	for _, defaultProfile := range defaultProfiles {
		// Resolve through the recursive resolver so bundles inherited from
		// parent profiles are included — a flat Load would only see this
		// profile's direct Bundles, silently dropping MCP servers shipped by
		// an inherited bundle (while the fragment path, which resolves
		// recursively, still picks them up). See ResolveBundleHooks for the
		// matching pattern.
		resolved, err := profileLoader.ResolveProfile(defaultProfile, nil)
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
			servers := loadMCPFromBundleRef(bundleRef, bundleLoader)
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
// is tagged with SCM="builtin:<name>" so apply-* reconciliation can
// identify built-in entries. Failures on individual bundles are logged
// to stderr and skipped — built-in bundles must never block startup.
func resolveBuiltinBundleMCPServers() map[string]wire.MCPServer {
	out := make(map[string]wire.MCPServer)
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: list builtin bundles: %v\n", err)
		return out
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: read builtin bundle %q: %v\n", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: parse builtin bundle %q: %v\n", name, err)
			continue
		}
		for serverName, server := range extractMCPFromBundle(&b, "builtin:"+name) {
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
func loadMCPFromBundleRef(bundleRef string, loader *bundles.Loader) map[string]wire.MCPServer {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		return nil
	}
	return extractMCPFromBundle(bundle, bundleRef)
}

// ResolveBundleHooks aggregates hooks shipped by every bundle referenced
// in the active default profiles, plus the always-on hooks shipped by
// built-in bundles embedded in the binary (resources/builtin_bundles).
// Mirrors ResolveBundleMCPServers. Each emitted hook carries SCM source
// info so apply-hooks can identify ctxloom-managed entries when
// reconciling the backend's settings.json.
func (c *Config) ResolveBundleHooks() wire.UnifiedHooks {
	var result wire.UnifiedHooks

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality (session bind, plan-stamping). No profile
	// gating, no remote pull.
	result.Append(resolveBuiltinBundleHooks())

	defaultProfiles := c.GetDefaultProfiles()
	if len(defaultProfiles) == 0 || len(c.AppPaths) == 0 {
		return result
	}
	profileLoader := c.GetProfileLoader()
	bundleLoader := c.SeededBundleLoader(false)

	for _, defaultProfile := range defaultProfiles {
		// Resolve recursively so hooks shipped by bundles inherited from
		// parent profiles are included (matches ResolveBundleMCPServers and
		// the fragment resolution path); a flat Load would drop them.
		resolved, err := profileLoader.ResolveProfile(defaultProfile, nil)
		if err != nil {
			continue
		}
		for _, bundleRef := range resolved.Bundles {
			hooks := loadHooksFromBundleRef(bundleRef, bundleLoader)
			result.Append(hooks)
		}
	}
	return result
}

// resolveBuiltinBundleHooks parses every YAML under
// resources/builtin_bundles/ (embedded at build time) and returns the
// merged hook set. Each hook is tagged with SCM="builtin:<name>" so the
// apply-hooks reconciliation can identify built-in entries. Failures on
// individual bundles are logged to stderr and skipped — built-in bundles
// must never block startup.
func resolveBuiltinBundleHooks() wire.UnifiedHooks {
	var out wire.UnifiedHooks
	names, err := resources.ListBuiltinBundles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: list builtin bundles: %v\n", err)
		return out
	}
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: read builtin bundle %q: %v\n", name, err)
			continue
		}
		var b bundles.Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: parse builtin bundle %q: %v\n", name, err)
			continue
		}
		out.Append(extractHooksFromBundle(&b, "builtin:"+name))
	}
	return out
}

// loadHooksFromBundleRef loads hooks from a bundle reference. Like
// loadMCPFromBundleRef it resolves via loader.Load (seed-aware) rather than a
// computed fs path, so remote bundles' hooks aren't silently dropped.
func loadHooksFromBundleRef(bundleRef string, loader *bundles.Loader) wire.UnifiedHooks {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		return wire.UnifiedHooks{}
	}
	return extractHooksFromBundle(bundle, bundleRef)
}

func extractHooksFromBundle(bundle *bundles.Bundle, source string) wire.UnifiedHooks {
	if !bundle.Hooks.HasAny() {
		return wire.UnifiedHooks{}
	}
	marker := "bundle:" + source
	convert := func(in []bundles.BundleHook) []wire.Hook {
		if len(in) == 0 {
			return nil
		}
		out := make([]wire.Hook, len(in))
		for i, h := range in {
			out[i] = wire.Hook{
				Matcher: h.Matcher,
				Command: h.Command,
				Type:    h.Type,
				Prompt:  h.Prompt,
				Timeout: h.Timeout,
				Async:   h.Async,
				SCM:     marker,
			}
		}
		return out
	}
	return wire.UnifiedHooks{
		PreTool:      convert(bundle.Hooks.PreTool),
		PostTool:     convert(bundle.Hooks.PostTool),
		SessionStart: convert(bundle.Hooks.SessionStart),
		SessionEnd:   convert(bundle.Hooks.SessionEnd),
		PreShell:     convert(bundle.Hooks.PreShell),
		PostFileEdit: convert(bundle.Hooks.PostFileEdit),
	}
}

// extractMCPFromBundle extracts MCP servers from a loaded bundle.
func extractMCPFromBundle(bundle *bundles.Bundle, source string) map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	for name, mcp := range bundle.MCP {
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
