package agent

import (
	"path"

	"github.com/spf13/afero"
)

// This file is the skills-surface analog of managed_commands.go: the shared
// deliver/cleanup body for engines whose skill package exports are
// reconciled TREES written by a manifest-scoped writer. Only WHICH writer, at
// WHICH path, is engine-specific; that is the injected write func. Cloned from
// ManagedCommandsDelivery per the skill/command split plan §3.4 ("new export
// type … ManagedSkillPackagesDelivery cloned from the ManagedCommandsDelivery
// pattern").
//
// It also holds WriteManagedSkillPackages, the write half that same "only the
// path is engine-specific" claim implies: every engine's WriteSkillFiles was a
// verbatim copy of one WriteManagedPackageFiles call, differing ONLY in the
// target directory and the manifest name.

// ManagedSkillPackagesDelivery is the shared skills Delivery for engines whose
// skill exports are managed package trees: on Deliver it writes every enabled
// package, and its cleanup reverts exactly the manifest-tracked file set by
// re-writing with no packages. Managed skill files are cwd-rooted with no
// SharedRealization (no engine has an out-of-cwd flag for a skill package), so
// a SHARED-cwd delivery of it falls back to the loud well-known write; it
// carries an engine/surface name (e.g. "claude/skills") and self-describes for
// that fallback's warning via UnsafeInfo.
type ManagedSkillPackagesDelivery struct {
	name   string
	skills []SkillExport
	write  func(dir string, skills []SkillExport) error
}

// NewManagedSkillPackagesDelivery builds a managed-skills Delivery from its
// engine/surface name (e.g. "claude/skills", for the shared-cwd fallback
// warning), the enabled exports, and the engine's manifest-scoped
// skill-package writer, bound so that write(dir, skills) materializes every
// package under dir and write(dir, nil) reverts exactly the managed set.
func NewManagedSkillPackagesDelivery(name string, skills []SkillExport, write func(dir string, skills []SkillExport) error) *ManagedSkillPackagesDelivery {
	return &ManagedSkillPackagesDelivery{name: name, skills: skills, write: write}
}

// UnsafeInfo returns the engine/surface identity for the DeliverShared
// fallback's warning (ResolvedSelection.deliverOneShared's unsafeNamed check,
// cells.go), making a managed-skills delivery self-describing when it lands in
// a shared cwd.
func (s *ManagedSkillPackagesDelivery) UnsafeInfo() string { return s.name }

// Kind reports this as the skills surface.
func (s *ManagedSkillPackagesDelivery) Kind() SurfaceKind { return SurfaceSkills }

// Deliver writes the enabled skill package exports into dir via the injected
// writer and returns a handle whose Cleanup reverts exactly the
// manifest-tracked set (a re-write with no packages).
func (s *ManagedSkillPackagesDelivery) Deliver(dir string) (Delivered, error) {
	if err := s.write(dir, s.skills); err != nil {
		return nil, err
	}
	return DeliveredFunc(func() error { return s.write(dir, nil) }), nil
}

// WriteManagedSkillPackages materializes every ENABLED skill package under
// skillsDir — `<skillsDir>/<skill name>/SKILL.md` plus every sibling file the
// package carries, each at the mode its export DECLARES — tracking exactly the
// files it wrote in `<skillsDir>/<manifestName>` so a later call with fewer (or
// no) skills reverts precisely that set and nothing a user put there.
//
// This is the ONE skill-materialization body in the tree. claude, codex,
// antigravity, kiro and opencode each used to carry a byte-identical copy of
// it, differing only in the two arguments this function takes; the mock engine
// would have been a sixth. Those copies are what a shared seam is supposed to
// make impossible: an engine whose skills materialize through its OWN code
// cannot prove the seam works, it can only prove its own copy does.
//
// A file's MODE comes from PackageFile.Mode — the export's declaration, which
// travelled from the package sidecar's `executable:` list through the signed
// manifest. It is never read off the filesystem here or anywhere below:
// a mode bit is not portable, the package digest deliberately excludes it, and
// the declaration is the whole of what a publisher said about executability
// (see content.SkillFile.Mode and content.DeclaredExecutable).
func WriteManagedSkillPackages(fs afero.Fs, skillsDir, manifestName string, skills []SkillExport) error {
	return WriteManagedPackageFiles(fs, skillsDir, manifestName, skills,
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
	)
}

// Compile-time contract.
var (
	_ Delivery       = (*ManagedSkillPackagesDelivery)(nil)
	_ KindedDelivery = (*ManagedSkillPackagesDelivery)(nil)
)
