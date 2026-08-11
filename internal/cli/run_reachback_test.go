package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
)

// TestStartContainerOwnedRun_NilCoordinatorRefusesTheLaunch pins the owned-run
// arm's refusal to start without a hosted coordinator.
//
// Transport 2 IS the reach-back for this arm: a --one-shot container
// run drives its whole session over the coordinator's event stream, so a run
// launched without one has no transport at all and could only produce a
// container that starts, answers nobody, and reports success. This must stay a
// hard refusal even in --degraded mode, which downgrades the coordinator
// standup failure elsewhere -- degrading THIS is not "fewer features", it is a
// run that cannot work.
func TestStartContainerOwnedRun_NilCoordinatorRefusesTheLaunch(t *testing.T) {
	handle, sess, err := startContainerOwnedRun(t.Context(), nil, ownedRunLaunch{
		BackendName: "claude-code",
		Harp:        "swift-amber-falcon",
	})

	require.Error(t, err, "no coordinator means no transport — the launch must refuse, not proceed")
	assert.Nil(t, sess)
	assert.Nil(t, handle, "nothing may be started before the refusal")
	assert.Contains(t, err.Error(), "coordinator",
		"the refusal must name what is missing, so the operator can act on it")
}

// TestConfirmProfileUpgrades_UnresolvableProfileIsNotFatal pins the harvesting
// loop's tolerance, which is what makes discarding ResolveProfile's error at
// that site correct rather than a swallow.
//
// confirmProfileUpgrades resolves the default agent's profiles for ONE reason:
// to make the loader populate PendingUpgrades, so an older-schema file can be
// offered a rewrite. It is not the place that decides whether the run's context
// is resolvable -- AssembleContext does that later and fails loud through
// strictness.ClassRef. So an unresolvable name here must neither abort the
// harvest nor stop the remaining profiles from being walked: the only thing a
// resolve failure can cost is an upgrade prompt for a file that could not be
// loaded anyway, and reporting it here would double-report a fault the
// assembly path is about to raise properly.
func TestConfirmProfileUpgrades_UnresolvableProfileIsNotFatal(t *testing.T) {
	warnings := captureWarnings(t)

	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "dev",
		Agents: map[string]agents.Agent{
			"dev": {Profiles: []string{"no-such-profile", "also-missing"}},
		},
	})
	require.NotEmpty(t, cfg.DefaultAgentProfiles(), "the fixture must actually give the harvest something to walk")

	assert.NotPanics(t, func() { confirmProfileUpgrades(cfg) },
		"an unresolvable profile must not abort the upgrade harvest")

	assert.NotContains(t, warnings.String(), "no-such-profile",
		"the harvest must not report a resolution fault here — AssembleContext raises it as a ClassRef finding, and warning twice for one broken reference reads as two problems")
}
