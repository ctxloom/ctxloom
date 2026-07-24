package liveness

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"
)

// ProcState is what a Probe could learn about the RUNTIME hosting one agent.
//
// The three "observed" bits are the whole point of the type. Absence of
// evidence and evidence of absence must never collapse into one another: a
// container probe that cannot read CPU must not be mistaken for a probe that
// read 0% CPU, because the first says "ask a different question" and the
// second says "this thing is hung".
type ProcState struct {
	// Observed is false when no probe could say anything at all about this
	// target. The monitor then refuses to reach a DIED verdict.
	Observed bool `json:"observed"`
	// Alive is meaningful only when Observed. Probes err toward true: a
	// process this user cannot signal is still a process.
	Alive bool `json:"alive"`
	// CPUObserved says whether CPU accounting is available for this runtime
	// AT ALL. False is a permanent property of the probe/runtime pair (a
	// container the coordinator cannot exec into), not a transient miss —
	// the monitor uses it to decide whether waiting for a second sample could
	// ever help.
	CPUObserved bool `json:"cpu_observed"`
	// CPU is CUMULATIVE cpu time consumed by the process since it started.
	// The monitor differences successive samples itself; a single absolute
	// reading says nothing about whether anything is happening now.
	CPU time.Duration `json:"cpu,omitempty"`
	// Detail is a human note for the reason text ("no pid known", "runner
	// heartbeat 3s ago").
	Detail string `json:"detail,omitempty"`
}

// Probe is the RUNTIME-POLYMORPHIC half of the monitor: what counts as
// evidence of life differs between a host process (a pid you can signal and
// whose CPU you can read from /proc) and a container (which the coordinator
// may only be able to see through its runner's heartbeat). The monitor never
// probes anything directly; it asks whichever probes claim the target's
// runtime, so adding a runtime is adding a Probe, not editing a ladder.
type Probe interface {
	// Runtime names the axis this probe serves ("host", "container"). The
	// empty string means UNIVERSAL — applies to every target regardless of
	// runtime (the runner-heartbeat probe is the motivating case: a runner
	// dials home from inside a container exactly as it does from the host).
	Runtime() string
	// Inspect reports what this probe can see. It must return Observed:false
	// rather than a guess when it cannot see the target — a probe that
	// invents liveness is the silent no-op wearing a different hat.
	Inspect(ctx context.Context, t Target) ProcState
}

// HostProbe is the host-runtime probe: signal-0 liveness for a known pid,
// plus cumulative CPU from /proc/<pid>/stat where the platform provides it.
// A target with no pid yields Observed:false — the coordinator does not
// currently retain child pids (Spawner.StartEngine returns a Kill closure,
// not a pid), so this probe is honest about knowing nothing rather than
// asserting a process that was never identified.
type HostProbe struct{}

func (HostProbe) Runtime() string { return "host" }

func (HostProbe) Inspect(_ context.Context, t Target) ProcState {
	if t.PID <= 0 {
		return ProcState{Detail: "no pid known for this host run"}
	}
	alive := pidalive.Alive(t.PID)
	st := ProcState{Observed: true, Alive: alive, Detail: fmt.Sprintf("pid %d", t.PID)}
	if !alive {
		return st
	}
	if cpu, ok := processCPU(t.PID); ok {
		st.CPUObserved = true
		st.CPU = cpu
	}
	return st
}

// UnobservableProbe is an explicit, named "this runtime tells us nothing"
// probe. It exists so that a runtime with no implemented process evidence is
// a DECLARED gap carrying a reason a human can read, rather than a silent
// fallthrough that looks identical to a healthy answer.
type UnobservableProbe struct {
	RuntimeName string
	Why         string
}

func (p UnobservableProbe) Runtime() string { return p.RuntimeName }

func (p UnobservableProbe) Inspect(context.Context, Target) ProcState {
	return ProcState{Detail: p.Why}
}

// ProbeFunc adapts a function to Probe, for callers (the coordinator adapter,
// tests) whose evidence source is a closure over state they already hold.
type ProbeFunc struct {
	Name string
	Fn   func(ctx context.Context, t Target) ProcState
}

func (p ProbeFunc) Runtime() string { return p.Name }

func (p ProbeFunc) Inspect(ctx context.Context, t Target) ProcState {
	if p.Fn == nil {
		return ProcState{}
	}
	return p.Fn(ctx, t)
}

// mergeProbes combines every probe applicable to t into one ProcState.
//
// The merge rules are deliberately asymmetric, and both asymmetries err in
// the direction of NOT firing:
//   - Alive is an OR over probes that observed. One probe positively seeing
//     the target alive outranks another's inability to find it, because a
//     wrong DIED verdict is worse than a missed one.
//   - CPU comes from the first probe that could measure it; probes that
//     cannot measure CPU do not clear CPUObserved.
func mergeProbes(ctx context.Context, probes []Probe, t Target) ProcState {
	out := ProcState{}
	var details []string
	for _, p := range probes {
		if p == nil {
			continue
		}
		if rt := p.Runtime(); rt != "" && rt != t.Runtime {
			continue
		}
		st := p.Inspect(ctx, t)
		if st.Detail != "" {
			details = append(details, st.Detail)
		}
		if st.Observed {
			out.Observed = true
			out.Alive = out.Alive || st.Alive
		}
		if st.CPUObserved && !out.CPUObserved {
			out.CPUObserved = true
			out.CPU = st.CPU
		}
	}
	out.Detail = strings.Join(details, "; ")
	return out
}

// processCPU reads cumulative user+system CPU time for pid. Linux-only in
// practice (/proc); every other platform returns ok=false, which the monitor
// reads as "this runtime has no CPU evidence" rather than "idle".
func processCPU(pid int) (time.Duration, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// Field 2 (comm) is parenthesized and may contain spaces, so split after
	// the LAST ')' — the standard way to parse this file safely.
	s := string(data)
	close := strings.LastIndex(s, ")")
	if close < 0 || close+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[close+2:])
	// After comm/state, utime is field 14 and stime field 15 of the whole
	// line, i.e. indices 11 and 12 of this remainder (state is index 0).
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	// USER_HZ is 100 on every Linux platform Go builds for; the monitor only
	// ever differences these values, so a wrong constant would scale both
	// samples identically and still answer "is it burning CPU".
	const userHZ = 100
	return time.Duration(utime+stime) * time.Second / userHZ, true
}
