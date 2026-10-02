package agent

import "github.com/ctxloom/ctxloom/internal/core/present"

// ManagedCommandsDelivery is the shared commands form for engines whose
// command exports are managed files beneath the project root: where they are
// presented. Managed command files are cwd-rooted with no out-of-cwd form.
type ManagedCommandsDelivery struct {
	rel string // the commands dir beneath the project root, for Present
}

// NewManagedCommandsDelivery builds a managed-commands form from the commands
// directory relative to the project root — what Present declares.
func NewManagedCommandsDelivery(rel string) *ManagedCommandsDelivery {
	return &ManagedCommandsDelivery{rel: rel}
}

// Present declares the commands directory beneath the advised project root.
// No flag: an engine finds its command files by name.
func (s *ManagedCommandsDelivery) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot(s.rel).Build()
}

// Compile-time contract.
var (
	_ Approach  = (*ManagedCommandsDelivery)(nil)
	_ Delivered = DeliveredFunc(nil)
)
