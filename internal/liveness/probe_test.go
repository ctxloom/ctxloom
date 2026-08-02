package liveness_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
)

// A probe declaring a runtime must not be consulted for a different one, or
// "evidence of life" stops being runtime-polymorphic and becomes a guess.
func TestProbes_AreSelectedByRuntime(t *testing.T) {
	testHome(t)
	const harp = "runtime-selection"
	healthySession(t, harp)
	var hostCalls, universalCalls, containerCalls int
	now := time.Now()
	m := liveness.New(liveness.Options{
		Now: func() time.Time { return now },
		Probes: []liveness.Probe{
			liveness.ProbeFunc{RuntimeName: "host", Fn: func(context.Context, liveness.Target) liveness.ProcState {
				hostCalls++
				return liveness.ProcState{Observed: true, Alive: true}
			}},
			liveness.ProbeFunc{RuntimeName: "container", Fn: func(context.Context, liveness.Target) liveness.ProcState {
				containerCalls++
				return liveness.ProcState{Observed: true, Alive: true}
			}},
			liveness.ProbeFunc{RuntimeName: "", Fn: func(context.Context, liveness.Target) liveness.ProcState {
				universalCalls++
				return liveness.ProcState{Detail: "universal"}
			}},
		},
	})
	m.Assess(context.Background(), liveness.Target{
		Harp: harp, Runtime: "container", StartedAt: now.Add(-time.Hour),
		LastActivity: now, TranscriptPath: transcriptPath(t, harp),
	})
	assert.Equal(t, 0, hostCalls, "a host probe must not be asked about a container")
	assert.Equal(t, 1, containerCalls)
	assert.Equal(t, 1, universalCalls, "an empty Runtime() means universal")
}

// One probe seeing the target alive must outrank another's inability to find
// it: a wrong DIED verdict is worse than a missed one.
func TestProbeMerge_AliveOutranksNotFound(t *testing.T) {
	testHome(t)
	const harp = "merge-alive"
	healthySession(t, harp)
	now := time.Now()
	m := liveness.New(liveness.Options{
		Now: func() time.Time { return now },
		Probes: []liveness.Probe{
			liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
				return liveness.ProcState{Observed: true, Alive: false, Detail: "pid gone"}
			}},
			liveness.ProbeFunc{Fn: func(context.Context, liveness.Target) liveness.ProcState {
				return liveness.ProcState{Observed: true, Alive: true, Detail: "runner heartbeat 1s ago"}
			}},
		},
	})
	rep := m.Assess(context.Background(), liveness.Target{
		Harp: harp, StartedAt: now.Add(-time.Hour), LastActivity: now,
		TranscriptPath: transcriptPath(t, harp),
	})
	require.True(t, rep.Evidence.Proc.Alive, "one positive sighting outranks a miss")
	assert.NotEqual(t, liveness.StateDied, rep.State)
	assert.Contains(t, rep.Evidence.Proc.Detail, "pid gone")
	assert.Contains(t, rep.Evidence.Proc.Detail, "runner heartbeat 1s ago")
}

// A zero-value Thresholds must fall back to the tuned defaults rather than
// silently degrading the ladder into firing on everything, and overriding one
// field must not blank the rest back to zero. Proven through Assess's
// observable behaviour rather than a getter: Monitor no longer exposes its
// normalized Thresholds, so the fallback is pinned by what a
// zero-StartGrace ladder would do differently — condemn a 1-minute-old
// launch as StateStalled instead of respecting the (normalized) 5-minute
// launch grace.
func TestThresholds_ZeroValueNormalizesToDefaults(t *testing.T) {
	testHome(t)
	now := time.Now()
	newTarget := func(harp string) liveness.Target {
		return liveness.Target{
			Harp: harp, StartedAt: now.Add(-time.Minute),
			TranscriptPath: transcriptPath(t, harp), // no file written: absence, not error
		}
	}

	zero := liveness.New(liveness.Options{Now: func() time.Time { return now }})
	rep := zero.Assess(context.Background(), newTarget("zero-thresholds"))
	assert.Equal(t, liveness.StateStarting, rep.State,
		"a zero-value StartGrace would have condemned this as stalled: reason=%q", rep.Reason)

	// Overriding QuietGrace alone must not blank StartGrace back to zero.
	custom := liveness.New(liveness.Options{
		Now:        func() time.Time { return now },
		Thresholds: liveness.Thresholds{QuietGrace: 30 * time.Second},
	})
	rep = custom.Assess(context.Background(), newTarget("custom-thresholds"))
	assert.Equal(t, liveness.StateStarting, rep.State,
		"overriding one threshold must not blank the rest: reason=%q", rep.Reason)
}
