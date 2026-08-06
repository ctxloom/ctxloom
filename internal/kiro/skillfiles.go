package kiro

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// WriteSkillFiles materializes kiro's Agent Skills surface: every enabled
// skill package lands at .kiro/skills/<name>/SKILL.md (+ its sibling files),
// exec bit preserved on scripts/ entries. It writes into the SAME directory
// as WriteCommandFiles (kiro has one native skills dir that also serves as
// its command surface — kiroSkillsDir, capabilities.go), but tracks its own
// files via kiroSkillManifest so the two writers' cleanups never touch each
// other's set. Collision-by-name (a command and a skill sharing a directory)
// is resolved upstream, in NewSurfaces, before the command exports ever reach
// WriteCommandFiles — this writer always writes every skill it is given.
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(kiroSkillsDir))
	return agent.WriteManagedSkillPackages(fs, skillsDir, skills)
}
