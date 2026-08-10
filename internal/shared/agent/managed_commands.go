package agent

// This file hoists the shared body of every engine's MANAGED-COMMAND
// delivery out of the per-backend surfaces.go files. The engines whose
// command (slash-command) exports are reconciled files written by a
// manifest-scoped writer — kiro (.kiro/skills/)
// and codex ($CODEX_HOME/prompts, the vendor's own directory name — codex's
// export is still a command, not a genuine Agent Skill) — both shared one
// identical Deliver: write the
// enabled exports, then revert exactly the managed set on cleanup by re-writing
// with none. Only WHICH writer, at WHICH path, is engine-specific; that is the
// injected write func. (claude's commands ride a different writer that owns its
// own cleanup, so they are not modeled here.)

// ManagedCommandsDelivery is the shared commands Delivery for engines whose
// command exports are managed files: on Deliver it writes the enabled
// exports, and its cleanup reverts exactly the manifest-tracked set by
// re-writing with no exports. Managed command files are cwd-rooted with no
// SharedRealization, so a SHARED-cwd delivery of it falls back to the loud
// well-known write; it carries an engine/surface name (e.g. "codex/commands")
// and self-describes for that fallback's warning via UnsafeInfo.
type ManagedCommandsDelivery struct {
	name     string
	commands []CommandExport
	write    func(dir string, commands []CommandExport) error
}

// NewManagedCommandsDelivery builds a managed-commands Delivery from its
// engine/surface name (e.g. "kiro/commands", for the shared-cwd fallback
// warning), the enabled exports, and the engine's manifest-scoped
// command-file writer, bound so that write(dir, commands) materializes the
// exports under dir and write(dir, nil) reverts exactly the managed set.
func NewManagedCommandsDelivery(name string, commands []CommandExport, write func(dir string, commands []CommandExport) error) *ManagedCommandsDelivery {
	return &ManagedCommandsDelivery{name: name, commands: commands, write: write}
}

// UnsafeInfo returns the engine/surface identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go),
// making a managed-commands delivery self-describing when it lands in a shared cwd.
func (s *ManagedCommandsDelivery) UnsafeInfo() string { return s.name }

// Kind reports this as the commands surface (codex/kiro both share it).
func (s *ManagedCommandsDelivery) Kind() SurfaceKind { return SurfaceCommands }

// Deliver writes the enabled command exports into dir via the injected writer
// and returns a handle whose Cleanup reverts exactly the manifest-tracked set
// (a re-write with no exports).
func (s *ManagedCommandsDelivery) Deliver(dir string) (Delivered, error) {
	if err := s.write(dir, s.commands); err != nil {
		return nil, err
	}
	return DeliveredFunc(func() error { return s.write(dir, nil) }), nil
}

// Compile-time contract.
var (
	_ Delivery       = (*ManagedCommandsDelivery)(nil)
	_ KindedDelivery = (*ManagedCommandsDelivery)(nil)
	_ Delivered      = DeliveredFunc(nil)
)
