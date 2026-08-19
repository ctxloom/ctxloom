package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// =============================================================================
// AgentRuntimeOffer — which runtime axes the agent-creating setup interview may
// OFFER for an engine, and why it withholds the container ones when it does.
//
// The claim under test is an AGREEMENT, not a lookup: the offer must never
// present an axis that the very next `agent create` refuses. That is what makes
// gating on isolation.HasContainerAuth load-bearing rather than decorative, and
// TestAgentRuntimeOffer_AgreesWithWhatTheWriterAccepts below asserts the
// agreement against the real writer instead of restating the predicate.
// =============================================================================

// acpConfig is a project whose only engine label is bound to the generic "acp"
// backend — a REGISTERED backend (so the write's engine-membership check passes
// and the container-auth refusal is the one thing left to fail on) that reaches
// engineContainerSpecFor's fail-closed default arm and therefore has no
// container auth.
const acpConfig = "version: 6\nllm:\n  configs:\n    editor: { type: acp }\n  defaults:\n    primary: editor\n"

// TestAgentRuntimeOffer_EngineWithoutContainerAuthIsNotOfferedAContainerRuntime
// is the gate. An engine that cannot authenticate inside a container gets host
// and nothing else — offering it a container axis would collect a decision the
// user has the least context to re-derive, and then throw it away at the write.
func TestAgentRuntimeOffer_EngineWithoutContainerAuthIsNotOfferedAContainerRuntime(t *testing.T) {
	cfg, _ := loadConfigDir(t, acpConfig)

	offer := AgentRuntimeOffer(cfg, "editor")

	require.False(t, isolation.HasContainerAuth(offer.Backend),
		"fixture precondition: %q must be a backend with no container auth", offer.Backend)
	assert.Equal(t, []isolation.RuntimeAxis{isolation.RuntimeHost}, offer.Runtimes,
		"an engine with no container auth may be offered host and nothing else")
	assert.False(t, offer.OffersContainer(),
		"no container axis may appear in an offer for an engine that cannot authenticate in one")
}

// TestAgentRuntimeOffer_WithheldContainerSaysWhy pins the second half of the
// requirement: the option is not silently absent. A shorter menu with no reason
// reads as ctxloom having decided against containers, when the real fact is
// narrow and fixable.
func TestAgentRuntimeOffer_WithheldContainerSaysWhy(t *testing.T) {
	cfg, _ := loadConfigDir(t, acpConfig)

	offer := AgentRuntimeOffer(cfg, "editor")

	require.NotEmpty(t, offer.ContainerWithheld,
		"a withheld container axis must carry its reason, not just be missing")
	assert.Contains(t, offer.ContainerWithheld, `"editor"`,
		"the reason names the label the user typed")
	assert.Contains(t, offer.ContainerWithheld, `"acp"`,
		"the reason names the backend the label resolved to, since the two differ here")
	assert.Contains(t, offer.ContainerWithheld, "container auth",
		"the reason states the actual obstacle")
	for _, engine := range isolation.ContainerAuthEngines() {
		assert.Contains(t, offer.ContainerWithheld, engine,
			"the reason lists the engines that DO have container auth, so the user has a way forward")
	}
}

// TestAgentRuntimeOffer_EngineWithContainerAuthGetsBothAxes proves the gate is
// not simply off: an engine that CAN authenticate is offered both container
// axes, and nothing is withheld.
func TestAgentRuntimeOffer_EngineWithContainerAuthGetsBothAxes(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 6\n")

	offer := AgentRuntimeOffer(cfg, "claude-code")

	require.True(t, isolation.HasContainerAuth(offer.Backend),
		"fixture precondition: %q must have container auth", offer.Backend)
	assert.Equal(t, []isolation.RuntimeAxis{
		isolation.RuntimeHost,
		isolation.RuntimeContainerRootless,
		isolation.RuntimeContainerRootful,
	}, offer.Runtimes)
	assert.True(t, offer.OffersContainer())
	assert.Empty(t, offer.ContainerWithheld,
		"nothing is withheld, so there is nothing to explain")
}

// TestAgentRuntimeOffer_NoDefaultIsMarked is the decision this task turns on:
// the offer is a MENU and names no preferred element. A container default would
// make every unspecified run an explicit container demand at SelectRuntime —
// fatal on any machine with no docker/podman — and a host default would quietly
// re-introduce the unchosen posture the interview exists to eliminate. The type
// therefore has nowhere to put a default, and this test pins that absence: host
// leads because it is the axis every engine can take, not because it wins.
func TestAgentRuntimeOffer_NoDefaultIsMarked(t *testing.T) {
	cfg, _ := loadConfigDir(t, "version: 6\n")

	offer := AgentRuntimeOffer(cfg, "claude-code")

	assert.Len(t, offer.Runtimes, 3,
		"all three axes are offered on equal footing — none is marked, promoted or pre-picked")
	assert.Equal(t, isolation.RuntimeHost, offer.Runtimes[0],
		"host leads the menu as the axis every engine can take")
}

// TestAgentRuntimeOffer_NilConfigWithholdsContainer pins the DIRECTION of the
// degraded answer. With no config the label cannot be resolved to a backend, so
// whether it has container auth is unknown — and an unknown withholds. Offering
// an axis that may then be refused costs the user a wasted decision on the one
// question they cannot re-derive; withholding one that would have been allowed
// costs a re-run of an interview that is re-runnable by design.
func TestAgentRuntimeOffer_NilConfigWithholdsContainer(t *testing.T) {
	offer := AgentRuntimeOffer(nil, "claude-code")

	assert.Equal(t, []isolation.RuntimeAxis{isolation.RuntimeHost}, offer.Runtimes)
	assert.False(t, offer.OffersContainer(),
		"an unknown backend is not evidence that a container axis would work")
	assert.NotEmpty(t, offer.ContainerWithheld,
		"even the degraded path says why, rather than presenting a short menu")
}

// TestAgentRuntimeOffer_AgreesWithWhatTheWriterAccepts is the reason the gate
// consults HasContainerAuth instead of a hand-kept roster. For EVERY registered
// backend, the offer's verdict and the real writer's verdict must match: if the
// interview offers container-rootless, `agent create --runtime container-rootless`
// must succeed, and if it withholds it, that same write must be refused.
//
// This is the test a second roster would fail the moment a container spec was
// added to one list and not the other.
func TestAgentRuntimeOffer_AgreesWithWhatTheWriterAccepts(t *testing.T) {
	names := backends.List()
	require.NotEmpty(t, names)

	for _, backend := range names {
		t.Run(backend, func(t *testing.T) {
			cfg, appDir := loadConfigDir(t, "version: 6\n")
			offer := AgentRuntimeOffer(cfg, backend)

			_, err := SetAgent(managerFor(appDir), cfg, SetAgentRequest{
				Name:    "probe",
				LLM:     ptr(backend),
				Runtime: ptr(string(isolation.RuntimeContainerRootless)),
			})

			if offer.OffersContainer() {
				assert.NoError(t, err,
					"%s: the interview offered container-rootless, so the write must accept it", backend)
				return
			}
			require.Error(t, err,
				"%s: the interview withheld container-rootless, so the write must refuse it — an offer the writer accepts but the interview hides is a missing option, and one the interview offers but the writer refuses is a wasted decision", backend)
			assert.Contains(t, err.Error(), "container auth",
				"%s: and the refusal must be the container-auth one, not some unrelated failure", backend)
		})
	}
}
