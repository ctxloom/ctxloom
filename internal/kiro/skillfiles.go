package kiro

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// kiroSkillManifest tracks ctxloom-written SKILL PACKAGE files (the new Agent
// Skills surface), distinct from kiroManifest (the commands surface's
// manifest) so the two surfaces' cleanup never collides — mirrors claude's
// .ctxloom-skills-manifest / codex's codexSkillManifest split. This is the
// "two manifests" half of the skill/command split plan's D6: kiro is the
// COLLISION engine (both kinds share .kiro/skills/), so keeping each writer's
// tracked-file set independent is what makes cleanup safe (see the
// name-claiming note above NewSurfaces in surfaces.go for the other half —
// skill-wins arbitration via the shared agent.FilterCommandsClaimedBySkills).
const kiroSkillManifest = ".ctxloom-skills-manifest"

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
	return agent.WriteManagedSkillPackages(fs, skillsDir, kiroSkillManifest, skills)
}
