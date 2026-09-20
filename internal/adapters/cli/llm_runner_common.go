package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/mcp"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// runnerStandup is the shared result of standUpRunner: the dialed-home runner's
// lifecycle handles plus the teardown both `llm serve` (go-plugin transport)
// and `llm host` (docker-direct, no plugin) run AFTER their own blocking wait.
// home/engineHost are nil when the coordinator trio was absent (an
// unconfigured/top-level serve, or a `llm host` launched with no reach-back).
type runnerStandup struct {
	home          *coord.Home
	engineHost    *coord.EngineHost
	endpointClose func()
}

// standUpRunner performs the runner standup shared by `llm serve` and `llm
// host`: consume + scrub the coordinator reach-back trio,
// load + apply the backend config, stand up the EngineHost for a delegated
// StructuredChat run, dial home, stand up the runner-local MCP socket, and
// BindHome — everything llm_serve.go's body did EXCEPT the transport tail
// (plugin.Serve vs a lifecycle block, which each caller owns). It returns the
// standup on success, or a FATAL error (a hosted run whose MCP endpoint failed
// — never launch its engine with no reach-back) after closing home itself.
//
// label is the config label whose LLM entry configures the backend, passed by
// whichever command owns the standup — each has its own --label flag, and a
// parameter is what keeps the three from sharing one mutable package global.
func standUpRunner(cmd *cobra.Command, backend agent.Backend, backendName, label string) (*runnerStandup, error) {
	reach, rerr := consumeCoordinatorReachBack(backendName, os.Getenv, os.Unsetenv)
	if rerr != nil {
		return nil, rerr
	}
	homeCfg := reach.home

	cfg, cfgErr := loadAndConfigureBackend(backend, backendName, label)
	if cfgErr != nil {
		return nil, cfgErr
	}

	standup := &runnerStandup{}
	if homeCfg.URL == "" || homeCfg.Token == "" {
		// No reach-back: nothing to dial or host (an unconfigured/top-level
		// serve, or a `llm host` launched without a coordinator).
		return standup, nil
	}

	if homeCfg.RunID != "" {
		if sc, ok := backend.(agent.StructuredChat); ok {
			standup.engineHost = coord.NewEngineHost(cmd.Context(), App().Reporter, sc, backendName, homeCfg.RunID)
			homeCfg.Engine = standup.engineHost.Handle
		}
	}
	// The Hello advertisement, resolved BEFORE NewHome dials: the control kinds
	// ride only when this runner actually hosts an engine that could execute
	// them, so an engineless runner advertises the mailbox surface alone.
	homeCfg.Capabilities = coord.RunnerCapabilities(standup.engineHost != nil)
	homeCfg.Reporter = App().Reporter
	h, herr := coord.NewHome(cmd.Context(), homeCfg)
	if herr != nil {
		clidiag.Warn("ctxloom", "runner dial-home failed (coordinator will synthesize loss): %v", herr)
		return standup, nil
	}
	standup.home = h

	if standup.engineHost == nil {
		// The plugin-hosted owner arm: no Launch will arrive, so its MCP
		// endpoint stands up now, keyed by the harp its env carried.
		if err := attachRunnerMCP(standup, cfg, h, reach.harp, false, ""); err != nil {
			h.Close(1, "")
			return nil, err
		}
		return standup, nil
	}
	// A hosted run: identity arrives on the Launch, so the runner's MCP
	// endpoint (keyed by the harp, gated by the depth) stands up at payload
	// arrival, inside runner.Execute, strictly before the engine is driven.
	runnerDeps, derr := runnerDepsFor(backend, backendName, standup.engineHost, func(ctx context.Context, l launch.Launch) (func(), error) {
		leaf := l.Identity.IsLeaf(cfg.GetDelegationDepth())
		if err := attachRunnerMCP(standup, cfg, h, l.Identity.Harp, leaf, l.Cell.Workspace); err != nil {
			return nil, err
		}
		return standup.endpointClose, nil
	})
	if derr != nil {
		h.Close(1, "")
		return nil, derr
	}
	standup.engineHost.BindRunner(runner.Host{Deps: runnerDeps})
	standup.engineHost.BindHome(h)
	return standup, nil
}

