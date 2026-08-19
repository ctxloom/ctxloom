//go:build acceptance

// Package acceptance: a regression guard for probeContainerRuntimeForAxis's
// one job — never let a container-rootful row resolve against a rootless
// runtime, or vice versa. Tagged (not hermetic) because it exercises
// containercell.Detect, which execs the real docker/podman CLIs on this box —
// read-only probes, never a paid engine call, so `just test-acceptance-focus`
// runs it on every invocation without spending a subscription turn.
//
// PROVEN BY MUTATION (task unvisited-magnolia, 2026-08-18): forcing
// wantRootless to true unconditionally in probeContainerRuntimeForAxis — the
// exact "a container-rootful row silently resolves to rootless" substitution
// the design forbids — turns this test red on this box (docker-rootless is
// reachable here, so the mutated function wrongly answers Proceed for the
// container-rootful axis). Reverting the mutation turns it green again. See
// the task's final report for the transcript.
package acceptance

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestProbeContainerRuntimeForAxis_NeverSubstitutesOwnership asks the real
// gate, on THIS box, for both ownership axes, and asserts the one invariant
// that matters regardless of what this box actually has reachable: whichever
// runtime a Proceed hands back, its OWN probed ownership must agree with the
// axis that was asked for. A same-vendor OR cross-vendor substitution — docker
// standing in for the other docker, or podman standing in for either — fails
// this test on any box, not just a rootless-only one.
func TestProbeContainerRuntimeForAxis_NeverSubstitutesOwnership(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		axis         string
		wantRootless bool
	}{
		{"container-rootless", true},
		{"container-rootful", false},
	} {
		t.Run(tc.axis, func(t *testing.T) {
			rt, decision, msg := probeContainerRuntimeForAxis(ctx, tc.axis, "mutation-proof test")
			t.Logf("%s on this box: decision=%s runtime=%+v msg=%s", tc.axis, decision, rt, msg)
			if decision != dockergate.Proceed {
				// Skip or Fail: this box does not (or does not admit to)
				// serving this axis. That is a fact about the box, not a
				// substitution, so it is not what this test checks — the
				// task's own report names which axis is verified-live here.
				return
			}
			if rt.RootMapsToInvoker != tc.wantRootless {
				t.Fatalf("%s resolved to runtime %q whose OWN probed ownership is rootless=%v — this is exactly the ownership substitution the two container runtime values exist to forbid",
					tc.axis, rt.Name, rt.RootMapsToInvoker)
			}
		})
	}
}
