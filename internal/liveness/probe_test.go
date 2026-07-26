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
			liveness.ProbeFunc{Name: "host", Fn: func(context.Context, liveness.Target) liveness.ProcState {
				hostCalls++
				return liveness.ProcState{Observed: true, Alive: true}
			}},
			liveness.ProbeFunc{Name: "container", Fn: func(context.Context, liveness.Target) liveness.ProcState {
				containerCalls++
				return liveness.ProcState{Observed: true, Alive: true}
			}},
			liveness.ProbeFunc{Name: "", Fn: func(context.Context, liveness.Target) liveness.ProcState {
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

// UnobservableProbe must be a DECLARED gap carrying a reason, never something
// that reads the same as a healthy answer.
func TestUnobservableProbe_DeclaresTheGap(t *testing.T) {
	p := liveness.UnobservableProbe{RuntimeName: "container", Why: "no exec into the container from here"}
	st := p.Inspect(context.Background(), liveness.Target{Runtime: "container"})
	assert.Equal(t, "container", p.Runtime())
	assert.False(t, st.Observed)
	assert.Equal(t, "no exec into the container from here", st.Detail)
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

// Watch must actually call back — a poller that never emits is the same
// silent no-op in a different costume.
func TestWatch_EmitsEveryTargetUntilCancelled(t *testing.T) {
	testHome(t)
	const harp = "watched"
	stuckSpawner{harp: harp, engine: "claude", deliveries: 5}.loop(t)
	now := time.Now()
	m := liveness.New(liveness.Options{Now: func() time.Time { return now }})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan liveness.Report, 4)
	go m.Watch(ctx, 5*time.Millisecond,
		func() []liveness.Target {
			return []liveness.Target{{Harp: harp, StartedAt: now.Add(-time.Hour), LastActivity: now, TranscriptPath: transcriptPath(t, harp)}}
		},
		func(r liveness.Report) {
			select {
			case got <- r:
			default:
			}
		})

	select {
	case r := <-got:
		assert.Equal(t, liveness.StateStalled, r.State, "reason=%q", r.Reason)
	case <-time.After(5 * time.Second):
		t.Fatal("Watch never emitted a report")
	}
}

// A zero-value Thresholds must fall back to the tuned defaults rather than
// silently degrading the ladder into firing on everything.
func TestThresholds_ZeroValueNormalizesToDefaults(t *testing.T) {
	m := liveness.New(liveness.Options{})
	assert.Equal(t, liveness.DefaultThresholds(), m.Thresholds())

	custom := liveness.New(liveness.Options{Thresholds: liveness.Thresholds{QuietGrace: time.Minute}})
	assert.Equal(t, time.Minute, custom.Thresholds().QuietGrace)
	assert.Equal(t, liveness.DefaultThresholds().StartGrace, custom.Thresholds().StartGrace,
		"overriding one threshold must not blank the rest")
}
