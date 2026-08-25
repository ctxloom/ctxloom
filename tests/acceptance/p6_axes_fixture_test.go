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

// TestP6Fixture_HomeConfigCarriesTheMailPlaneAndNoImagePin replaces
// TestP6Fixture_HomeConfigPinsTheImageToOneEngine, whose subject no longer
// exists.
//
// THAT TEST PINNED A WORKAROUND. P6's container cell used to write
// `isolation_engines: [<engine>]` into the HOME layer, because an agent image
// composed ALL FOUR engines and this cell's first container run died building
// one when opencode's installer could not fetch its version — a cell exercising
// one engine failing on a different vendor's availability.
//
// The image is now ONE ENGINE BY CONSTRUCTION (frosted-pony, 2026-08-25), so
// the pin is inert and the config no longer varies by axis at all. The
// invariant it was defending did not disappear; it MOVED, and it is asserted
// where it can actually be checked — TestComposeAgentContainerfile_ExactlyOneEngineStage
// reads the generated Containerfile and reds if a second engine's installer
// appears. A YAML fixture could never have proven that.
//
// What is left worth pinning here is the mail plane, and the ABSENCE of the
// pin: re-adding it would look like configuration and do nothing.
func TestP6Fixture_HomeConfigCarriesTheMailPlaneAndNoImagePin(t *testing.T) {
	cfg := p6SpoolHomeConfigYAML()

	assert.NotContains(t, cfg, "isolation_engines",
		"the image is one-engine by construction now; an isolation_engines pin here would look like configuration and do nothing")
	// The mail plane must survive, or the cell has no bus to measure.
	assert.Contains(t, cfg, "spool_tee: true")
	assert.Contains(t, cfg, "spool_delivery: true")
}
