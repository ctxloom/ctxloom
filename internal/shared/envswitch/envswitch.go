// Package envswitch reads ctxloom's boolean process switches — the CTXLOOM_*
// environment variables that select a mode before any flag is parsed
// (CTXLOOM_DEGRADED, CTXLOOM_NO_COMPANIONS, CTXLOOM_VERBOSE).
//
// It exists because those switches must agree on what "on" means. Comparing
// against a single literal makes every other spelling a silent no-op: an
// operator who exports the variable as `true` gets none of the behaviour, no
// warning, and nothing to look at. Worse, one site comparing against "1" while
// another accepts more spellings makes the SAME variable half-on.
//
// This package deliberately depends on nothing but the standard library so
// every layer — the binary's main, the coordinator, the runners — can read a
// switch the same way.
package envswitch

import (
	"os"
	"strings"
)

// onValues and offValues are the accepted spellings, matched case-insensitively
// after trimming surrounding space. Anything else is neither: see On.
var (
	onValues  = map[string]bool{"1": true, "t": true, "true": true, "y": true, "yes": true, "on": true}
	offValues = map[string]bool{"": true, "0": true, "f": true, "false": true, "n": true, "no": true, "off": true}
)

// On reports whether the named switch is on, and returns the raw value when it
// is set to something no accepted spelling covers.
//
// An unrecognized value is reported rather than guessed at, and is treated as
// OFF: these switches all widen what the process is allowed to do (degrade
// past a fatal finding, skip companion probing, raise log volume), so the
// conservative reading of an unparseable value is "not asked for". The caller
// is expected to say so — an ignored switch the operator believes is set is
// indistinguishable from the feature not working.
//
// An unset or empty variable is off and is NOT unrecognized: that is the
// ordinary case, not a mistake.
func On(name string) (on bool, unrecognized string) {
	raw := os.Getenv(name)
	v := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case onValues[v]:
		return true, ""
	case offValues[v]:
		return false, ""
	default:
		return false, raw
	}
}
