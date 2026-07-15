package opencode

import (
	"path"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// opencodeSkillDir is opencode's project-scoped Agent Skills directory,
// relative to the workspace root. opencode's skill loader scans
// `.opencode/skill(s)/<name>/SKILL.md` for a skill's frontmatter
// (name+description) and body, discovering it AUTOMATICALLY under this
// default project location — VERIFIED against opencode 1.18.1 (`opencode
// debug skill` lists a skill placed here with no opencode.json change at
// all). ctxloom still registers the dir in opencode.json's `skills.paths`
// (see settings.go) for explicitness/defense-in-depth, per the singular form
// opencode's own docs list first (mirroring opencodeCommandDir's choice of
// the singular `command` over `commands`).
const opencodeSkillDir = ".opencode/skill"

// opencodeSkillManifest tracks the skill files ctxloom wrote, distinct from
// opencodeCommandManifest (commands' manifest) so the two surfaces' cleanup
// never collides — mirrors claude's .ctxloom-skills-manifest split.
const opencodeSkillManifest = ".ctxloom-skills-manifest"

// WriteSkillFiles materializes opencode's Agent Skills surface: every enabled
// skill package lands at <workDir>/.opencode/skill/<name>/SKILL.md (+ sibling
// files), exec bit preserved on scripts/ entries. ctxloom tracks its writes
// via a manifest distinct from commands' so the two surfaces' cleanup never
// collides (see agent.WriteManagedPackageFiles for the shared mechanics).
func WriteSkillFiles(workDir string, skills []agent.SkillExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	dir := filepath.Join(workDir, filepath.FromSlash(opencodeSkillDir))
	return agent.WriteManagedPackageFiles(fs, dir, opencodeSkillManifest, skills,
		func(s agent.SkillExport) bool { return s.Enabled },
		func(s agent.SkillExport) string { return s.Name },
		func(s agent.SkillExport) ([]agent.PackageFile, error) {
			out := make([]agent.PackageFile, len(s.Files))
			for i, f := range s.Files {
				out[i] = agent.PackageFile{
					RelPath: path.Join(s.Name, f.RelPath),
					Content: f.Content,
					Mode:    f.Mode,
				}
			}
			return out, nil
		},
	)
}

// reconcileSkillsSurface writes (or reverts) the skill package tree via
// WriteSkillFiles AND reconciles opencode.json's `skills.paths` registration
// to match: at least one enabled skill registers opencodeSkillDir, none
// unregisters it. This is the single write function both the persistent
// skills surface (surfaces.go) and the live chat transient overlay (chat.go)
// bind to, so the two paths can never drift on what "materializing skills"
// means for opencode.
func reconcileSkillsSurface(fs afero.Fs, workDir string, skills []agent.SkillExport) error {
	if err := WriteSkillFiles(workDir, skills, agent.WithCommandFS(fs)); err != nil {
		return err
	}
	hasEnabled := false
	for _, s := range skills {
		if s.Enabled {
			hasEnabled = true
			break
		}
	}
	if hasEnabled {
		return registerSkillsPath(fs, workDir)
	}
	return unregisterSkillsPath(fs, workDir)
}
