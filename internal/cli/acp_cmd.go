package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var (
	acpProfile string
	acpLLM     string
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Serve ctxloom as an Agent Client Protocol agent (stdio)",
	Long: `Serve ctxloom AS an ACP agent over stdio, so any ACP client (Zed's agent
panel, editor plugins) can drive ctxloom sessions — assembled context,
profiles, and the configured engine — without a bespoke per-editor frontend.

Each session/new opens one engine conversation rooted at the request's cwd
(ctxloom config is discovered from there); ctxloom's assembled context rides
the first turn as a lead block. Engine permissions are auto-approved for now,
mirroring the non-interactive oneshot rule — forwarding permission requests to
the connected editor is a planned addition.

Stdout carries the protocol; all diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return acpagent.Serve(cmd.Context(), os.Stdin, os.Stdout, func(ctx context.Context, cwd string) (*acpagent.EngineChat, error) {
			return openACPEngineChat(ctx, cwd, acpProfile, acpLLM)
		})
	},
}

// openACPEngineChat is the production ChatOpener: it loads ctxloom config for
// the session's cwd, assembles the profile's context, resolves the engine
// label (override → profile llm → primary), and opens the plugin's structured
// chat — the same substrate `run --structured` drives.
func openACPEngineChat(ctx context.Context, cwd, profile, llmOverride string) (*acpagent.EngineChat, error) {
	cfg, err := loadConfigForDir(cwd)
	if err != nil {
		return nil, err
	}

	// Context assembly is fault-tolerant (CLAUDE.md): a failed assembly warns
	// and the session still opens — the engine runs without ctxloom context
	// rather than refusing the editor's session.
	var contextText, profileLLM string
	if ctxResult, cerr := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{Profile: profile}); cerr != nil {
		clidiag.Warn("ctxloom", "acp agent: context assembly failed; continuing without context: %v", cerr)
	} else {
		contextText = ctxResult.Context
		profileLLM = ctxResult.ProfileLLM
	}

	label := llmOverride
	if label == "" {
		label = profileLLM
	}
	if label == "" {
		label = cfg.PrimaryLabel()
	}
	backendName, model := operations.ResolveBackend(cfg, label)

	client, err := pb.DefaultClientFactory()(backendName, label, 0)
	if err != nil {
		return nil, err
	}
	// AutoApprove mirrors the non-interactive oneshot rule: there is no
	// terminal prompt on this path (permission forwarding to the ACP client is
	// the next slice).
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir:     cwd,
		Model:       model,
		AutoApprove: true,
	})
	if err != nil {
		client.Kill()
		return nil, err
	}
	return &acpagent.EngineChat{
		Context: contextText,
		In:      in,
		Events:  events,
		Errs:    errs,
		Close:   client.Kill,
	}, nil
}

// loadConfigForDir loads ctxloom config as seen FROM dir (the ACP session's
// cwd, which need not be this server process's cwd): it walks dir upward for a
// .ctxloom directory and pins config loading to it; absent one, the default
// discovery (process cwd, then home) applies.
func loadConfigForDir(dir string) (*config.Config, error) {
	for d := dir; ; {
		appDir := filepath.Join(d, config.AppDirName)
		if info, err := os.Stat(appDir); err == nil && info.IsDir() {
			return config.Load(config.WithAppDir(appDir))
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return config.Load()
}

func init() {
	acpCmd.Flags().StringVarP(&acpProfile, "profile", "p", "", "profile to assemble context from (default: the configured defaults)")
	acpCmd.Flags().StringVarP(&acpLLM, "llm", "l", "", "LLM config label to drive (default: the profile's llm, then the primary)")
	rootCmd.AddCommand(acpCmd)
}
