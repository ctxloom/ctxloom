package backends

import (
	"errors"
	"os"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// modeFromPOSIX converts a resolved skill file's POSIX permission bits
// (bundles.LoadedSkillFile.Mode — plain uint32, since bundles.go avoids an
// os.FileMode dependency in the loader/manifest layer) into the os.FileMode
// agent.PackageFile carries.
func modeFromPOSIX(mode uint32) os.FileMode {
	return os.FileMode(mode)
}

// This file is the skills analog of commands.go/commandfiles.go: the
// single skill-export assembly (LoadSkillExports) and the per-engine mapping
// from a resolved bundle skill to that engine's agent.SkillExport. claude
// (Part B3-seam, the reference engine), codex, opencode, antigravity (Part
// B4), and kiro (Part B5, the collision case) are wired. kiro's mapping here
// is ordinary — its distinctiveness (sharing .kiro/skills/ with the renamed
// command surface, and the D6 skill-wins collision rule) lives entirely in
// internal/kiro/surfaces.go's filterClaimedCommands
// (skill-command-split.plan.md §3.3/§3.5). An engine with no skillExports
// mapper simply exports no skills (mirrors a nil `exports` meaning no command
// export).

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
func LoadSkillExports(cfg *config.Config, profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedSkill {
	if cfg == nil {
		return nil
	}
	if gate := cfg.ExecutableTrustGate(); gate != nil {
		opts = append(opts, bundles.WithTrustGate(gate))
	}
	if curated := resolveProfileSkillRefs(cfg, profileNames); len(curated) > 0 {
		loader := cfg.SeededBundleLoader(opts...)
		return loadCuratedSkills(loader, curated)
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
	profileDefs := cfg.GetProfileDefinitions()
	for _, profileName := range scopedProfiles(cfg, profileNames) {
		// Same real-vs-not-found error distinction as resolveProfilePromptRefs
		// (commands.go): a broken inline profile must not be silently
		// retried as a directory profile.
		inlineResolved, inlineErr := config.ResolveProfile(profileDefs, profileName)
		if inlineErr == nil {
			add(inlineResolved.Skills)
			continue
		}
		if !errors.Is(inlineErr, errs.ErrProfileNotFound) {
			clidiag.Warn("ctxloom", "profile %q: inline resolution failed; its curated skills omitted: %v", profileName, inlineErr)
			continue
		}
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
func loadCuratedSkills(loader *bundles.Loader, refs []string) []*bundles.LoadedSkill {
	var out []*bundles.LoadedSkill
	for _, ref := range refs {
		ls, err := loader.GetSkill(ref)
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
	ls.LLM.Antigravity.Enabled = &on
	ls.LLM.Codex.Enabled = &on
	ls.LLM.Kiro.Enabled = &on
	ls.LLM.Opencode.Enabled = &on
	return ls
}

// buildSkillExports is the shared skill-export loop: it maps every resolved
// skill's frontmatter/files (engine-agnostic) into an agent.SkillExport with
// pick supplying the engine-specific enablement.
func buildSkillExports(skills []*bundles.LoadedSkill, pick func(*bundles.LoadedSkill) bool) []agent.SkillExport {
	out := make([]agent.SkillExport, 0, len(skills))
	for _, s := range skills {
		files := make([]agent.PackageFile, 0, len(s.Files))
		for _, f := range s.Files {
			files = append(files, agent.PackageFile{RelPath: f.RelPath, Content: f.Content, Mode: modeFromPOSIX(f.Mode)})
		}
		out = append(out, agent.SkillExport{
			Name:        s.Frontmatter.Name,
			Description: s.Frontmatter.Description,
			Enabled:     pick(s),
			Files:       files,
		})
	}
	return out
}

// claudeSkillExports resolves claude-code's per-skill enablement.
func claudeSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return buildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.ClaudeCode.IsEnabled() })
}

// codexSkillExports resolves codex's per-skill enablement (Part B4).
func codexSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return buildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.Codex.IsEnabled() })
}

// opencodeSkillExports resolves opencode's per-skill enablement (Part B4).
func opencodeSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return buildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.Opencode.IsEnabled() })
}

// antigravitySkillExports resolves antigravity's per-skill enablement (Part B4).
func antigravitySkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return buildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.Antigravity.IsEnabled() })
}

// kiroSkillExports resolves kiro's per-skill enablement (Part B5 — the
// collision engine). This mapping is identical in shape to every other
// engine's; kiro's distinctiveness (sharing .kiro/skills/ with the renamed
// command surface, and the resulting D6 skill-wins collision rule) lives
// entirely in internal/kiro/surfaces.go's filterClaimedCommands, not here.
func kiroSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return buildSkillExports(skills, func(s *bundles.LoadedSkill) bool { return s.LLM.Kiro.IsEnabled() })
}
