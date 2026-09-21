package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/mcp"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// runnerStandup is the result of standUpRunner: the dialed-home owner
// runner's lifecycle handles plus the teardown `llm serve` runs AFTER its
// blocking plugin.Serve. home is nil when the coordinator trio was absent
// (an unconfigured/top-level serve). Dies with the plugin arm.
type runnerStandup struct {
	home          *runner.Home
	endpointClose func()
}

// standUpRunner is the plugin arm's standup (`llm serve`, `llm turn`):
// consume + scrub the coordinator reach-back trio, then the plugin-hosted
// OWNER arm (no run id) loads config and serves the socket endpoint its stdio
// shim forwards to. A HOSTED run — the trio names a run id — is
// `ctxloom runner`'s (runner.Main) and is refused here. With no reach-back at
// all there is nothing to dial or host. Dies with the plugin arm.
//
// label is the config label whose LLM entry configures the backend on the
// owner arm, passed by whichever command owns the standup — each has its own
// --label flag.
func standUpRunner(cmd *cobra.Command, backend agent.Backend, backendName, label string) (*runnerStandup, error) {
	reach, rerr := consumeCoordinatorReachBack(backendName, os.Getenv, os.Unsetenv)
	if rerr != nil {
		return nil, rerr
	}
	homeCfg := reach.home
	standup := &runnerStandup{}

	if homeCfg.URL == "" || homeCfg.Token == "" {
		// No reach-back: nothing to dial or host (an unconfigured/top-level
		// serve).
		if _, cfgErr := loadAndConfigureBackend(backend, backendName, label); cfgErr != nil {
			return nil, cfgErr
		}
		return standup, nil
	}

	if homeCfg.RunID != "" {
		// A hosted run is `ctxloom runner`'s (runner.Main); the plugin arm
		// hosts no run of its own.
		return nil, fmt.Errorf("%s: a reach-back naming run %s belongs to `ctxloom runner`, not the plugin arm", backendName, homeCfg.RunID)
	}

	cfg, cfgErr := loadAndConfigureBackend(backend, backendName, label)
	if cfgErr != nil {
		return nil, cfgErr
	}
	// The Hello advertisement: an engineless runner advertises the mailbox
	// surface alone.
	homeCfg.Capabilities = coord.RunnerCapabilities(false)
	homeCfg.Reporter = App().Reporter
	h, herr := runner.NewHome(cmd.Context(), homeCfg)
	if herr != nil {
		clidiag.Warn("ctxloom", "runner dial-home failed (coordinator will synthesize loss): %v", herr)
		return standup, nil
	}
	standup.home = h
	// The plugin-hosted owner arm: no Launch will arrive, so its MCP
	// endpoint stands up now, keyed by the harp its env carried.
	if err := attachRunnerMCP(standup, cfg, h, reach.harp); err != nil {
		h.Close(1, "")
		return nil, err
	}
	return standup, nil
}

// runnerDepsFor composes the runner's ports for the one engine this process
// hosts: the two package transports (the claim store rooted at this
// process's sessions root — the mounted one inside a container), the ONE
// static writer over the home-rooted ownership record, the runner MCP
// standup as the dynamic half, the engine's configure seam over the label
// body the Launch carries, and the engine host as the driver.
func runnerDepsFor(backend agent.Backend, backendName string, host *runner.EngineHost, dynamic delivery.Dynamic) (runner.Deps, error) {
	ctxHome, err := paths.HomeConfigDir()
	if err != nil {
		return runner.Deps{}, fmt.Errorf("runner: sessions root: %w", err)
	}
	kind, ok := backends.Kind(backendName)
	if !ok {
		return runner.Deps{}, fmt.Errorf("runner: no engine kind %q is composed", backendName)
	}
	records, err := operations.OwnershipRecords()
	if err != nil {
		return runner.Deps{}, err
	}
	deps := runner.Deps{
		Kind:       kind,
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: fsstore.PackageStore{Root: filepath.Join(ctxHome, paths.SessionsDir)}},
		Static:     fsstatic.New(afero.NewOsFs()),
		Records:    records,
		Dynamic:    dynamic,
		Driver:     host,
	}
	if c, ok := backend.(backends.Configurable); ok {
		deps.Configure = func(body map[string]any) error {
			bc, err := backends.DecodeLLMConfig(backendName, body)
			if err != nil {
				return err
			}
			c.Configure(bc)
			return nil
		}
	}
	return deps, nil
}

// loadAndConfigureBackend reads this runner process's configuration and
// applies the backend's LLM entry. A configuration the reader REFUSES (a
// present config.yaml that cannot be parsed) is recorded as a fatal-class
// finding and returned: a process-owning entry point must never launch an
// engine unconfigured over a config it could not read. The runner has no
// owner of its own beyond the one it opens here; what a runner may know is
// slice 8's Loadout carrier.
func loadAndConfigureBackend(backend agent.Backend, backendName, label string) (*config.Config, error) {
	cfg, cfgErr := GetConfig()
	if cfgErr != nil {
		strictness.Fail(strictness.ClassConfig, "fix the config this runner could not read",
			"config cannot be read; refusing to serve %s unconfigured: %v", backendName, cfgErr)
		return nil, cfgErr
	}
	config.ReportWarnings(strictness.Sink("ctxloom"), cfg.GetWarnings())
	if bc := serveBackendConfig(cfg, backendName, label); bc != nil {
		if c, ok := backend.(backends.Configurable); ok {
			c.Configure(bc)
		}
	}
	return cfg, nil
}

