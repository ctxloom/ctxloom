package isolation

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestDefaultMemoryLimit_TheFloorIsTwoGiB pins the documented floor by value:
// a 3 GiB host gets 2 GiB, not half of itself.
func TestDefaultMemoryLimit_TheFloorIsTwoGiB(t *testing.T) {
	assert.Equal(t, 2*gib, defaultMemoryLimit(3*gib))
}

// TestResourceLimitsArgs_AnyPositiveCapIsAFlag: 0 is "no flag"; every positive
// value, however small, is rendered.
func TestResourceLimitsArgs_AnyPositiveCapIsAFlag(t *testing.T) {
	assert.Equal(t, []string{"--memory=1", "--memory-swap=1", "--pids-limit=1"},
		resourceLimits{memory: 1, memorySwap: 1, pids: 1}.args())
	assert.Empty(t, resourceLimits{}.args())
}

// TestResolveLimits_AOneProcessCapIsACap: isolation_pids_limit: 1 is a cap
// like any other — rendered where it is enforced, warned about where not.
func TestResolveLimits_AOneProcessCapIsACap(t *testing.T) {
	one := config.ContainerLimits{
		Memory: config.ContainerLimit{Kind: config.LimitUnlimited},
		PIDs:   config.ContainerLimit{Kind: config.LimitFixed, Value: 1},
	}
	assert.Equal(t, resourceLimits{pids: 1}, resolveLimits(one, enforcing(32*gib)))

	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	buf := captureWarnings(t)
	noPids := enforcing(32 * gib)
	noPids.pids = false
	assert.Equal(t, resourceLimits{}, resolveLimits(one, noPids))
	assert.Contains(t, buf.String(), "agent containers run without some resource caps")
}

// TestWithNote_KeepsTheErrorItNotes: the note grows the text only; the error
// it was added to is still the error underneath, classified exit or not.
func TestWithNote_KeepsTheErrorItNotes(t *testing.T) {
	cause := errors.New("engine exited")
	assert.ErrorIs(t, withNote(cause, "note"), cause)

	noted := withNote(&exitstatus.Error{Code: 137, Err: cause}, "note")
	assert.ErrorIs(t, noted, cause)
	var es *exitstatus.Error
	assert.ErrorAs(t, noted, &es)
	assert.Equal(t, 137, es.ExitStatus())
}

// TestDockerLimitSupport_EachVerdictStandsAlone: one enforced cap is enough to
// say nothing about unenforced caps; an unreadable MemTotal is an unreadable
// answer.
func TestDockerLimitSupport_EachVerdictStandsAlone(t *testing.T) {
	assert.Equal(t, limitSupport{hostMemory: 4000, memory: true, swap: true}, dockerLimitSupport("4000 true true false"))
	assert.Equal(t, limitSupport{hostMemory: 4000, pids: true}, dockerLimitSupport("4000 false false true"))
	assert.Equal(t, limitSupport{}, dockerLimitSupport("lots true true true"))
}

// TestPodmanLimitSupport_EachControllerStandsAlone: pids delegated alone is
// enforceable; neither delegated says why; an unreadable MemTotal is an
// unreadable answer.
func TestPodmanLimitSupport_EachControllerStandsAlone(t *testing.T) {
	assert.Equal(t, limitSupport{hostMemory: 1000, pids: true}, podmanLimitSupport("true pasta 1000 v2 systemd [pids]", true))
	assert.Equal(t, limitSupport{hostMemory: 1000, unenforced: "rootless podman has neither the memory nor the pids cgroup controller delegated"},
		podmanLimitSupport("true pasta 1000 v2 systemd [cpu io]", true))
	assert.Equal(t, limitSupport{}, podmanLimitSupport("true pasta lots v2 systemd [memory pids]", true))
}
