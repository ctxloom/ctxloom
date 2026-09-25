package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// TestStartOwnedRun_NilCoordinatorRefusesTheLaunch pins the owner run's
// refusal to start without a hosted coordinator.
//
// The coordinator IS the run's transport: the runner receives its Launch from
// it and the session is driven over its event stream, so a run launched
// without one has no transport at all and could only produce a runner that
// starts, answers nobody, and reports success. This must stay a hard refusal
// even in --degraded mode, which downgrades the coordinator standup failure
// elsewhere -- degrading THIS is not "fewer features", it is a run that
// cannot work.
func TestStartOwnedRun_NilCoordinatorRefusesTheLaunch(t *testing.T) {
	started := false
	sess, err := startOwnedRun(t.Context(), nil, ownedRunLaunch{
		Launch: launch.Launch{Identity: sessions.Identity{Harp: "swift-amber-falcon"}, Engine: "claude-code"},
	}, func(context.Context, map[string]string) (coord.OwnedRunner, error) {
		started = true
		return coord.OwnedRunner{}, nil
	})

	require.Error(t, err, "no coordinator means no transport — the launch must refuse, not proceed")
	assert.Nil(t, sess)
	assert.False(t, started, "nothing may be started before the refusal")
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
