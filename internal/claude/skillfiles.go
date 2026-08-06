package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// WriteSkillFiles materializes claude's Agent Skills surface: every enabled
// skill package lands at .claude/skills/<name>/SKILL.md (+ its sibling
// files), exec bit preserved on scripts/ entries. Like WriteCommandFiles, the
// directory is shared territory with the user's own skills, so ctxloom tracks
// its writes via a manifest (.ctxloom-skills-manifest, distinct from commands'
// .ctxloom-manifest so the two surfaces' cleanup never collides) and reverts
// exactly that set on a re-materialize with fewer/no skills. This is claude's
// half of the skill/command split plan's Part B3-seam: the shared
// agent.WriteManagedSkillPackages writer, engine-specific ONLY in the target
// directory and the manifest name — the per-skill path prefix, the declared
// mode and the manifest-scoped reversal are the one shared body every engine
// (and the mock engine) goes through.
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, ConfigDirName, SkillsDirName)
	return agent.WriteManagedSkillPackages(fs, skillsDir, skills)
}
