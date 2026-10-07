package agent

import (
	"path"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the skills-surface analog of managed_commands.go: the shared
// deliver body for engines whose skill package exports are
// TREES written by the managed package writer. Only WHICH writer, at
// WHICH path, is engine-specific; that is the injected write func. Cloned from
// ManagedCommandsDelivery per the skill/command split plan §3.4 ("new export
// type … ManagedSkillPackagesDelivery cloned from the ManagedCommandsDelivery
// pattern").
//
// It also holds WriteManagedSkillPackages, the write half that same "only the
// path is engine-specific" claim implies.

// ManagedSkillPackagesDelivery is the shared skills Delivery for engines whose
// skill exports are managed package trees: on Deliver it writes every enabled
// package. Removal is the static writer's release, never this form's. Managed
// skill files are cwd-rooted with no
// out-of-cwd form (no engine has an out-of-cwd flag for a skill package).
type ManagedSkillPackagesDelivery struct {
	rel    string // the skills dir beneath the project root, for Present
	skills []SkillExport
	write  func(dir string, skills []SkillExport) error
}

// NewManagedSkillPackagesDelivery builds a managed-skills Delivery from the
// enabled exports and the engine's skill-package writer, bound so that
// write(dir, skills) materializes every package under dir. rel
// is the skills directory the writer lands in, relative to the project root —
// what Present declares.
func NewManagedSkillPackagesDelivery(rel string, skills []SkillExport, write func(dir string, skills []SkillExport) error) *ManagedSkillPackagesDelivery {
	return &ManagedSkillPackagesDelivery{rel: rel, skills: skills, write: write}
}

// Present declares the skills directory beneath the advised project root. No
// flag: no engine has an out-of-cwd redirect for a skill package.
func (s *ManagedSkillPackagesDelivery) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(s.rel).Build()
}

// Deliver writes the enabled skill package exports beneath the advised project
// root via the injected writer; the handle leaves them in place.
func (s *ManagedSkillPackagesDelivery) Deliver(start present.Start) (Delivered, error) {
	if err := s.write(start.Paths().ProjectRoot.Host, s.skills); err != nil {
		return nil, err
	}
	return SurfacePersistsAfterExit, nil
}

// WriteManagedSkillPackages materializes every ENABLED skill package under
// skillsDir — `<skillsDir>/<skill name>/SKILL.md` plus every sibling file the
// package carries, each at the mode its export DECLARES — and returns the host
// path of every file it placed, for the caller to declare. It removes
// nothing (WriteManagedPackageFiles).
//
// This is the ONE skill-materialization body in the tree: every engine calls
// it, differing only in the two arguments it takes. A per-engine copy is what
// a shared seam is supposed to make impossible: an engine whose skills materialize through its OWN code
// cannot prove the seam works, it can only prove its own copy does.
//
// A file's MODE comes from PackageFile.Mode — the export's declaration, which
// travelled from the package sidecar's `executable:` list through the signed
// manifest. It is never read off the filesystem here or anywhere below:
// a mode bit is not portable, the package digest deliberately excludes it, and
// the declaration is the whole of what a publisher said about executability
// (see content.SkillFile.Mode and content.DeclaredExecutable).
func WriteManagedSkillPackages(files safefs.Root, skillsDir string, skills []SkillExport, opts ...ManagedWriteOption) ([]string, error) {
	return WriteManagedPackageFiles(files, skillsDir, skills,
		func(s SkillExport) bool { return s.Enabled },
		func(s SkillExport) string { return s.Name },
		func(s SkillExport) ([]PackageFile, error) {
			out := make([]PackageFile, len(s.Files))
			for i, f := range s.Files {
				out[i] = PackageFile{
					// The export's paths are package-relative ("SKILL.md",
					// "scripts/run.sh"); the package's own directory is joined
					// on HERE, once, so no engine re-derives it.
					RelPath: path.Join(s.Name, f.RelPath),
					Content: f.Content,
					Mode:    f.Mode,
				}
			}
			return out, nil
		},
		opts...)
}

// Compile-time contract.
var _ Approach = (*ManagedSkillPackagesDelivery)(nil)