// runnerDepsFor composes the runner's ports for the one engine this process
// hosts: the two package transports (the claim store rooted at this
// process's sessions root — the mounted one inside a container), the
// engine's own Setup as the static writer, the runner MCP standup as the
// dynamic half, the binding-preference validator, the engine's configure
// seam over the label body the Launch carries, and the engine host as the
// driver.
func runnerDepsFor(backend agent.Backend, backendName string, host *coord.EngineHost, dynamic runner.Dynamic) (runner.Deps, error) {
	ctxHome, err := paths.HomeConfigDir()
	if err != nil {
		return runner.Deps{}, fmt.Errorf("runner: sessions root: %w", err)
	}
	kind, ok := backends.Kind(backendName)
	if !ok {
		return runner.Deps{}, fmt.Errorf("runner: no engine kind %q is composed", backendName)
	}
	deps := runner.Deps{
		Kind:       kind,
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: fsstore.PackageStore{Root: filepath.Join(ctxHome, paths.SessionsDir)}},
		Static:     backend,
		Dynamic:    dynamic,
		Surfaces:   operations.ResolveAgentSurfaces,
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

// attachRunnerMCP stands up the runner-local MCP endpoint for harp and
// publishes its socket into this process's environment, recording the
// endpoint's closer on standup. leaf withholds the coordinator-only tools
// (a one-shot run, or one at the delegation-depth cap); cellWorkDir keys the
// discovery marker by the directory the engine runs in ("" = this process's
// cwd). It returns a non-nil error ONLY when the runner must refuse to launch
// its engine: this runner hosts a delegated run (engineHost != nil) and has no
// reach-back for it. The caller owns closing home on that error.
//
// The two refusal conditions are the same condition reached two ways, which is
// why they answer identically: the child's shim keys entirely off
// CTXLOOM_MCP_SOCKET, so an endpoint that failed to come up, an endpoint that
// could not be published, and a config too broken to build one from all leave the
// engine reaching a rogue local coordinator nobody reads. Without a hosted run
// there is nothing to refuse for and the shim's own local fallback is correct, so
// it degrades with a warning.
func attachRunnerMCP(standup *runnerStandup, cfg *config.Config, h *coord.Home, harp string, leaf bool, cellWorkDir string) error {
	endpoint, merr := mcp.ServeRunnerMCP(cfg, harp, h, leaf, cellWorkDir)
	if merr == nil {
		// The child's shim reads CTXLOOM_MCP_SOCKET from THIS process's env
		// (every engine spawn path builds the harness env over os.Environ), so a
		// failed export leaves the endpoint standing and unaddressable — the same
		// end state as no endpoint at all, and treated as the same failure.
		if merr = exportRunnerMCPSocket(os.Setenv, endpoint.SocketPath); merr != nil {
			endpoint.Close()
		}
	}
	switch {
	case merr == nil:
		standup.endpointClose = endpoint.Close
		return nil
	case standup.engineHost != nil:
		return fmt.Errorf("runner MCP endpoint failed and this runner hosts delegated run %s — refusing to launch its engine with no reach-back: %w", h.RunID(), merr)
	default:
		clidiag.Warn("ctxloom", "runner MCP endpoint failed (the harness shim will fall back to its local mode): %v", merr)
		return nil
	}
}

// coordinatorReachBack is the per-spawn coordinator credential set a runner
// consumes from its own environment: the dial-home config (the reach-back
// trio) and, for the plugin-hosted session owner alone, its harp. A hosted
// run's identity — harp, depth, whether it is one-shot, the cell it runs in
// — arrives ONCE on the Launch and is never read from the environment.
type coordinatorReachBack struct {
	home coord.HomeConfig
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
	coord.EnvCellWorkDir,
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
		home: coord.HomeConfig{
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

// teardown reports the runner's exit through home.Close, mirroring
// llm_serve.go's original tail exactly (engine host joined first so an
// in-flight adapt can finish its terminal RunCompleted while home is still
// live; the MCP endpoint closes last).
func (s *runnerStandup) teardown() {
	if s.engineHost != nil {
		s.engineHost.Close()
	}
	if s.home != nil {
		s.home.Close(0, "")
	}
	if s.endpointClose != nil {
		s.endpointClose()
	}
}
