package backends

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/resources"
)

// LoadSkillExports loads every prompt that exports as a slash command:
// ctxloom's embedded builtin commands plus the bundle prompts. This is the
// SINGLE prompt-export assembly — both the `ctxloom run` setup payload
// (AssembleManagedConfig, which passes the run's resolved profile set) and
// operations.ApplyHooks (which passes nil → the configured defaults) route
// through it. The settings/command writers reconcile by removing all
// ctxloom-managed files and re-adding the assembled set, so two diverging
// assemblies would silently delete whatever one produced and the other didn't.
//
// Bundle prompts come from one of two sources, chosen by the SELECTED (or
// default) profiles:
//   - When the profiles declare a NON-EMPTY prompts: list (profile prompt
//     curation, opt-in), ONLY those are exported (each at its pinned version),
//     force-enabled so a curated prompt surfaces even if its bundle didn't flag
//     it as a slash command — the profile explicitly curates the set. Scoped to
//     the SELECTED profiles (profileNames), so `run -p X` curates from X rather
//     than the configured defaults.
//   - Otherwise (the common case) the SELECTED (or default) profiles' bundle
//     skills are exported via config.ResolveBundleSkills, each still gated by
//     the downstream per-backend enabled flag — the profile-scoped analog of
//     the mcp/hooks resolvers (ResolveBundleMCPServers/ResolveBundleHooks), so
//     `run -p X` carries only X's bundle skills, not every pulled bundle's.
//
// Builtins are always present in both modes (ctxloom's core commands aren't
// part of the curatable bundle-prompt set).
//
// The SeededBundleLoader is the only loader that also surfaces remote bundles
// from the lockfile clone cache; empty fs bundle dirs are fine — remote-only
// setups still produce commands.
func LoadSkillExports(cfg *config.Config, profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedContent {
	prompts := builtinSkills()

	// Gate prompt command-file exports through the same per-item trust cascade as
	// content (trust rework, TR5 follow-up #2): a prompt whose effective content
	// the cascade denies must not be exported as a slash command either. The gate
	// is the cfg-injected executable gate (nil on management paths = no gating);
	// it keys on "<bundle>#prompts/<name>", identical to the content choke, so a
	// baselined/granted prompt is exported and an untrusted one is withheld.
	if cfg != nil {
		if gate := cfg.ExecutableTrustGate(); gate != nil {
			opts = append(opts, bundles.WithTrustGate(gate))
		}
	}

	// Profile prompt curation (opt-in): a non-empty curated set exports EXACTLY
	// the listed prompts (force-enabled), scoped to the SELECTED profiles.
	if curated := resolveProfilePromptRefs(cfg, profileNames); len(curated) > 0 {
		loader := cfg.SeededBundleLoader(cfg.ShouldUseDistilled(), opts...)
		return append(prompts, loadCuratedPrompts(loader, curated)...)
	}

	// Uncurated (common case): export the SELECTED (or default) profiles' bundle
	// skills via the profile-scoped resolver — the analog of ResolveBundleMCPServers
	// / ResolveBundleHooks — so `run -p X` carries only X's bundle skills. Each is
	// still gated downstream by its per-backend enabled flag; a trust-withheld skill
	// never loads. opts thread the seed + trust gate into the resolver's loader.
	return append(prompts, cfg.ResolveBundleSkills(profileNames, opts...)...)
}

// resolveProfilePromptRefs returns the union of prompt refs curated by the
// resolved active (default) profiles, in declaration order. It mirrors how
// operations.resolveProfile resolves a default profile: inline definitions
// (config.ResolveProfile over the config.yaml profiles: map) win, and a name
// that isn't an inline profile falls back to a directory profile
// (.ctxloom/profiles/<name>.yaml via the profile loader) — so a directory
// profile's prompts: curation reaches the same curation point as an inline
// one. Each resolution carries parent inheritance (the same parents-merge the
// Fragments path uses) and the prompts: lists union across all default
// profiles. A nil/empty result means no profile curates prompts, so the caller
// keeps the global flag-based auto-export (opt-in: no silent change).
func resolveProfilePromptRefs(cfg *config.Config, profileNames []string) []string {
	if cfg == nil {
		return nil
	}
	seen := collections.NewSet[string]()
	var refs []string
	add := func(prompts []string) {
		for _, ref := range prompts {
			if !seen.Has(ref) {
				seen.Add(ref)
				refs = append(refs, ref)
			}
		}
	}
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		// Inline profile (config.yaml profiles: map) wins, matching
		// operations.resolveProfile's inline-first ordering.
		if resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName); err == nil {
			add(resolved.Prompts)
			continue
		}
		// Directory profile fallback (.ctxloom/profiles/<name>.yaml): its
		// prompts: curation lives in profiles.ResolvedProfile, the directory-side
		// mirror of config.Profile.Prompts.
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "default profile %q unresolved; its curated prompts omitted: %v", profileName, err)
			continue
		}
		add(resolved.Prompts)
	}
	return refs
}

