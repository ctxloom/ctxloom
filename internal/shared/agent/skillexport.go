package agent

// SkillExport is the agent-agnostic Agent Skill package export spec for one
// skill — the SurfaceSkills sibling of CommandExport. Unlike a command (a
// single rendered file), a skill is a whole TREE: SKILL.md plus whatever
// sibling files (scripts/, assets/, references/) its package carries. The
// per-agent skill writers (currently claude only — codex/opencode/kiro/agy are
// the next parallel wave) consume this without importing ctxloom's bundle
// types, mirroring how CommandExport decouples the writers from
// bundles.LoadedContent.
type SkillExport struct {
	// Name is the skill's package/directory name (SKILL.md frontmatter `name`,
	// already validated to equal its source directory's basename — see
	// bundles.ParseSkillPackage). It is used as the materialized subdirectory
	// name under the engine's native skills dir.
	Name string
	// Description mirrors the SKILL.md frontmatter `description` for engines
	// that need it registered outside the package itself (e.g. a config-file
	// listing) — claude needs none (it discovers skills by scanning the
	// directory), but the field travels so a later engine doesn't need a
	// different export type.
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
