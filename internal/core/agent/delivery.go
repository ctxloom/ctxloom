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

// DeliveredFunc adapts a cleanup closure to Delivered, for a Delivery whose
// reversal is a single function call.
type DeliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f DeliveredFunc) Cleanup() error { return f() }

// SurfacePersistsAfterExit is the Delivered handle for a PROJECT SURFACE: its
// reversal is deliberately a no-op, so the surface stays on disk when the run
// ends. Startup reconciles it; `ctxloom clean` and `ctxloom manage uninstall`
// are the commands that remove it.
//
// WHY, and it is not merely that exit-removal was redundant. Exit cleanup NEVER
// RUNS on SIGKILL, on a crash, or on a container stop, so leftover surfaces
// have to be tolerated regardless — which makes startup reconciliation
// load-bearing whatever else is true. Keeping exit-removal as well added no
// safety; it made post-session state depend on HOW THE PROCESS ENDED. Sometimes
// the context file was there afterwards, sometimes not, and nothing said which.
//
// It also collapses a confusion that cost real time: two writers shared one
// path with opposite lifecycles. A run wrote at launch and removed at its
// end (ephemeral); materialize wrote and never removed (persistent). Same file, two
// ownership models, and nothing on the file to say which had produced it.
//
// THIS IS FOR PROJECT SURFACES ONLY. Per-session SCRATCH keeps its teardown and
// must not be given this handle: the session tree is TierLocal so `clean` never
// touches it by design, its ephemeral subdirectory is not its own paths.Layout
// entry so clean cannot see it even in principle, and startup does not
// reconcile it because a new session means a new harp and a new directory.
// Give scratch this handle and it accumulates forever with nothing reaping it.
var SurfacePersistsAfterExit Delivered = DeliveredFunc(func() error { return nil })
