package kiro

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// kiroSkillsDir is the workspace skills directory Kiro reads, relative to the
// workspace root. Kiro uses the agentskills.io SKILL.md standard: each skill is
// a directory holding a SKILL.md (YAML front-matter name+description, body =
// instructions), discovered via the agent's skill:// resource glob and
// invocable as the /<name> slash command.
const kiroSkillsDir = kiroDir + "/skills"

// kiroManifest tracks ctxloom-written skill files for clean removal. The skills
// directory is shared with user-authored skills, so it is never wiped wholesale
// — only manifest-listed files are reconciled.
const kiroManifest = ".ctxloom-manifest"

// KiroSkills writes skills for Kiro CLI as SKILL.md files under .kiro/skills/.
type KiroSkills struct{}

// RegisterFromContent writes skill files from host-resolved command exports.
// The host maps bundle content (with kiro enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *KiroSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// WriteCommandFiles writes enabled command exports as agentskills SKILL.md files
// under .kiro/skills/<name>/SKILL.md and records them in the manifest.
// Previously manifest-listed files are removed first, so the written set always
// mirrors the current exports (see agent.WriteManagedCommandFiles for the shared
// mechanics; the directory is shared with user-authored skills, never wiped).
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	skillsDir := filepath.Join(workDir, filepath.FromSlash(kiroSkillsDir))
	return agent.WriteManagedCommandFiles(fs, skillsDir, kiroManifest, cmds, renderSkillFile,
		agent.WithManifestTrailingNewline())
}

// renderSkillFile renders one command export as a Kiro SKILL.md. The relative
// path <name>/SKILL.md places each skill in its own directory (subdirectory
// names in the export name are preserved as nested directories, matched by the
// agent's `skill://.kiro/skills/**/SKILL.md` glob). name is slash-flattened for
// the front-matter identifier (the slash-command name); description is
// JSON-quoted, which is a valid YAML double-quoted scalar and needs no bespoke
// escaping.
func renderSkillFile(c agent.CommandExport) (string, []byte, error) {
	desc := c.Description
	if desc == "" {
		desc = c.Name
	}
	name := strings.ReplaceAll(c.Name, "/", "-")

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + jsonScalar(name) + "\n")
	b.WriteString("description: " + jsonScalar(desc) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(c.Content)
	if !strings.HasSuffix(c.Content, "\n") {
		b.WriteString("\n")
	}
	return c.Name + "/SKILL.md", []byte(b.String()), nil
}

// jsonScalar renders s as a double-quoted scalar safe for YAML front-matter. A
// JSON string literal is a valid YAML flow scalar, so json.Marshal handles the
// escaping (quotes, backslashes, control chars) without a bespoke quoter.
func jsonScalar(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
