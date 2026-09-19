package agent

import "fmt"

// LaunchForm is the DECLARED form a run's managed surfaces take: where this
// run's config lands, and therefore what the engine is told about it on argv.
//
// It replaces the SkipSetup flag, which was not a mode but a BYPASS: it skipped
// Setup outright, so nothing was delivered AND nothing was presented, and the
// context had to reach the engine by a second route with its own assembler. One
// concept, two implementations, and only one of them warned about an oversize
// context — so a run could deliver one in silence by taking the other route.
//
// A form is RESOLVED ONCE, host-side, by whoever knows the run's shape, and
// carried to the plugin already decided. The plugin's Setup constructs from the
// form it was handed and its Execute emits what Setup resolved; no argv site
// re-derives the decision from the inputs. That is what makes an invalid
// combination unrepresentable rather than a branch nobody wrote.
type LaunchForm int

const (
	// LaunchFormDeliver writes this run's managed surfaces into its OWN cell —
	// the well-known files in an isolated cwd, or the session's private
	// out-of-cwd scratch for a top-level shared run. It is what SkipSetup:false
	// did.
	LaunchFormDeliver LaunchForm = iota
	// LaunchFormPresent names the surfaces the session ALREADY delivered and
	// writes nothing: every Deliver returns a nil handle, the seam's own
	// "nothing was written" convention, and Present — which is pure with
	// respect to the filesystem — supplies the argv.
	//
	// It is the form a member that shares the project cwd takes. Writing
	// per-member config there would clobber the one shared surface, which is
	// exactly the rationale the old code gave for bypassing delivery
	// altogether; using the existing surface is what that rationale actually
	// asks for.
	//
	// A surface that is NOT there REFUSES (ErrAbsentSharedSurface). Falling
	// back to writing it, or to a second delivery route, would reintroduce the
	// silent degrade this form exists to remove.
	LaunchFormPresent
	// LaunchFormMinimal declares NO managed surfaces at all: no hooks, no
	// commands, no project memory, no context file. It is the headless posture
	// — distillation, compaction, task triage — where the run is a bare model
	// call and the engine is stripped back to one. Setup resolves the engine's
	// declared minimal launch posture (MinimalLaunch) and delivers nothing.
	//
	// It is NOT LaunchFormPresent with an empty surface set: Present asserts a
	// session's surfaces exist and names them, Minimal asserts there are none.
	// Collapsing them would make a headless run refuse on a fresh project.
	LaunchFormMinimal
)

// String renders the form for diagnostics and for the wire enum's names.
func (f LaunchForm) String() string {
	switch f {
	case LaunchFormDeliver:
		return "deliver"
	case LaunchFormPresent:
		return "present"
	case LaunchFormMinimal:
		return "minimal"
	default:
		return fmt.Sprintf("LaunchForm(%d)", int(f))
	}
}

// LaunchFormForCell selects the form a MEMBER of a fan-out takes from the cell
// it landed in. This is the whole of the shared-vs-isolated decision, made
// once: an isolated cell has a private cwd, so the member's own config is
// written into it; a shared cell has exactly one surface set and the member
// rides it.
//
// It is deliberately not a method on CellKind. Cell kind does not IMPLY a form
// — a top-level run is CellKindShared and delivers its own surfaces into the
// session scratch, because it is the run that owns them. This is the rule for a
// caller that is riding someone else's session, and naming it as a function
// keeps that caller visible.
func LaunchFormForCell(cell CellKind) LaunchForm {
	if cell == CellKindShared {
		return LaunchFormPresent
	}
	return LaunchFormDeliver
}
