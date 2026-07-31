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
//
// o's maps are COPIED in. Storing them directly would leave the caller
// holding live references to state this mutex is supposed to own, so the
// lock would cover the struct header and nothing reachable through it — a
// caller that later wrote to the map it passed would race every reader with
// no lock in sight and no way to tell from this file that it could.
func SetProcessOverrides(o Overrides) {
	processMu.Lock()
	processVal = Overrides{Env: cloneFlat(o.Env), Flags: cloneFlat(o.Flags)}
	processMu.Unlock()
}

// ProcessOverrides returns the Overrides SetProcessOverrides last installed
// (the zero Overrides{} — "none installed" — if it was never called).
//
// The maps are copies, for the same reason SetProcessOverrides copies on the
// way in: returning the stored maps would publish them outside the mutex, so
// the synchronization would be partial in a way nothing at the call site
// could reveal. Copying at both ends makes the maps unreachable except under
// the lock, which is what the mutex was always meant to mean. Both are flat
// name->value maps of an invocation's own overrides, so a copy is cheap and
// almost always empty.
func ProcessOverrides() Overrides {
	processMu.Lock()
	defer processMu.Unlock()
	return Overrides{Env: cloneFlat(processVal.Env), Flags: cloneFlat(processVal.Flags)}
}

// cloneFlat copies one of Overrides' flat maps, preserving nil (the zero
// Overrides{} must stay distinguishable as "none installed" — Stamp and every
// len() check treat a nil and an empty map alike, but a caller comparing
// against the zero value should not see one silently become the other).
func cloneFlat(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ResetProcessOverrides clears the process-wide Overrides back to the zero
// value. Test isolation helpers (internal/testsupport.Isolate) call this so a
// real environment variable — or a value a PRIOR test's call into production
// code installed — can never leak into a test that never itself installs
// overrides.
func ResetProcessOverrides() {
	SetProcessOverrides(Overrides{})
}
