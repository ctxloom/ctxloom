// Package acceptance: the capability ladder's SHARED FIXTURE VOCABULARY — the
// task, the agent binding, the bundle that plants the nonce, and the config a
// cell runs under.
//
// UNTAGGED, like capability_probe_registry.go and probe_assert.go beside it, and
// for the same reason those two are: every one of these values is an input a
// hermetic test needs to reach. A fixture whose only reader is a paid live cell
// is a fixture nobody checks — and this project's characteristic defect is the
// well-formed report of nothing, which is exactly what a mistyped config key
// produces (an ignored key, a launch that silently takes the default, a green
// cell proving something other than what it claims).
//
// THESE SYMBOLS WERE P0'S, AND THEY MOVED HERE UNCHANGED. They were declared in
// steps_engine_isolation_matrix.go, behind the `acceptance` build tag, when P0
// was the only probe that had a fixture. P1 (the context-approach sweep) runs
// the SAME task through a pinned delivery approach — that is the entire
// experiment: hold the task constant, vary the mechanism — so a copy would have
// meant two prompts free to drift, and a drifting prompt would turn "this
// approach delivered the nonce" into "these two prompts got different answers".
// P2–P7 inherit the same vocabulary as they land.
//
// The build-tag move is not incidental to that. P1's verdict and its config
// renderer are unit-tested without an engine (capability_context_approaches.go),
// and a test cannot exercise a renderer it cannot compile.
package acceptance

import (
	"fmt"
	"strings"
	"time"
)

// matrixAgent is the one agent binding every cell configures. Fixed, so the
// only thing that varies across cells is the engine, the two isolation axes and
// (for P1) the pinned delivery approach.
const matrixAgent = "hello"

// matrixRunTimeout bounds one cell's live run. Generous on purpose: a cold
// container cell may pull or build before the engine even starts, and a killed
// run reports as a failure with no output, which reads like an engine defect
// rather than an impatient harness. A cell that genuinely hangs still fails —
// it just fails after saying so.
const matrixRunTimeout = 8 * time.Minute

// matrixExpectedKey is the ONE key the engine's entire stdout must parse to an
// object of: {"hello": "<the minted harp>"}. Named once so the prompt below and
// every probe's assertion cannot drift apart — a probe that asks for one key and
// asserts another is a test of nothing.
const matrixExpectedKey = "hello"

// matrixPrompt is the whole task. Every constraint is stated explicitly and
// redundantly, because this floor's job is to find out which engines honour a
// plain output contract — a vaguely worded prompt would blame the engine for
// the prompt's own slack.
//
// P1 sends this prompt BYTE-IDENTICALLY under each pinned approach. That is
// what makes its cells comparable to each other and to P0: the only variable is
// the delivery mechanism.
func matrixPrompt() string {
	return "Output a single JSON object and nothing else. The object has exactly one key, \"hello\", " +
		"and its value is the nonce string that appears in the additional context available to you in this " +
		"session (not in this message). Rules, all mandatory: output JSON only; no preamble; no postamble; " +
		"no explanation; no apology; no markdown code fences; no backticks; no trailing commentary. Your " +
		"entire response must be exactly one line of the form {\"hello\":\"THE_NONCE\"}."
}

// matrixBundleYAML plants the nonce as the agent's own composed context.
func matrixBundleYAML(nonce string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  nonce:\n    content: %q\n",
		"The nonce for this session is "+nonce)
}

// matrixConfigYAML renders config.yaml for one cell: the engine's OWN registry
// config (live_engine_registry.go's liveAgents[key].config, already carrying
// that engine's backend type and the cheap pinned model the whole @live lane
// shares) plus one agent binding, and `runtime: container` for the container
// axis only. Appending to the registry's own string keeps ONE source of truth
// for which backend type and model the live lane drives an engine with —
// exactly what probeConfigYAML (isolation_probe.go) does for the same reason.
//
// The workspace axis is NOT written here: it rides the `--workspace` flag on
// the run, mirroring the isolation probe, so a cell exercises the same public
// surface an operator would type.
//
// The agent binding's `surfaces:` DELIVERY PREFERENCE is not written here
// either — it is P1's variable, appended by approachSurfacesBlock
// (capability_context_approaches.go) so that P0's cells and P1's cells differ in
// exactly one line of config and nothing else.
func matrixConfigYAML(a liveAgent, llmKey, runtime string) string {
	var b strings.Builder
	b.WriteString(a.config)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - %s-profile\n    permissions: bypass\n",
		matrixAgent, llmKey, matrixAgent)
	if runtime == "container" {
		b.WriteString("    runtime: container\n")
	}
	return b.String()
}
