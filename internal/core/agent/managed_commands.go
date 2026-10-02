package agent

import "github.com/ctxloom/ctxloom/internal/core/present"

// ManagedCommandsDelivery is the shared commands form for engines whose
// command exports are managed files beneath the project root: where they are
// presented. Managed command files are cwd-rooted with no out-of-cwd form; it
// carries an engine/surface name (e.g. "mock/commands") and self-describes
// via UnsafeInfo.
type ManagedCommandsDelivery struct {
	name string
	rel  string // the commands dir beneath the project root, for Present
}

// NewManagedCommandsDelivery builds a managed-commands form from its
// engine/surface name (e.g. "mock/commands") and the commands directory
// relative to the project root — what Present declares.
func NewManagedCommandsDelivery(name, rel string) *ManagedCommandsDelivery {
	return &ManagedCommandsDelivery{name: name, rel: rel}
}

// Present declares the commands directory beneath the advised project root.
// No flag: an engine finds its command files by name.
func (s *ManagedCommandsDelivery) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(s.rel).Build()
}

// UnsafeInfo returns the engine/surface identity for the DeliverShared fallback's
// warning (ResolvedSelection.deliverOneShared's unsafeNamed check, cells.go),
// making a managed-commands delivery self-describing when it lands in a shared cwd.
func (s *ManagedCommandsDelivery) UnsafeInfo() string { return s.name }

// Kind reports this as the commands surface.
func (s *ManagedCommandsDelivery) Kind() SurfaceKind { return SurfaceCommands }

// Compile-time contract.
var (
	_ Approach  = (*ManagedCommandsDelivery)(nil)
	_ Delivered = DeliveredFunc(nil)
)