// loadCuratedPrompts resolves each profile-curated prompt ref (honoring an
// "@<commit>" version pin and the trust gate) and force-enables its
// slash-command export so a curated prompt is exported even when its bundle
// didn't flag it — the profile explicitly curates it. A ref that doesn't
// resolve (not found, gate-withheld, or a pinned version that fails to fetch) is
// warned and skipped (fault tolerance), never aborting the rest.
func loadCuratedPrompts(loader *bundles.Loader, refs []string) []*bundles.LoadedContent {
	var out []*bundles.LoadedContent
	for _, ref := range refs {
		name, version := remote.SplitPromptVersion(ref)
		var (
			content *bundles.LoadedContent
			err     error
		)
		if version == "" {
			content, err = loader.GetSkill(ref)
		} else {
			content, err = loader.GetPromptAtVersion(name, version)
		}
		if err != nil {
			clidiag.Warn("ctxloom", "skipping curated prompt %q: %v", ref, err)
			continue
		}
		out = append(out, forceExport(content))
	}
	return out
}

// forceExport marks a loaded prompt enabled for every backend's slash-command
// export. A profile that curates a prompt is an explicit request to export it,
// so the per-prompt opt-out flag is overridden; all other export metadata
// (description, hints, model, …) is reused as-is.
func forceExport(c *bundles.LoadedContent) *bundles.LoadedContent {
	on := true
	c.LLM.ClaudeCode.Enabled = &on
	c.LLM.Antigravity.Enabled = &on
	c.LLM.Codex.Enabled = &on
	return c
}

// builtinSkills returns ctxloom's built-in slash command prompts. These are
// embedded in the ctxloom binary and always available.
func builtinSkills() []*bundles.LoadedContent {
	names, err := resources.ListBuiltinCommands()
	if err != nil {
		clidiag.Warn("ctxloom", "builtin commands unavailable: %v", err)
		return nil
	}

	var prompts []*bundles.LoadedContent
	for _, name := range names {
		content, err := resources.GetBuiltinCommand(name)
		if err != nil {
			continue
		}

		description, body := parseMarkdownFrontmatter(string(content))
		prompts = append(prompts, &bundles.LoadedContent{
			Name:    name,
			Content: body,
			LLM: bundles.LLMExports{
				ClaudeCode: bundles.ClaudeCodeConfig{
					Description: description,
				},
				Antigravity: bundles.AntigravityConfig{
					Description: description,
				},
				Codex: bundles.CodexConfig{
					Description: description,
				},
			},
		})
	}
	return prompts
}

// parseMarkdownFrontmatter extracts description from YAML frontmatter and returns body.
// Expects format: ---\ndescription: ...\n---\nbody
func parseMarkdownFrontmatter(content string) (description, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}

	// Find the closing ---
	rest := content[4:] // Skip opening "---\n"
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", content
	}

	frontmatter := rest[:endIdx]
	body = strings.TrimPrefix(rest[endIdx+4:], "\n")

	// Parse description from frontmatter (simple key: value parsing)
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			// Remove surrounding quotes if present
			if len(description) >= 2 && description[0] == '"' && description[len(description)-1] == '"' {
				description = description[1 : len(description)-1]
			}
			break
		}
	}

	return description, body
}
