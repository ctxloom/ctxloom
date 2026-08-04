package backends

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/resources"
)

// LoadCommandExports loads every prompt that exports as a slash command:
// ctxloom's embedded builtin commands, the commands shipped by every
// DISCOVERED COMPANION's loadout, plus the bundle prompts. This is the SINGLE
// prompt-export assembly — both the `ctxloom run` setup payload
// (AssembleManagedConfig, which passes the run's resolved profile set) and
// operations.ApplyHooks (which passes nil → the configured defaults) route
// through it. The settings/command writers reconcile by removing all
// ctxloom-managed files and re-adding the assembled set, so two diverging
// assemblies would silently delete whatever one produced and the other didn't.
//
// Companion commands (S8 — config.ResolveCompanionCommands) are unconditional
// whenever the companion binary is on PATH — e.g. ltk's task-runner command
// becomes the `/ltk-task-runner` slash command with no profile wiring needed —
// gated through the SAME discovery+trust path as a companion's fragments/
// hooks/MCP, never a builtin-style exemption. They are added on BOTH branches
// below (curated and uncurated), since profile command curation otherwise
// bypasses config.ResolveBundleCommands entirely and would silently drop them;
// dedup keeps an explicit curation of the SAME command (e.g. a profile that
// lists "ctxloom:companion@ltk#commands/task-runner" itself) from doubling up.
//
// Bundle prompts (non-companion) come from one of two sources, chosen by the
// SELECTED (or default) profiles:
//   - When the profiles declare a NON-EMPTY commands: list (profile command
//     curation, opt-in), ONLY those are exported (each at its pinned version),
//     force-enabled so a curated prompt surfaces even if its bundle didn't flag
//     it as a slash command — the profile explicitly curates the set. Scoped to
//     the SELECTED profiles (profileNames), so `run -p X` curates from X rather
//     than the configured defaults.
//   - Otherwise (the common case) the SELECTED (or default) profiles' bundle
//     commands are exported via config.ResolveBundleCommands, each still gated
//     by the downstream per-backend enabled flag — the profile-scoped analog of
//     the mcp/hooks resolvers (ResolveBundleMCPServers/ResolveBundleHooks), so
//     `run -p X` carries only X's bundle commands, not every pulled bundle's.
//     ResolveBundleCommands already folds in the companion commands itself, so
//     this branch does not add them again.
//
// Builtins are always present in both modes (ctxloom's core commands aren't
// part of the curatable bundle-prompt set).
//
// The SeededBundleLoader is the only loader that also surfaces remote bundles
// from the lockfile clone cache; empty fs bundle dirs are fine — remote-only
// setups still produce commands.
func LoadCommandExports(cfg *config.Config, profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedContent {
	prompts := builtinCommands()

	// A nil cfg has nothing further to resolve — mirrors LoadSkillExports'
	// guard (skillfiles.go). Without this, the uncurated branch's
	// cfg.ResolveBundleCommands(...) below dereferences cfg unguarded and
	// panics; the two earlier `if cfg != nil` guards never protected that
	// call.
	if cfg == nil {
		return prompts
	}

	// Gate prompt command-file exports through the same per-item decision
	// function as content: a prompt whose effective content the trust model
	// denies must not be exported as a slash command either. The gate is the
	// cfg-injected executable gate (nil on management paths = no gating); it
	// keys on "<bundle>#prompts/<name>", identical to the content choke, so an
	// accepted/exempt prompt is exported and a pending/rejected one is withheld.
	if gate := cfg.ExecutableTrustGate(); gate != nil {
		opts = append(opts, bundles.WithTrustGate(gate))
	}

	// Profile command curation (opt-in): a non-empty curated set exports EXACTLY
	// the listed prompts (force-enabled), scoped to the SELECTED profiles. Even
	// here, companion commands stay unconditional (see doc comment above).
	if curated := resolveProfilePromptRefs(cfg, profileNames); len(curated) > 0 {
		loader := cfg.SeededBundleLoader(opts...)
		prompts = append(prompts, loadCuratedPrompts(loader, curated, cfg.ShouldUseDistilled())...)
		prompts = append(prompts, dedupCommandsByItem(prompts, cfg.ResolveCompanionCommands(opts...))...)
		return prompts
	}

	// Uncurated (common case): export the SELECTED (or default) profiles' bundle
	// commands via the profile-scoped resolver — the analog of ResolveBundleMCPServers
	// / ResolveBundleHooks — so `run -p X` carries only X's bundle commands, plus
	// every discovered companion's commands (ResolveBundleCommands folds both
	// in). Each is still gated downstream by its per-backend enabled flag; a
	// trust-withheld command never loads. opts thread the seed + trust gate
	// into the resolver's loader.
	return append(prompts, cfg.ResolveBundleCommands(profileNames, opts...)...)
}

// dedupCommandsByItem returns the entries of add whose Item does not already
// appear in existing, preserving add's order. Used to fold companion commands
// into the curated LoadCommandExports branch without doubling up a command a
// profile's commands: list already names explicitly.
func dedupCommandsByItem(existing, add []*bundles.LoadedContent) []*bundles.LoadedContent {
	seen := make(map[string]bool, len(existing))
	for _, p := range existing {
		if p.Item != "" {
			seen[p.Item] = true
		}
	}
	var out []*bundles.LoadedContent
	for _, p := range add {
		if seen[p.Item] {
			continue
		}
		seen[p.Item] = true
		out = append(out, p)
	}
	return out
}

// resolveProfilePromptRefs returns the union of command refs curated by the
// resolved active (default) profiles, in declaration order. It mirrors how
// operations.resolveProfile resolves a default profile: inline definitions
// (config.ResolveProfile over the config.yaml profiles: map) win, and a name
// that isn't an inline profile falls back to a directory profile
// (.ctxloom/profiles/<name>.yaml via the profile loader) — so a directory
// profile's commands: curation reaches the same curation point as an inline
// one. Each resolution carries parent inheritance (the same parents-merge the
// Fragments path uses) and the commands: lists union across all default
// profiles. A nil/empty result means no profile curates commands, so the
// caller keeps the global flag-based auto-export (opt-in: no silent change).
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
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		// Inline profile (config.yaml profiles: map) wins, matching
		// operations.resolveProfile's inline-first ordering. A non-nil inlineErr
		// is either "not an inline profile" (errs.ErrProfileNotFound — fall
		// through to the directory loader) or a REAL inline-profile defect
		// (circular inheritance, depth exceeded) the directory fallback cannot
		// explain: this used to treat both the same, so a broken inline
		// profile's real cause was discarded for the directory loader's
		// unrelated not-found error.
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			add(inlineResolved.Commands)
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom", "profile %q: inline resolution failed; its curated prompts omitted: %v", profileName, inlineErr)
			continue
		}
		// Directory profile fallback (.ctxloom/profiles/<name>.yaml): its
		// commands: curation lives in profiles.ResolvedProfile, the directory-side
		// mirror of config.Profile.Commands.
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "profile %q unresolved; its curated prompts omitted: %v", profileName, err)
			continue
		}
		add(resolved.Commands)
	}
	return refs
}