// attachRunnerMCP stands up the plugin-hosted owner arm's socket endpoint for
// harp and publishes its socket into this process's environment, recording
// the endpoint's closer on standup. The owner's stdio shim (`ctxloom mcp
// serve`, launched by the interactive engine) forwards to it by
// CTXLOOM_MCP_SOCKET; the arm dies with the plugin protocol. A failed
// endpoint degrades with a warning: the shim then serves its own cell-local
// surface and REFUSES the agent tools — it never hosts a coordinator of its
// own (mcp.ServeStdio) — so the cost is this session's delegation, not a
// rival owner.
func attachRunnerMCP(standup *runnerStandup, cfg *config.Config, h *runner.Home, harp string) error {
	endpoint, merr := mcp.ServeRunnerMCP(App().Reporter, cfg, harp, h)
	if merr == nil {
		// The shim reads CTXLOOM_MCP_SOCKET from THIS process's env (every
		// engine spawn path builds the harness env over os.Environ), so a
		// failed export leaves the endpoint standing and unaddressable — the
		// same end state as no endpoint at all, and treated as the same failure.
		if merr = exportRunnerMCPSocket(os.Setenv, endpoint.SocketPath); merr != nil {
			endpoint.Close()
		}
	}
	if merr != nil {
		clidiag.Warn("ctxloom", "runner MCP endpoint failed (the harness shim will serve its local surface and refuse agent delegation): %v", merr)
		return nil
	}
	standup.endpointClose = endpoint.Close
	return nil
}

// coordinatorReachBack is the per-spawn coordinator credential set a runner
// consumes from its own environment: the dial-home config (the reach-back
// trio) and, for the plugin-hosted session owner alone, its harp. A hosted
// run's identity — harp, depth, whether it is one-shot, the cell it runs in
// — arrives ONCE on the Launch and is never read from the environment.
type coordinatorReachBack struct {
	home runner.HomeConfig
	// harp is the plugin-hosted owner's own harp (OwnerRunnerEnv stamps it;
	// no Launch ever reaches that runner); empty for a hosted run.
	harp string
}

// coordinatorEnvKeys is the reach-back set the per-spawn seam stamps onto a
// runner's environment. Every one of them must be GONE from this process before
// it spawns anything.
var coordinatorEnvKeys = []string{
	coord.EnvCoordURL,
	coord.EnvCoordCred,
	coord.EnvRunID,
}

// consumeCoordinatorReachBack reads the coordinator reach-back out of the
// environment and SCRUBS it: the runner is the ONE credential holder, and the
// harness and every subprocess it spawns inherit this process's environment.
//
// A key that survives the scrub is a fatal error, not a warning. The engine is
// third-party code; leaving the coordinator credential in the environment it
// inherits is the isolation failing open, and there is no partial success to
// report — either the token is gone or it is handed over.
//
// getenv/unset are parameters because the failure they guard cannot be provoked
// through the real syscalls: on unix syscall.Unsetenv always reports success, so
// the branch is reachable only under test and on Windows. Injecting them is what
// makes the invariant testable at all.
func consumeCoordinatorReachBack(backendName string, getenv func(string) string, unset func(string) error) (coordinatorReachBack, error) {
	reach := coordinatorReachBack{
		home: runner.HomeConfig{
			URL:     getenv(coord.EnvCoordURL),
			Token:   getenv(coord.EnvCoordCred),
			RunID:   getenv(coord.EnvRunID),
			Harness: backendName,
			Version: version.Version,
		},
	}
	if reach.home.RunID == "" {
		// The plugin-hosted owner arm: the harp rides the env because no
		// Launch will carry it.
		reach.harp = getenv(sessions.EnvHarp)
		reach.home.Harp = reach.harp
	}

	var unscrubbed []string
	for _, k := range coordinatorEnvKeys {
		if err := unset(k); err != nil {
			unscrubbed = append(unscrubbed, fmt.Sprintf("%s (%v)", k, err))
		}
	}
	if len(unscrubbed) > 0 {
		return coordinatorReachBack{}, fmt.Errorf("refusing to launch: the coordinator reach-back could not be scrubbed from this runner's environment, so the engine child would inherit the coordinator credential: %s", strings.Join(unscrubbed, ", "))
	}
	return reach, nil
}

// exportRunnerMCPSocket publishes the runner-local MCP socket path into THIS
// process's environment, which is where the engine child's shim reads it from
// (every engine spawn path builds the harness env over os.Environ). A failure
// here leaves the endpoint listening with nobody able to address it, so it is
// reported rather than dropped — the caller treats it exactly like an endpoint
// that never came up.
//
// set is a parameter for the same reason consumeCoordinatorReachBack's unset is:
// os.Setenv cannot fail for this constant key and a NUL-free socket path, so the
// error branch is otherwise untestable.
func exportRunnerMCPSocket(set func(string, string) error, socketPath string) error {
	if err := set(coord.EnvMCPSocket, socketPath); err != nil {
		return fmt.Errorf("export %s=%s (the engine child's shim reads the socket from this env): %w", coord.EnvMCPSocket, socketPath, err)
	}
	return nil
}

// teardown reports the runner's exit through home.Close; the MCP endpoint
// closes last.
func (s *runnerStandup) teardown() {
	if s.home != nil {
		s.home.Close(0, "")
	}
	if s.endpointClose != nil {
		s.endpointClose()
	}
}
