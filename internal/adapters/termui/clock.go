package termui

import "time"

// Clock is the layer's only source of time for its own decisions (the quiet
// gate, arming, the bell's rate limit, the nudge's wiggle separation), so a
// test can force every interleaving instead of sleeping into one.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f on its own goroutine after d; stop cancels it and
	// reports whether it did so before f started.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// PresentPolicy times the approvals modal's focus locks. Zero fields take the
// defaults; values below a floor are raised to it — the locks exist so a key
// typed for the engine cannot decide an approval, and a duration short enough
// to defeat that is not a configuration, it is the lock switched off.
type PresentPolicy struct {
	// QuietFor is how long stdin must have been idle before the modal may
	// take focus. Default 1.5s, floor 500ms.
	QuietFor time.Duration
	// ArmFor is the inert window after the modal's first frame, restarting on
	// every key received during it; keys in the window are discarded. Default
	// 750ms, floor 300ms.
	ArmFor time.Duration
}

const (
	defaultQuietFor = 1500 * time.Millisecond
	floorQuietFor   = 500 * time.Millisecond
	defaultArmFor   = 750 * time.Millisecond
	floorArmFor     = 300 * time.Millisecond
)

func (p PresentPolicy) normalized() PresentPolicy {
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
