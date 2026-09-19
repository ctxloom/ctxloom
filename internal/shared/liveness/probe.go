package liveness

import (
	"context"
	"strings"
)

// ProcState is what a Probe could learn about the RUNTIME hosting one agent.
//
// The Observed bit is the whole point of the type. Absence of evidence and
// evidence of absence must never collapse into one another: a probe that
// cannot see the target must not be mistaken for a probe that saw it gone,
// because the first says "ask a different question" and the second says "this
// thing is dead".
type ProcState struct {
	// Observed is false when no probe could say anything at all about this
	// target. The monitor then refuses to reach a DIED verdict.
	Observed bool `json:"observed"`
	// Alive is meaningful only when Observed. Probes err toward true: a
	// process this user cannot signal is still a process.
	Alive bool `json:"alive"`
	// Detail is a human note for the reason text ("no runner connected",
	// "runner heartbeat 3s ago").
	Detail string `json:"detail,omitempty"`
}

// Probe is the RUNTIME-POLYMORPHIC half of the monitor: what counts as
// evidence of life differs between a host process and a container (which the
// coordinator may only be able to see through its runner's heartbeat). The
// monitor never
// probes anything directly; it asks whichever probes claim the target's
// runtime, so adding a runtime is adding a Probe, not editing a ladder.
type Probe interface {
	// Runtime names the axis this probe serves ("host",
	// "container-rootless", "container-rootful"). Matching is EXACT, so a
	// probe meant for containers at large needs one registration per
	// ownership mode. The empty string means UNIVERSAL — applies to every
	// target regardless of runtime (the runner-heartbeat probe is the
	// motivating case: a runner dials home from inside a container exactly
	// as it does from the host).
	Runtime() string
	// Inspect reports what this probe can see. It must return Observed:false
	// rather than a guess when it cannot see the target — a probe that
	// invents liveness is the silent no-op wearing a different hat.
	Inspect(ctx context.Context, t Target) ProcState
}

// ProbeFunc adapts a function to Probe, for callers (the coordinator adapter,
// tests) whose evidence source is a closure over state they already hold.
type ProbeFunc struct {
	// RuntimeName is this probe's Runtime() answer: the runtime AXIS it
	// serves, matched against Target.Runtime, where the empty string means
	// UNIVERSAL. It is not an identifier for the probe — a field called `Name`
	// reads like one, and a caller filling it in with a label ("heartbeat")
	// would silently narrow the probe to targets of a runtime that does not
	// exist, which is a probe that never runs rather than a probe misnamed.
	RuntimeName string
	Fn          func(ctx context.Context, t Target) ProcState
}

func (p ProbeFunc) Runtime() string { return p.RuntimeName }

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
//   - Observed is likewise an OR: one probe that could see nothing never
//     erases another's observation.
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
	}
	out.Detail = strings.Join(details, "; ")
	return out
}
