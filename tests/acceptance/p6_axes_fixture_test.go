//go:build acceptance

package acceptance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestP6Fixture_ContainerCellIsNotAHostCellWearingALabel pins the one way this
// probe could lie: an Examples row labelled container-rootless/worktree whose
// fixture actually writes a host, shared-workspace config. That cell would be
// addressable, runnable and green, and it would prove nothing about the
// isolation boundary it names — the precise failure mode the P6 axis guard was
// written to refuse.
//
// So this asserts the two configs DIFFER in the two specific keys that carry
// the axes, rather than asserting either one merely parses. Comparing them to
// each other is what makes it impossible to satisfy both halves with one
// hardcoded string.
func TestP6Fixture_ContainerCellIsNotAHostCellWearingALabel(t *testing.T) {
	a, ok := liveAgents[backendTypeToLiveKey("claude-code")]
	require.True(t, ok, "claude-code must be a registered live engine or the cell could never run")
	spec := &j002300AgentSpec{Name: "delegate", Profile: "p", Bundle: "b", Fragment: "marker", Guidance: "m"}

	host := j002300PerEngineConfigYAML(a, "claude-code", spec, "host", "none")
	ctr := j002300PerEngineConfigYAML(a, "claude-code", spec, "container-rootless", "worktree")

	require.NotEqual(t, host, ctr,
		"the container cell and the host cell must not render the same config — identical bytes mean the axes were dropped and the container row runs on the host")

	// The workspace axis lives at the top level.
	assert.Contains(t, host, "workspace: none")
	assert.NotContains(t, host, "workspace: worktree")
	assert.Contains(t, ctr, "workspace: worktree")
	assert.NotContains(t, ctr, "workspace: none")

	// The runtime axis rides the agent binding. Host must carry NO runtime key
	// at all: host is the schema default, and a fixture that wrote it would
	// differ from the one the host rows were actually measured against.
	assert.NotContains(t, host, "runtime:",
		"the host cell must write no runtime key — host is the default and the measured host rows had none")
	assert.Contains(t, ctr, "runtime: container-rootless")

	// The runtime key must sit INSIDE the agent binding, not at the top level:
	// the two axes are independent and a top-level runtime would not bind to
	// this agent at all. Indentation is the only thing expressing that in YAML.
	assert.Contains(t, ctr, "\n    runtime: container-rootless\n",
		"runtime must be indented under the agent binding, as j002200ConfigYAML's mock-container binding writes it")
	// dirty_tree_handler: MEASURED requirement, not a preference. The first live
	// run of this cell failed with the child never launching — agent_run refused
	// the spawn because ctxloom's own managed files (.ctxloom-managed,
	// .ctxloom/project-id, .claude/settings.json, .claude/commands/*) are written
	// during session startup, AFTER the fixture commits. The default "commit"
	// handler cannot be used by any automated cell: it requires
	// dirty_tree_commit_ack, which is a human act and cannot be set from config,
	// an env var, or a per-call parameter.
	assert.Contains(t, ctr, "dirty_tree_handler: copy",
		"the worktree cell must reproduce uncommitted state into the child's checkout, or the child never launches")
	assert.NotContains(t, host, "dirty_tree_handler",
		"a shared-workspace run has no second checkout to reproduce into; the key would be inert noise in the host rows")

	// isolation_engines is MACHINE-SCOPED and must NOT appear in the project
	// file: config Load drops such a key with a warning rather than applying it,
	// so a fixture writing it there would look pinned and not be. It belongs to
	// the home layer, asserted separately below.
	assert.NotContains(t, ctr, "isolation_engines",
		"a machine-scoped key in the project file is dropped, not applied")

	idxAgents := strings.Index(ctr, "agents:")
	idxRuntime := strings.Index(ctr, "runtime: container-rootless")
	require.Positive(t, idxAgents)
	assert.Greater(t, idxRuntime, idxAgents, "runtime must appear after agents:, i.e. within the binding")
}

// TestP6Fixture_HomeConfigPinsTheImageToOneEngine: the container cell must
// compose an agent image carrying ONLY the engine it exercises.
//
// Unset, isolation.resolveEngines returns composableEngines() — claude-code,
// codex, kiro and opencode — so the image installs all four via four
// third-party installers, and this cell then depends on three other vendors
// staying reachable. That is not hypothetical: its first container run died
// building that image when opencode's installer could not fetch its version.
//
// The pin lives in the HOME layer because isolation_engines is machine-scoped;
// a project file carrying it is dropped with a warning rather than applied, so
// pinning there would look applied and not be.
func TestP6Fixture_HomeConfigPinsTheImageToOneEngine(t *testing.T) {
	ctr := p6SpoolHomeConfigYAML("claude-code", "container-rootless")
	host := p6SpoolHomeConfigYAML("claude-code", "host")

	require.NotEqual(t, host, ctr,
		"identical bytes would mean the runtime axis was dropped on the way in")
	assert.Contains(t, ctr, "isolation_engines:\n  - claude-code",
		"the container cell must pin the image composition to its own engine")
	assert.NotContains(t, host, "isolation_engines",
		"a host row builds no image, so pinning its composition would be inert noise")
	// The mail plane must survive on both, or the cell has no bus to measure.
	for _, c := range []string{ctr, host} {
		assert.Contains(t, c, "spool_tee: true")
		assert.Contains(t, c, "spool_delivery: true")
	}
}
