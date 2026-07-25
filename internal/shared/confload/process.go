package confload

import "sync"

// ProcessOverrides is the process-wide Overrides holder: the ONE place a
// binary installs the Overrides it captured (typically via ReadOverrides, in
// its root command's PersistentPreRun, right after flags are parsed) so every
// later config load in the process — however deep, however many call sites —
// can find it without threading a *pflag.FlagSet or an env snapshot through
// every intermediate call. This lives here (not in internal/config) so a
// caller with no dependency on internal/config at all — internal/testsupport,
// most importantly, which must be able to reset it for test isolation without
// creating an import cycle back through internal/config's own test files —
// can still reach it. internal/config's SetOverrides/currentOverrides/
// ResetOverrides are thin wrappers around this for its own callers'
// convenience (SetOverrides additionally invalidates its ambient memo, which
// only internal/config knows how to do).
//
// A production binary calls SetProcessOverrides exactly once per process; a
// test that wants a specific Overrides resolved for ONE load without
// mutating this shared state uses internal/config's WithOverrides option
// instead.
var (
	processMu  sync.Mutex
	processVal Overrides
)

// SetProcessOverrides installs o as the process-wide Overrides every
// subsequent resolution (that doesn't explicitly override it, e.g. via
// internal/config's WithOverrides test seam) consults.
func SetProcessOverrides(o Overrides) {
	processMu.Lock()
	processVal = o
	processMu.Unlock()
}

// ProcessOverrides returns the Overrides SetProcessOverrides last installed
// (the zero Overrides{} — "none installed" — if it was never called).
func ProcessOverrides() Overrides {
	processMu.Lock()
	defer processMu.Unlock()
	return processVal
}

// ResetProcessOverrides clears the process-wide Overrides back to the zero
// value. Test isolation helpers (internal/testsupport.Isolate) call this so a
// real environment variable — or a value a PRIOR test's call into production
// code installed — can never leak into a test that never itself installs
// overrides.
func ResetProcessOverrides() {
	SetProcessOverrides(Overrides{})
}
