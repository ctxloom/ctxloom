package agent

// Delivered is the handle returned from delivering one surface of a loadout: it
// owns the cleanup that undoes that delivery. Each mechanism strategy returns a
// Delivered whose Cleanup reverses exactly what the strategy did — remove the
// written file, reconcile a settings edit, deregister an MCP server, or no-op
// for an in-band delivery that leaves nothing behind.
type Delivered interface {
	// Cleanup undoes the delivery this handle represents (rm file / reconcile /
	// deregister / no-op).
	Cleanup() error
}
