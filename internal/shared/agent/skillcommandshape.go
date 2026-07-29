package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/afero"
)

// This file is the shared core for engines whose ONLY command surface is the
// agentskills.io SKILL.md convention (kiro, antigravity): a command export
// renders as `<name>/SKILL.md` with generated YAML frontmatter, and a
// same-named Agent Skill package collides with it on the identical path — a
// collision resolved skill-wins. Both engines' writers wrap these helpers
// verbatim rather than each carrying its own copy (a real duplicate reprise's
// gate caught: kiro's renderSkillFile/filterClaimedCommands and antigravity's
// were byte-for-byte the same shape modulo the engine name/skills-dir
// display string).

// yamlDoubleQuoted renders s as a double-quoted scalar safe for YAML
// front-matter. A JSON string literal is a valid YAML double-quoted scalar, so
// the JSON encoder supplies the escaping — quotes, backslashes AND control
// characters — without a bespoke quoter. It is the ONE escaping algorithm this
// package has; EscapeYAMLString (commandfiles.go) applies its own
// quote-or-not policy and then delegates here rather than keeping hand-written
// rules of its own, which escaped neither \n nor \r.
//
// HTML escaping is turned OFF deliberately: json.Marshal's default would emit
// < / > / & for <, > and &, which are ordinary characters in
// YAML. Leaving them escaped made the two quoters disagree and put unreadable
// sequences into every generated SKILL.md carrying an angle bracket.
//
// It is unexported: no package outside this one has ever called it.
func yamlDoubleQuoted(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	// Encode appends a trailing newline; the scalar is everything before it.
	return strings.TrimRight(buf.String(), "\n")
}

// RenderCommandAsSkillFile renders one command export as an agentskills
// SKILL.md: the relative path `<name>/SKILL.md` places each skill in its own
// directory (subdirectory names in the export name are preserved as nested
// directories, matched by a `skill://…/**/SKILL.md`-shaped discovery glob);
// name is slash-flattened for the front-matter identifier (the slash-command
// name); description falls back to name when empty.
func RenderCommandAsSkillFile(c CommandExport) (string, []byte, error) {
	// U102-F01: empty (or whitespace-only) Content used to still render a
	// valid-looking SKILL.md carrying only frontmatter — success, zero
	// instruction bytes delivered to the engine. The caller
	// (WriteManagedPackageFiles) already warns loudly and skips this one item
	// on a render error, so refusing here makes the loss visible instead of
	// materializing a hollow skill.
	if strings.TrimSpace(c.Content) == "" {
		return "", nil, fmt.Errorf("command %q has no content to render as a skill", c.Name)
	}

	desc := c.Description
	if desc == "" {
		desc = c.Name
	}
	name := strings.ReplaceAll(c.Name, "/", "-")

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + yamlDoubleQuoted(name) + "\n")
	b.WriteString("description: " + yamlDoubleQuoted(desc) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(c.Content)
	if !strings.HasSuffix(c.Content, "\n") {
		b.WriteString("\n")
	}
	return c.Name + "/SKILL.md", []byte(b.String()), nil
}

// FilterCommandsClaimedBySkills is the shared skill-wins collision guard for
// an engine whose commands and Agent Skills BOTH render to
// `<skillsDirDisplay>/<name>/SKILL.md` under one native directory: a name
// claimed by an ENABLED skill is dropped from the returned command set before
// the caller ever hands it to its command writer, so the two writers'
// manifests never both claim the same path (a later re-materialize, with the
// skill removed or disabled, lets the command reclaim the name cleanly, and
// neither writer's cleanup can strand the other's live file).
//
// engineName and skillsDirDisplay are only used to render the WARN message
// (e.g. "kiro", ".kiro/skills" or "antigravity", ".agents/skills") — the
// filtering logic itself is engine-agnostic.
func FilterCommandsClaimedBySkills(engineName, skillsDirDisplay string, cmds []CommandExport, skills []SkillExport) []CommandExport {
	claimed := make(map[string]bool, len(skills))
	for _, s := range skills {
		if s.Enabled {
			claimed[s.Name] = true
		}
	}
	if len(claimed) == 0 {
		return cmds
	}
	out := make([]CommandExport, 0, len(cmds))
	for _, c := range cmds {
		if claimed[c.Name] {
			Warn("%s: skill %q wins over command of the same name; command not written to %s/%s/SKILL.md",
				engineName, c.Name, skillsDirDisplay, c.Name)
			continue
		}
		out = append(out, c)
	}
	return out
}

// NewSkillShapedCommandsAndSkills builds the collision-safe commands+skills
// delivery PAIR for an engine whose only command surface is the SKILL.md
// convention (kiro, antigravity): filters in.Commands through
// FilterCommandsClaimedBySkills, then wraps writeCommands/writeSkills in the
// standard NewManagedCommandsDelivery/NewManagedSkillPackagesDelivery pair.
// Both engines' NewSurfaces used to inline this identical sequence (reprise's
// duplicate-detection gate caught the two as a 95%-similar inconsistent
// update); this is the ONE place it is assembled now — an engine's NewSurfaces
// still owns its OWN context/MCP/settings surfaces and the returned dispatch
// map, which genuinely differ per engine and stay un-shared.
//
// namePrefix names the two deliveries ("<namePrefix>/commands",
// "<namePrefix>/skills" — kiro's are "kiro/commands"/"kiro/skills",
// antigravity's "antigravity/commands"/"antigravity/skills"); engineName and
// skillsDirDisplay feed FilterCommandsClaimedBySkills' WARN message.
func NewSkillShapedCommandsAndSkills(
	namePrefix, engineName, skillsDirDisplay string,
	in SurfaceInputs, fs afero.Fs,
	writeCommands func(dir string, cmds []CommandExport, fs afero.Fs) error,
	writeSkills func(dir string, skills []SkillExport, fs afero.Fs) error,
) (*ManagedCommandsDelivery, *ManagedSkillPackagesDelivery) {
	cmds := FilterCommandsClaimedBySkills(engineName, skillsDirDisplay, in.Commands, in.Skills)
	commands := NewManagedCommandsDelivery(namePrefix+"/commands", cmds, func(dir string, c []CommandExport) error {
		return writeCommands(dir, c, fs)
	})
	skills := NewManagedSkillPackagesDelivery(namePrefix+"/skills", in.Skills, func(dir string, s []SkillExport) error {
		return writeSkills(dir, s, fs)
	})
	return commands, skills
}
