package agent

// This file is the skills-surface analog of managed_commands.go: the shared
// deliver/cleanup body for engines whose skill package exports are
// reconciled TREES written by a manifest-scoped writer. Only WHICH writer, at
// WHICH path, is engine-specific; that is the injected write func. Cloned from
// ManagedCommandsDelivery per the skill/command split plan §3.4 ("new export
// type … ManagedSkillPackagesDelivery cloned from the ManagedCommandsDelivery
// pattern").

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
	return deliveredFunc(func() error { return s.write(dir, nil) }), nil
}

// Compile-time contract.
var (
	_ Delivery       = (*ManagedSkillPackagesDelivery)(nil)
	_ KindedDelivery = (*ManagedSkillPackagesDelivery)(nil)
)
