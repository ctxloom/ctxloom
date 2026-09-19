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
	// respect to the filesystem — supplies the argv. A surface that is NOT
	// there REFUSES (ErrAbsentSharedSurface).
	//
	// No host launch produces it any more: every launch owns a harp and
	// delivers its own surfaces (coordgrpc.EncodeLaunch). The runner still
	// decodes it, as a wire value, until the runner reads the delivery plan
	// instead of a form.
	LaunchFormPresent
	// LaunchFormMinimal declares NO managed surfaces at all. No host launch
	// produces it any more: an internal one-shot (distillation, compaction,
	// triage) is a real session with its own surfaces. The runner still
	// decodes it, as a wire value, until the runner reads the delivery plan
	// instead of a form.
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
