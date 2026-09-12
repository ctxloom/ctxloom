package backends

import (
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// This file is the skills analog of commands.go/commandfiles.go: the
// single skill-export assembly (LoadSkillExports) and the per-engine mapping
// from a resolved bundle skill to that engine's agent.SkillExport. An engine
// with no skillExports mapper simply exports no skills (mirrors a nil
// `exports` meaning no command export).

// LoadSkillExports loads every Agent Skill package shipped by the SELECTED (or
// default) profiles' bundles. Unlike LoadCommandExports there are no built-in
// embedded skills and no companion-shipped skills yet (Part B6b) — but a
// profile's `skills:` CURATED list (opt-in, mirroring LoadCommandExports'
// commands: curation, B6a) now takes precedence exactly like commands.go's
// curated branch: a non-empty curated set exports EXACTLY the listed skills
// (force-enabled per engine), scoped to the SELECTED profiles; otherwise the
// uncurated path (config.ResolveBundleSkills, every profile-referenced
// bundle's skills) is unchanged. Both are gated through the SAME executable
// trust gate as commands/mcp/hooks when cfg carries one.
func LoadSkillExports(cfg *config.Config, profileNames []string, opts ...config.BundleLoaderOption) []*bundles.LoadedSkill {
	if cfg == nil {
		return nil
	}
	if curated := resolveProfileSkillRefs(cfg, profileNames); len(curated) > 0 {
		// Same gate as the command curation branch (commands.go): the
		// cfg-injected executable gate, nil on management paths.
		return loadCuratedSkills(
			bundles.NewPipeline(cfg.BundleLoader(opts...), cfg.ExecutableTrustGate(), cfg.ShouldUseDistilled()),
			curated)
	}
	return cfg.ResolveBundleSkills(profileNames, opts...)
}

// resolveProfileSkillRefs returns the union of skill refs curated by the
// resolved active (default) profiles, in declaration order — the skill mirror
// of resolveProfilePromptRefs (commands.go). Inline definitions
// (config.ResolveProfile) win, and a name that isn't an inline profile falls
// back to a directory profile, exactly like the command resolver. A nil/empty
// result means no profile curates skills, so the caller keeps the global
// bundle-wide auto-export (opt-in: no silent change).
func resolveProfileSkillRefs(cfg *config.Config, profileNames []string) []string {
	if cfg == nil {
		return nil
	}
	seen := collections.NewSet[string]()
	var refs []string
	add := func(skills []string) {
		for _, ref := range skills {
			if !seen.Has(ref) {
				seen.Add(ref)
				refs = append(refs, ref)
			}
		}
	}
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		resolved, err := cfg.GetProfileLoader().ResolveProfile(profileName, nil)
		if err != nil {
			clidiag.Warn("ctxloom", "profile %q unresolved; its curated skills omitted: %v", profileName, err)
			continue
		}
		add(resolved.Skills)
	}
	return refs
}

// loadCuratedSkills resolves each profile-curated skill ref (via loader.GetSkill
// — no version pin support, unlike loadCuratedPrompts, since a skill carries no
// historical-content resolution) and force-enables its export for every engine
// so a curated skill surfaces even when its bundle didn't flag it. A ref that
// doesn't resolve (not found, gate-withheld) is warned and skipped — fault
// tolerance, never aborting the rest of the curated set.
func loadCuratedSkills(pipe *bundles.Pipeline, refs []string) []*bundles.LoadedSkill {
	var out []*bundles.LoadedSkill
	for _, ref := range refs {
		ls, err := pipe.GetSkill(ref)
		if err != nil {
			clidiag.Warn("ctxloom", "skipping curated skill %q: %v", ref, err)
			continue
		}
		out = append(out, forceExportSkill(ls))
	}
	return out
}

// forceExportSkill marks a loaded skill enabled for every engine's export. A
// profile that curates a skill is an explicit request to export it, so the
// per-skill per-engine opt-out flag is overridden — the skill mirror of
// forceExport (commands.go). Deliberately parallel with it: same
// shape by design, different item types with no shared supertype to factor
// through without a cross-file generics refactor.
// reprise:ignore
func forceExportSkill(ls *bundles.LoadedSkill) *bundles.LoadedSkill {
	on := true
	ls.LLM.ClaudeCode.Enabled = &on
	return ls
}

// mockSkillExports resolves the mock engine's per-skill enablement: every
// resolved skill is exported.
//
// bundles.SkillLLMExports declares no `mock` key — mock is a test engine
// nobody publishes a bundle FOR — so there is no per-engine opt-out to read
// and nothing to invent one from. Enabling everything is the honest reading of
// that absence, and it is what makes mock a usable hermetic vehicle: whatever
// a profile resolves is exactly what mock materializes, with no engine-specific
// filter standing between a test's fixture and its assertion.
//
// It goes through the SAME buildSkillExports loop as the five real engines
// rather than mapping bundles.LoadedSkill to agent.SkillExport itself, so the
// file bytes and the DECLARED modes reach the surface by the one path.
func mockSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return engine.BuildSkillExports(skills, func(*bundles.LoadedSkill) bool { return true })
}
