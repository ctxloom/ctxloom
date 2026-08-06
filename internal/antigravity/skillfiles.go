package antigravity

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// antigravitySkillsDir (capabilities.go) is agy's workspace Agent Skills
// directory, .agents/skills — the SAME parent directory WriteCommandFiles
// targets (both render `<name>/SKILL.md`). See surfaces.go's NewSurfaces doc
// comment for the canonical account of that shared-parent resolution
// (deduped from three near-identical tellings; the version this comment
// used to carry described the RETIRED flat `<name>.md` command shape and
// was wrong).
//
// VERIFIED against agy's own bundled documentation
// (~/.gemini/antigravity-cli/builtin/skills/agy-customizations/docs/skills.md,
// agy 1.1.2, read directly — no model tokens spent): "A skill must be
// structured as a directory within a `skills/` folder inside a customization
// root (e.g., `.agents/skills/`)", and agy's own customization guide lists
// workspace discovery at `.agents/` (or `.agent/`, `_agents/`, `_agent/`)
// walked up from cwd to the repo root.

// WriteSkillFiles materializes agy's Agent Skills surface: every enabled
// skill package lands at .agents/skills/<name>/SKILL.md (+ sibling files),
// exec bit preserved on scripts/ entries. ctxloom tracks its writes via a
// manifest distinct from commands' so the two surfaces' cleanup never
// collides (see agent.WriteManagedSkillPackages for the shared mechanics —
// this writer supplies only agy's directory and manifest name).
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(antigravitySkillsDir))
	return agent.WriteManagedSkillPackages(fs, skillsDir, skills)
}
