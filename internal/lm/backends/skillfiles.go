package backends

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
)

// mockSkillExports resolves the mock engine's per-skill enablement: every
// resolved skill is exported.
//
// No bundle carries a block for mock — mock is a test engine nobody
// publishes a bundle FOR — so there is no per-engine opt-out to read and
// nothing to invent one from. Enabling everything is the honest reading of
// that absence, and it is what makes mock a usable hermetic vehicle: whatever
// a profile resolves is exactly what mock materializes, with no engine-specific
// filter standing between a test's fixture and its assertion.
//
// It goes through the SAME buildSkillExports loop as the five real engines
// rather than mapping bundles.LoadedSkill to agent.SkillExport itself, so the
// file bytes and the DECLARED modes reach the surface by the one path.
func mockSkillExports(skills []*bundles.LoadedSkill) []agent.SkillExport {
	return hosting.BuildSkillExports(skills, func(*bundles.LoadedSkill) bool { return true })
}
