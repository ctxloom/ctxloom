package claude

import (
	"path"
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
// agent.WriteManagedPackageFiles tree writer, engine-specific only in the
// target directory and the per-file path prefix (each file's package-relative
// path is joined under its own skill's name).
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, ConfigDirName, SkillsDirName)

	return agent.WriteManagedPackageFiles(fs, skillsDir, ".ctxloom-skills-manifest", skills,
		func(s agent.SkillExport) bool { return s.Enabled },
		func(s agent.SkillExport) string { return s.Name },
		func(s agent.SkillExport) ([]agent.PackageFile, error) {
			out := make([]agent.PackageFile, len(s.Files))
			for i, f := range s.Files {
				out[i] = agent.PackageFile{
					RelPath: path.Join(s.Name, f.RelPath),
					Content: f.Content,
					Mode:    f.Mode,
				}
			}
			return out, nil
		},
	)
}
