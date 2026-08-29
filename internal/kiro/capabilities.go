//go:build parked_engines

package kiro

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// kiroSkillsDir is the workspace skills directory Kiro reads, relative to the
// workspace root. Kiro uses the agentskills.io SKILL.md standard: each skill is
// a directory holding a SKILL.md (YAML front-matter name+description, body =
// instructions), discovered via the agent's skill:// resource glob and
// invocable as the /<name> slash command.
const kiroSkillsDir = ConfigDirName + "/skills"

// WriteCommandFiles writes enabled command exports as agentskills SKILL.md files
// under .kiro/skills/<name>/SKILL.md and records them in the manifest.
// Previously manifest-listed files are removed first, so the written set always
// mirrors the current exports (see agent.WriteManagedCommandFiles for the shared
// mechanics; the directory is shared with user-authored skills, never wiped).
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(kiroSkillsDir))
	return agent.WriteManagedCommandFiles(fs, skillsDir, cmds, renderSkillFile)
}

// renderSkillFile renders one command export as a Kiro SKILL.md via the
// shared agent.RenderCommandAsSkillFile (reprise flagged kiro's and
// antigravity's copies as byte-for-byte duplicates before antigravity was
// removed in 0.7.0; kept shared rather than inlined back). Kept as a named
// wrapper (rather than passing the shared func
// directly to WriteManagedCommandFiles) so existing call sites/tests that name
// "kiro's own renderer" keep reading naturally.
func renderSkillFile(c agent.CommandExport) (string, []byte, error) {
	return agent.RenderCommandAsSkillFile(c)
}
