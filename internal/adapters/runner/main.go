package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// MainDeps is what the composition root hands Main: the ONE engine this
// process hosts, the process environment to read the reach-back from and
// scrub it out of, and the ports over the host and the dialed home.
type MainDeps struct {
	Reporter report.Sink
	// Harness is the engine this runner hosts — its RunnerHello
	// advertisement, and the name the launch must match.
	Harness string
	Version string
	// Getenv and Unsetenv are the process environment. Injected because the
	// scrub's failure cannot be provoked through the real syscalls (unix
	// Unsetenv always succeeds), and the invariant it guards must be testable.
	Getenv   func(string) string
	Unsetenv func(string) error
	// Ports composes the runner's Deps over the engine host and the dialed
	// home: the writers, the endpoint (runner/mcp over the home), the engine
	// kind. Called ONCE, after the environment is scrubbed.
	Ports func(host *EngineHost, home *Home) (Deps, error)
}

var (
	// ErrNoRun refuses a reach-back that names no run: every runner hosts
	// exactly one run, whose identity arrives on the Launch.
	ErrNoRun = errors.New("runner: the reach-back names no run to host")
	// ErrUnscrubbed refuses to compose an engine while the coordinator
	// credential is still in the process environment it would inherit.
	ErrUnscrubbed = errors.New("runner: the coordinator reach-back could not be scrubbed from the process environment")
	// ErrOwnerLost ends a runner whose coordinator stayed unreachable for the
	// whole owner-loss window (Home.OwnerLost): it tears down rather than
	// redialling forever, which is what lets a container's --rm remove it.
	ErrOwnerLost = errors.New("runner: the owning coordinator is gone")
)

// reachKeys is the reach-back trio the originator stamps on a runner
// process. Every one of them is GONE from the environment before anything
// is composed.
var reachKeys = []string{sessions.EnvCoordURL, sessions.EnvCoordCred, sessions.EnvRunID}

// Main is the runner process: the ONE unit a launch runs in, on the human's
// machine or as a container's foreground process, started by
// spawn.StartRunner. It decodes the reach-back trio ONCE, scrubs it, stands
// the engine host up for its one run, dials home, composes its ports and
// BLOCKS until ctx ends (the coordinator's Kill, a signal) or the home reports
// its owner lost (ErrOwnerLost). The Launch arrives over the RunnerChannel
// (StartRun) and Execute is its one tail.
func Main(ctx context.Context, d MainDeps) error {
	reach, runID, err := sessions.DecodeReach(d.Getenv)
	if err != nil {
		return err
	}
	if runID == "" {
		return ErrNoRun
	}
	var unscrubbed []string
	var uerrs error
	for _, k := range reachKeys {
		if uerr := d.Unsetenv(k); uerr != nil {
			unscrubbed = append(unscrubbed, fmt.Sprintf("%s (%v)", k, uerr))
			uerrs = errors.Join(uerrs, uerr)
		}
	}
	if len(unscrubbed) > 0 {
		return fmt.Errorf("%w: the engine would inherit the coordinator credential: %s: %w", ErrUnscrubbed, strings.Join(unscrubbed, ", "), uerrs)
	}

	// Read once, here, where the runner's lifetime policy is established.
	window := coord.EnvPositiveDuration(report.To(d.Reporter), sessions.EnvRunnerOwnerLossWindow, DefaultOwnerLossWindow,
		"a zero or negative window would end this runner the instant its coordinator blinked")
	host := NewEngineHost(ctx, d.Reporter, d.Harness, runID)
	home, err := NewHome(ctx, HomeConfig{
		URL:          reach.URL,
		Token:        reach.Credential,
		RunID:        runID,
		Harness:      d.Harness,
		Version:      d.Version,
		Engine:       host.Handle,
		Capabilities: coord.RunnerCapabilities(true),
		Reporter:     d.Reporter,

		OwnerLossWindow: window,
	})
	if err != nil {
		host.Close()
		return fmt.Errorf("runner: dial home: %w", err)
	}
	deps, err := d.Ports(host, home)
	if err != nil {
		host.Close()
		home.Close(1, "")
		return err
	}
	host.BindRunner(Host{Deps: deps})
	host.BindHome(home)

	var ended error
	select {
	case <-ctx.Done():
	case <-home.OwnerLost():
		ended = ErrOwnerLost
	}
	// The engine host joins first so an in-flight adapt can finish its
	// terminal RunCompleted while the home is still live.
	host.Close()
	home.Close(0, "")
	return ended
}
