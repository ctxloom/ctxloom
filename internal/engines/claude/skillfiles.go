package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// WriteSkillFiles materializes claude's Agent Skills surface: every enabled
// skill package lands at .claude/skills/<name>/SKILL.md (+ its sibling
// files), exec bit preserved on scripts/ entries. It is the shared
// agent.WriteManagedSkillPackages writer, engine-specific ONLY in the target
// directory and the vendor constraints applied on the way in (acceptedSkills,
// skillconstraints.go). Like WriteCommandFiles it removes nothing.
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	files := agent.ResolveCommandRoot(opts...)
	skillsDir := filepath.Join(workDir, ConfigDirName, SkillsDirName)
	_, err := agent.WriteManagedSkillPackages(files, skillsDir, acceptedSkills(skills), agent.WithWriteReporter(agent.ResolveReporter(opts...)))
	return err
}
