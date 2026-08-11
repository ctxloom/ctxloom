package cli

import (
	"fmt"
	"os"

	"github.com/hashicorp/go-plugin"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	startupMark := strictness.Checkpoint()

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
	if ferr := failOnFindings(os.Stderr, startupMark); ferr != nil {
		return ferr
	}

	// Create the plugin map with our backend
	pluginMap := map[string]plugin.Plugin{
		pb.LLMPluginKey: &pb.LLMGRPCPlugin{Impl: backend},
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
