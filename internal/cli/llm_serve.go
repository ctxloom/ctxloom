package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var llmServeCmd = &cobra.Command{
	Use:     "serve <backend>",
	Aliases: []string{"srv"},
	Short:   "Run as a plugin server (internal use)",
	Long:    `Starts the ctxloom binary as a plugin server for the specified backend. This is used internally by the plugin system.`,
	Args:    cobra.ExactArgs(1),
	Hidden:  true, // Hide from help since it's for internal use
	RunE:    runLLMServe,
}

func runLLMServe(cmd *cobra.Command, args []string) error {
	// Fail-loudly gate: checkpoint before standUpRunner's config
	// load, so a fatal-class finding it records (a corrupted/malformed
	// config.yaml, via config.RecordWarningsTo) aborts this process-owning
	// entry point below instead of silently serving an empty/partial
	// context. Degraded mode (--degraded / CTXLOOM_DEGRADED=1) is the
	// escape hatch, same as `ctxloom run`/`ctxloom mcp`.
	gates := newPhaseGates(os.Stderr)

	backendName := args[0]

	// Get the backend from the registry
	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}

	// Shared runner standup: consume+scrub the coordinator trio, load
	// config, stand up the EngineHost + dial home + runner-local MCP +
	// BindHome (llm_serve.go's former body, now shared with `llm host`).
	// serve's distinction is the transport TAIL below: plugin.Serve.
	standup, err := standUpRunner(cmd, backend, backendName, llmServeLabel)
	if err != nil {
		return err
	}
	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}

	// wrapStreams is set only for the one case that needs a terminal to
	// inject into: a Home with no EngineHost (this run hosts no
	// StructuredChat turn sink — see standUpRunner) means deliverNotice's
	// third case can only buffer an arrival, never hand it to an engine.
	// coord.NewTerminalInjector gives that Home's nudge a live stdin to write
	// into whenever this process actually drives one interactively. This is a
	// func value threaded through the plugin/server, not a Backend decorator,
	// so it cannot erase an optional capability interface (agent.StructuredChat,
	// agent.StateReader, agent.EngineCLIProvider) the backend implements — see
	// grpc.LLMGRPCPlugin.WrapStreams.
	var wrapStreams func(io.Reader, io.Writer) (io.Reader, io.Writer, func())
	if standup.home != nil && standup.engineHost == nil {
		// ONE injector per Home, constructed OUT here and re-Wrapped per turn.
		// Constructing it per turn instead builds a new injector every time,
		// and Home.SetTerminalNudge refuses a second registration by design —
		// so the registered nudge stays bound to the FIRST turn's stdin,
		// which nothing reads once that turn ends, and later mail is injected
		// into a dead reader. Wrap re-points the injection target under the
		// mutex, so re-Wrapping is how a turn takes ownership; the release it
		// returns is how a turn gives that ownership back.
		ti := coord.NewTerminalInjector(standup.home)
		wrapStreams = ti.Wrap
	}

	// Create the plugin map with our backend
	pluginMap := map[string]plugin.Plugin{
		pb.LLMPluginKey: &pb.LLMGRPCPlugin{Impl: backend, WrapStreams: wrapStreams},
	}

	// plugin.Serve below blocks with no signal handling of its own (it
	// swallows SIGINT and leaves SIGTERM at its default disposition), so
	// this is `llm serve`'s only chance to react to the SIGTERM the kernel
	// delivers when its host dies — isolateRunner's Pdeathsig. Without it
	// the runner would die and strand the engine subprocess it isolated
	// into its own process group. `llm host` deliberately does NOT install
	// this: it already unwinds through waitForRunnerTermination into
	// standup.teardown, and two handlers on one signal race.
	pb.InstallRunnerTeardown()

	// Serve the plugin
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pb.HandshakeConfig,
		Plugins:         pluginMap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})

	standup.teardown()
	return nil
}

var llmServeLabel string

// serveBackendConfig picks the labeled entry to configure the served backend
// with. The label the run resolved wins (verified against the backend type so
// a stale flag can't misconfigure); the type-based scan remains the fallback
// for callers that only know the type — its map-order tie is exactly why the
// run path passes the label through.
func serveBackendConfig(cfg *config.Config, backendName, label string) agent.BackendConfig {
	if label != "" {
		if entry, ok := cfg.GetLLMEntry(label); ok && entry.EffectiveType() == backendName {
			if bc := operations.DecodeBackendConfig(cfg, label); bc != nil {
				return bc
			}
		}
		clidiag.Warn("ctxloom", "label %q does not resolve to backend %s; falling back to type lookup", label, backendName)
	}
	return decodeBackendConfigForType(cfg, backendName)
}

func init() {
	llmCmd.AddCommand(llmServeCmd)
	llmServeCmd.Flags().StringVar(&llmServeLabel, "label", "", "Config label to apply (internal; passed by the self-invoking run path)")
}
