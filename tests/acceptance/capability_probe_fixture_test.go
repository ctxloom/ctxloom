package acceptance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestProbeConfigYAML_EveryProbeCarriesTheRuntimeAxisOntoTheBinding is the
// assertion this wiring owes, and it guards a SILENT failure.
//
// A probe that records the axes but never writes them runs on the HOST while
// its cell id, its tags and its evidence line all say "container". Exit 0,
// green, measuring the wrong thing — matrixConfigYAML's own doc names it: "an
// axis a cell asked for that quietly runs on the host instead". No ordinary
// gate can see it, because the cell passes.
//
// So this pins the ONE line that carries the containerization axis, for every
// probe that builds a binding. A builder that stops emitting it goes RED here
// rather than going green on the host.
func TestProbeConfigYAML_EveryProbeCarriesTheRuntimeAxisOntoTheBinding(t *testing.T) {
	a := liveAgent{config: fmt.Sprintf("version: %d\nllm:\n  configs:\n    claude:\n      type: claude-code\n      model: claude-haiku-4-5-20251001\n", config.CurrentConfigVersion)}

	// UNTAGGED builders only. P2's and P3's live in //go:build acceptance files,
	// so they are covered by the sibling test in
	// capability_probe_axis_wiring_acceptance_test.go — split rather than tagging
	// this whole file, because these two must keep running under plain
	// `just test` (the reason probe_p4_plan_sentinel_test.go states for staying
	// untagged: a guard that only fires where a real engine and a credential
	// happen to exist is not much of a guard).
	builders := probeRuntimeBuilders(a)

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			for _, rt := range []string{"container-rootless", "container-rootful"} {
				assert.Contains(t, build(rt), "    runtime: "+rt+"\n",
					"%s must write the requested runtime onto the agent binding; without it the cell runs on the HOST and reports itself containerized", name)
			}
			// The host axis is the schema default. Writing it would put a key in
			// the fixture that every existing host row was measured without.
			assert.NotContains(t, build("host"), "runtime:",
				"%s must NOT write a runtime line for the host axis", name)
			assert.NotContains(t, build(""), "runtime:",
				"%s must NOT write a runtime line for an unset axis", name)
		})
	}
}

// TestRuntimeBindingLine_PassesAnUnknownValueThroughToTheSchema pins the
// deliberate choice this helper made when it replaced three copies.
//
// Two of those copies disagreed: one allow-listed the two container values, so
// an unrecognised runtime wrote NOTHING and the cell silently ran on the host.
// Passing the value through instead sends it to config's schema, which refuses
// it loudly. A future ownership mode is then a schema edit, not a silent drop.
func TestRuntimeBindingLine_PassesAnUnknownValueThroughToTheSchema(t *testing.T) {
	assert.Equal(t, "", runtimeBindingLine("host"), "host is the schema default")
	assert.Equal(t, "", runtimeBindingLine(""), "unset writes nothing")
	assert.Equal(t, "    runtime: container-rootless\n", runtimeBindingLine("container-rootless"))
	assert.Equal(t, "    runtime: container-rootful\n", runtimeBindingLine("container-rootful"))
	assert.Equal(t, "    runtime: container-someday\n", runtimeBindingLine("container-someday"),
		"an unrecognised mode must reach the schema and be REFUSED there, never be dropped here into a silent host run")
}

// probeRuntimeBuilders is the untagged half of the builder set. The acceptance-
// tagged file adds P2 and P3 to it, so ONE assertion body covers every probe
// that writes a binding, on whichever build the caller is running.
func probeRuntimeBuilders(a liveAgent) map[string]func(string) string {
	return map[string]func(string) string{
		"p0/p1 matrixConfigYAML": func(rt string) string { return matrixConfigYAML(a, "claude", rt) },
		"p4 p4ConfigYAML":        func(rt string) string { return p4ConfigYAML(a, "claude", p4Plan, rt) },
	}
}
