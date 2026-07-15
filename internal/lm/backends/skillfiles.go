package backends

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
// (Part B3-seam, the reference engine), codex, opencode, and antigravity
// (Part B4) are wired; kiro is the collision case, handled separately
// (skill-command-split.plan.md §3.3/§3.5 B5). An engine with no skillExports
// mapper simply exports no skills (mirrors a nil `exports` meaning no command
// export).

// LoadSkillExports loads every Agent Skill package shipped by the SELECTED (or
// default) profiles' bundles. Unlike LoadCommandExports there are no built-in
// embedded skills and no companion-shipped skills yet (Part B6) — this is
// exactly config.ResolveBundleSkills' uncurated path, gated through the SAME
// executable trust gate as commands/mcp/hooks when cfg carries one.
func LoadSkillExports(cfg *config.Config, profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedSkill {
	if cfg == nil {
		return nil
	}
	if gate := cfg.ExecutableTrustGate(); gate != nil {
		opts = append(opts, bundles.WithTrustGate(gate))
	}
	return cfg.ResolveBundleSkills(profileNames, opts...)
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
