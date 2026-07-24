package liveness_test

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/liveness"
)

func TestHostProbe_NoPidIsUnobservedNotDead(t *testing.T) {
	st := liveness.HostProbe{}.Inspect(context.Background(), liveness.Target{Runtime: "host"})
	assert.False(t, st.Observed, "an unknown pid means we know nothing, not that it died")
	assert.False(t, st.Alive)
	assert.NotEmpty(t, st.Detail, "an unobserved target must say why")
}

func TestHostProbe_LiveProcessIsObservedAlive(t *testing.T) {
	st := liveness.HostProbe{}.Inspect(context.Background(), liveness.Target{Runtime: "host", PID: os.Getpid()})
	require.True(t, st.Observed)
	assert.True(t, st.Alive)
	if runtime.GOOS == "linux" {
		assert.True(t, st.CPUObserved, "/proc CPU accounting must be read where it exists")
		assert.Positive(t, st.CPU, "this very test process has consumed CPU")
	}
}

func TestHostProbe_DeadPidIsObservedDead(t *testing.T) {
	// A pid that has certainly exited: spawn nothing, use an implausible one.
	// pid 0 and negatives are rejected earlier, so use a very high pid which
	// is either free or (vanishingly rarely) in use — the assertion is only
	// that we OBSERVE something, and that a free pid reads as not alive.
	const unlikely = 4194303 // one past the usual Linux pid_max
	st := liveness.HostProbe{}.Inspect(context.Background(), liveness.Target{Runtime: "host", PID: unlikely})
	assert.True(t, st.Observed, "a pid we were given is a pid we can answer about")
	assert.False(t, st.Alive)
}

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
	assert.False(t, st.CPUObserved)
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
