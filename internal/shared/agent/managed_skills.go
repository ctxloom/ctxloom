package agent

// This file hoists the shared body of every engine's MANAGED-COMMAND skills
// delivery out of the per-backend surfaces.go files. The engines whose skill
// (slash-command / prompt) exports are reconciled files written by a
// manifest-scoped writer — antigravity (.agents/skills/), kiro (.kiro/skills/),
// and codex ($CODEX_HOME/prompts) — all shared one identical Deliver: write the
// enabled exports, then revert exactly the managed set on cleanup by re-writing
// with none. Only WHICH writer, at WHICH path, is engine-specific; that is the
// injected write func. (claude's skills ride a different writer that owns its own
// cleanup, so they are not modeled here.)

// ManagedSkillsDelivery is the shared skills Delivery for engines whose skill
// exports are managed files: on Deliver it writes the enabled exports, and its
// cleanup reverts exactly the manifest-tracked set by re-writing with no exports.
// It is Delivery-ONLY — managed skill files are cwd-rooted, so a ManagedSkills
// delivery reaches a SharedCell only through the loud Unsafe hatch. It carries an
// engine/surface name (e.g. "codex/skills") so it self-describes for that hatch
// (UnsafeInfo).
type ManagedSkillsDelivery struct {
	name   string
	skills []CommandExport
	write  func(dir string, skills []CommandExport) error
}

// NewManagedSkillsDelivery builds a managed-skills Delivery from its engine/surface
// name (e.g. "kiro/skills", for the Unsafe warning), the enabled exports, and the
// engine's manifest-scoped command-file writer, bound so that write(dir, skills)
// materializes the exports under dir and write(dir, nil) reverts exactly the
// managed set.
func NewManagedSkillsDelivery(name string, skills []CommandExport, write func(dir string, skills []CommandExport) error) *ManagedSkillsDelivery {
	return &ManagedSkillsDelivery{name: name, skills: skills, write: write}
}

// UnsafeInfo returns the engine/surface identity for the Unsafe warning, making a
// managed-skills delivery self-describing when the loud hatch delivers it into a
// shared cwd.
func (s *ManagedSkillsDelivery) UnsafeInfo() string { return s.name }

// Kind reports this as the skills surface (codex/antigravity/kiro all share it).
func (s *ManagedSkillsDelivery) Kind() SurfaceKind { return SurfaceSkills }

// Deliver writes the enabled skill exports into dir via the injected writer and
// returns a handle whose Cleanup reverts exactly the manifest-tracked set (a
// re-write with no exports).
func (s *ManagedSkillsDelivery) Deliver(dir string) (Delivered, error) {
	if err := s.write(dir, s.skills); err != nil {
		return nil, err
	}
	return deliveredFunc(func() error { return s.write(dir, nil) }), nil
}

// deliveredFunc adapts a cleanup closure to Delivered so a shared delivery can
// return its teardown inline without a bespoke handle type.
type deliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f deliveredFunc) Cleanup() error { return f() }

// Compile-time contract: a managed-skills delivery is Delivery-ONLY (no
// out-of-cwd flag), so it can never enter a SharedCell except through Unsafe —
// which it satisfies as a self-describing UnsafeSurface.
var (
	_ Delivery        = (*ManagedSkillsDelivery)(nil)
	_ UnsafeSurface   = (*ManagedSkillsDelivery)(nil)
	_ KindedDelivery  = (*ManagedSkillsDelivery)(nil)
	_ Delivered       = deliveredFunc(nil)
)
