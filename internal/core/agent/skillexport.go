package agent

// SkillExport is the agent-agnostic Agent Skill package export spec for one
// skill — the SurfaceSkills sibling of CommandExport. Unlike a command (a
// single rendered file), a skill is a whole TREE: SKILL.md plus whatever
// sibling files (scripts/, assets/, references/) its package carries. The
// per-agent skill writers consume this without importing ctxloom's bundle
// types, mirroring how CommandExport decouples the writers from
// bundles.LoadedContent.
type SkillExport struct {
	// Name is the SKILL.md frontmatter `name`, carried verbatim from the
	// package. It is used as the materialized subdirectory name under the
	// engine's native skills dir — which is why an engine's own rules for it
	// (length, character set, reserved words) are checked by that engine's
	// writer before it emits, not on load.
	Name string
	// Description mirrors the SKILL.md frontmatter `description`, carried
	// verbatim like Name. No engine registers it outside the package (claude
	// discovers skills by scanning the directory, and the description it acts
	// on travels inside the authored SKILL.md, one of Files — pinned by
	// TestSkillExports_DescriptionReachesTheEngineInSKILLmd); what an engine's
	// writer reads it FOR is its own acceptance rules (presence, length) before
	// it emits the package. It is also WIRE-BACKED: llm.proto's
	// `SkillExport.description` carries it host->plugin, so removing it would
	// be a schema edit, not a Go-side deletion.
	Description string
	// Enabled is the resolved per-target-agent enablement (already resolved
	// host-side from bundles.SkillLLMExports), mirroring CommandExport.Enabled.
	Enabled bool
	// Files is every file the package materializes — SKILL.md included — with
	// its path relative to the skill's OWN directory (never "<name>/…"; the
	// writer joins the skill directory itself) and its POSIX mode. The exec bit
	// on scripts/ entries is load-bearing and must survive to the materialized
	// file.
	Files []PackageFile
}
