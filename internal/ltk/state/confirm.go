package state

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
)

// ConfirmByRepeat implements the "run it again to permit" override against the
// store at stateFile on fs. On the first denial of command it arms a pending
// entry (confirmable only in the band [now+delay, now+window]) and returns the
// denial with an added, deliberately stern hint. A later identical re-run is
// allowed only once the delay has elapsed and before the window closes; an
// over-eager repeat inside the delay is refused with a sharper rebuke and does
// NOT reset the timer. The bool reports whether this call consumed a confirmation.
//
// Callers must only invoke this for a *confirmable* denial; an inviolate rule is
// gated out upstream (engine.Response.Confirmable), so repeating it never reaches
// here. now is injected so callers (and tests) control the clock. This is an
// escape hatch, not a security control — the delay raises the cost of a reflexive
// bypass, it does not prevent a determined one.
//
// The returned error is whatever reading or writing the store reported (nil on
// success). It never changes the decision above; it exists so a caller can
// notice that persistence is broken. Without it, a failure to arm silently
// converted "the override is armed" into a no-op, and the very next identical
// repeat was denied again — forever — while the message the agent had just
// been shown ("run the exact same command again ... to proceed") promised the
// opposite.
func ConfirmByRepeat(fs afero.Fs, resp engine.Response, command, stateFile string, now time.Time, delay, window time.Duration) (engine.Response, bool, error) {
	key := strings.TrimSpace(command)
	st := Open(fs, stateFile)

	if st.Armed(key, now) {
		// A live override from an earlier denial.
		if st.Ready(key, now) {
			st.Clear(key)
			err := st.Save(now)
			return engine.Response{Allow: true}, true, errors.Join(st.LoadError(), err)
		}
		// Repeated inside the delay: keep the existing arm (don't reset the
		// timer) and push back harder. Nothing in st.Pending changed on this
		// path (U076-F07), so there is nothing to persist — do NOT call Save
		// here: it would perform a real atomic write (temp file + rename) for
		// zero state delta, on every over-eager repeat.
		resp.Reason = tooEarlyReason(resp.Reason, st.RemainingDelay(key, now))
		return resp, false, st.LoadError()
	}

	// First denial (or the previous arm expired): arm and explain.
	st.Arm(key, now, delay, window)
	err := st.Save(now)
	resp.Reason = armReason(resp.Reason, int(delay.Seconds()), int(window.Seconds()))
	return resp, false, errors.Join(st.LoadError(), err)
}

// armReason appends the override instructions to a fresh denial. The tone is
// deliberately discouraging: repeating is a logged, intentional escape hatch, not
// a retry button — the agent should prefer the suggested alternative.
func armReason(reason string, delaySec, windowSec int) string {
	base := strings.TrimRight(reason, "\n")
	const lead = "\n\nSTOP and re-evaluate. The user has set this rule because they do NOT want this run — they want you to use the suggested alternative above. Switch to it. Do not just re-run this to push past the rule: a repeat is a deliberate, logged override, not a retry button. The ONLY valid reason to override is that the alternative genuinely cannot do what you need — not that it is slower or less convenient."
	if delaySec > 0 {
		return base + lead + fmt.Sprintf(" If that is truly the case, wait at least %ds, then run the exact same command again (within %ds) to proceed.", delaySec, windowSec)
	}
	return base + lead + fmt.Sprintf(" If that is truly the case, run the exact same command again within %ds to proceed.", windowSec)
}

// tooEarlyReason rebukes an immediate repeat made before the delay elapsed.
func tooEarlyReason(reason string, remainingSec int) string {
	base := strings.TrimRight(reason, "\n")
	return base + fmt.Sprintf("\n\nYou repeated this almost immediately instead of following the guidance — that is exactly the reflexive bypass this rule guards against. Stop and use the suggested alternative. If you genuinely have a reason to override, wait %ds more, then run the exact same command again.", remainingSec)
}
