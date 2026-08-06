package antigravity

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// antigravitySkillsDir is the workspace skills directory agy reads, relative
// to the workspace root.
const antigravitySkillsDir = AgentsDir + "/skills"

// WriteCommandFiles writes enabled command exports as agy Agent Skill
// directories (.agents/skills/<name>/SKILL.md, generated YAML frontmatter) and
// records them in the manifest. Previously manifest-listed files are removed
// first, so the written set always mirrors the current exports (see
// agent.WriteManagedCommandFiles for the shared mechanics; the directory is
// shared with user-authored skills, never wiped wholesale).
//
// G3 FIX (was the silent no-op): this writer used to emit flat `<name>.md`
// files, which agy's skill scanner NEVER discovers — agy only walks
// `.agents/skills/<name>/SKILL.md` DIRECTORIES (VERIFIED against agy's own
// bundled docs, see skillfiles.go's doc comment; every builtin skill is a
// dir). ctxloom slash-command exports landed on disk but were invisible to
// agy. Fixed by rendering the SAME `<name>/SKILL.md` shape kiro already used —
// agent.RenderCommandAsSkillFile is the shared renderer both engines' writers
// call (reprise flagged the two as byte-for-byte duplicates when this was a
// local copy), so the two engines' generated frontmatter can't silently drift
// apart.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(antigravitySkillsDir))
	return agent.WriteManagedCommandFiles(fs, skillsDir, cmds,
		agent.RenderCommandAsSkillFile)
}

// The command/skill name-collision resolution (both now want
// .agents/skills/<name>/SKILL.md) is documented once, canonically, on
// surfaces.go's NewSurfaces — see that doc comment (deduped from three
// near-identical tellings).
