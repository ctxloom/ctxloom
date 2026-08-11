package agent

// SkillExport is the agent-agnostic Agent Skill package export spec for one
// skill — the SurfaceSkills sibling of CommandExport. Unlike a command (a
// single rendered file), a skill is a whole TREE: SKILL.md plus whatever
// sibling files (scripts/, assets/, references/) its package carries. The
// per-agent skill writers (currently claude only — codex/opencode/kiro are
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
	//
	// No such engine exists yet, so this field is WRITE-ONLY. It is set once, by
	// lm/backends/skillfiles.go's buildSkillExports, and read by no engine —
	// claude/opencode's skill writers take Enabled, Name and Files
	// only; the sole read anywhere is one assertion in skillfiles_test.go.
	// Nothing is lost by deleting it: the description an engine actually reads
	// travels verbatim inside the authored SKILL.md, which is one of Files
	// (pinned by TestSkillExports_DescriptionReachesTheEngineInSKILLmd).
	//
	// It is NOT deleted because it is WIRE-BACKED, which is not a Go-side call:
	// llm.proto's `SkillExport.description = 2`
	// carries it host->plugin, with converters at lm/grpc/managed.go:109 and
	// :128. Removing the Go field alone leaves a proto field nothing populates
	// — which the wire-parity gate (lm/grpc/arch_test.go) exists to reject —
	// so the honest change removes or RESERVES the proto field too. That is a
	// schema edit for the human.
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
