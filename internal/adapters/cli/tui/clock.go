package tui

import "time"

// Clock is the split view's only source of time for its own decisions (the
// quiet gate, arming, the bell's rate limit, the notes' expiry), and the
// presenters' that drive it, so a test can force every interleaving instead
// of sleeping into one.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine after d; stop cancels it and
	// reports whether it did so before f started.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

// RealClock is real time.
type RealClock struct{}

// Now is time.Now.
func (RealClock) Now() time.Time { return time.Now() }

// AfterFunc is time.AfterFunc.
func (RealClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// PresentPolicy times the approvals view's focus locks. Zero fields take the
// defaults; values below a floor are raised to it — the locks exist so a key
// typed for the engine cannot decide an approval, and a duration short enough
// to defeat that is not a configuration, it is the lock switched off.
type PresentPolicy struct {
	// QuietFor is how long the human's input must have been idle before a
	// summoned approval may take the pane. Default 1.5s, floor 500ms.
	QuietFor time.Duration
	// ArmFor is the inert window after the approvals view first shows,
	// restarting on every key received during it; keys in the window are
	// discarded. Default 750ms, floor 300ms.
	ArmFor time.Duration
}

const (
	defaultQuietFor = 1500 * time.Millisecond
	floorQuietFor   = 500 * time.Millisecond
	defaultArmFor   = 750 * time.Millisecond
	floorArmFor     = 300 * time.Millisecond
)

// Normalized is p with its defaults and floors applied: the timings the
// locks actually run with.
func (p PresentPolicy) Normalized() PresentPolicy {
	return PresentPolicy{
		QuietFor: withFloor(p.QuietFor, defaultQuietFor, floorQuietFor),
		ArmFor:   withFloor(p.ArmFor, defaultArmFor, floorArmFor),
	}
}

func withFloor(d, def, floor time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return max(d, floor)
}