// loadCuratedPrompts resolves each profile-curated prompt ref (honoring an
// "@<commit>" version pin and the trust gate) and force-enables its
// slash-command export so a curated prompt is exported even when its bundle
// didn't flag it — the profile explicitly curates it. A ref that doesn't
// resolve (not found, gate-withheld, or a pinned version that fails to fetch) is
// warned and skipped (fault tolerance), never aborting the rest.
func loadCuratedPrompts(loader *bundles.Loader, refs []string, preferDistilled bool) []*bundles.LoadedContent {
	var out []*bundles.LoadedContent
	for _, ref := range refs {
		name, version := remote.SplitPromptVersion(ref)
		var (
			content *bundles.LoadedContent
			err     error
		)
		if version == "" {
			content, err = loader.GetCommand(ref, preferDistilled)
		} else {
			content, err = loader.GetPromptAtVersion(name, version, preferDistilled)
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
// (description, hints, model, …) is reused as-is. Mirrors forceExportSkill
// (skillfiles.go), which force-enables all five engines: this used to cover
// only claude/antigravity/codex, so a profile-curated command whose bundle
// set `kiro: {enabled: false}` or `opencode: {enabled: false}` silently
// exported nothing for those two engines despite the explicit curation.
// Deliberately parallel with forceExportSkill (skillfiles.go): same shape by
// design (force-enable every engine), different item types (LoadedContent vs
// LoadedSkill) with no shared supertype to factor through without a
// cross-file generics refactor outside this fix's scope.
// reprise:ignore
func forceExport(c *bundles.LoadedContent) *bundles.LoadedContent {
	on := true
	c.LLM.ClaudeCode.Enabled = &on
	c.LLM.Antigravity.Enabled = &on
	c.LLM.Codex.Enabled = &on
	c.LLM.Kiro.Enabled = &on
	c.LLM.Opencode.Enabled = &on
	return c
}

// getBuiltinCommandFn is the seam onto resources.GetBuiltinCommand — a package
// var (mirrors internal/codex/backend.go's seedCodexHomeFn) so a test can force
// the per-name read to fail without needing a broken embed.FS, which is
// otherwise immutable at runtime and never fails for a name ListBuiltinCommands
// itself just enumerated.
var getBuiltinCommandFn = resources.GetBuiltinCommand

// builtinCommands returns ctxloom's built-in slash command prompts. These are
// embedded in the ctxloom binary and always available.
func builtinCommands() []*bundles.LoadedContent {
	names, err := resources.ListBuiltinCommands()
	if err != nil {
		clidiag.Warn("ctxloom", "builtin commands unavailable: %v", err)
		return nil
	}

	var prompts []*bundles.LoadedContent
	for _, name := range names {
		content, err := getBuiltinCommandFn(name)
		if err != nil {
			// This used to `continue` with zero diagnostics. Because the
			// command writers reconcile by removing every ctxloom-managed
			// file then re-adding the assembled set, a dropped builtin
			// simply vanishes from the next materialize with no signal at
			// all — warn so the loss is visible even though degrading
			// (never blocking launch over one missing embedded file) is
			// still right.
			clidiag.Warn("ctxloom", "builtin command %q unavailable: %v", name, err)
			continue
		}

		description, body := resources.SplitCommandFrontmatter(string(content))
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
