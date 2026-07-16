package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var (
	acpProfile string
	acpLLM     string
	acpAgent   string
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Serve ctxloom as an Agent Client Protocol agent (stdio)",
	Long: `Serve ctxloom AS an ACP agent over stdio, so any ACP client (Zed's agent
panel, editor plugins) can drive ctxloom sessions — assembled context,
profiles, and the configured engine — without a bespoke per-editor frontend.

Each session/new opens one engine conversation rooted at the request's cwd
(ctxloom config is discovered from there); ctxloom's assembled context rides
the first turn as a lead block, and client-supplied mcpServers pass through to
the engine. Engine permission requests forward to the connected editor as
session/request_permission — the editor's own approval UI decides. ctxloom
profile sets (the composed defaults, each profile, each agent's composed
set) surface as ACP session modes; session/set_mode re-assembles the lead
context for the next turn, while the ENGINE stays pinned at launch. Sessions
are recorded under ctxloom harp names, and session/load resumes a recorded
harp: its history replays to the client and primes the fresh engine
conversation.

--agent serves one agent as the agent: its composed profiles become the
session context and its engine binding picks the backend (an explicit --llm
still wins). Configure one editor agent entry per agent to pick agents
from the editor — 'ctxloom acp agents' prints the entries ready to paste.

Stdout carries the protocol; all diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// COORDINATOR HOSTING (agentcoord Wave B1, review R1): an ACP-launched
		// session has no run wrapper — acp opens engines directly — so it
		// stands the runtime coordinator up itself, one per acp process,
		// keyed by the process cwd's project. Each session/new mints its own
		// owner credential and injects the reach-back trio. A standup failure
		// degrades to no delegation (warned): the editor's chat still opens.
		var acpCoord = newACPCoordinator()
		defer acpCoord.close()
		// ISO0: the session opener itself lives in operations.OpenEngineSession
		// (internal/operations/engine_session.go) — frontend-neutral, so ISO1/
		// ISO2 and any future ACP-shaped surface reuse the same value-injecting
		// opener instead of re-implementing it. acpCoord satisfies
		// operations.EngineSessionCoordinator structurally.
		return acpagent.Serve(cmd.Context(), os.Stdin, os.Stdout, func(ctx context.Context, req acpagent.OpenRequest) (*acpagent.EngineChat, error) {
			return operations.OpenEngineSession(ctx, req, acpCoord, acpProfile, acpAgent, acpLLM)
		})
	},
}

func init() {
	acpCmd.Flags().StringVarP(&acpProfile, "profile", "p", "", "profile to assemble context from (default: the configured defaults)")
	acpCmd.Flags().StringVarP(&acpLLM, "llm", "l", "", "LLM config label to drive (default: the agent's/profile's llm, then the primary)")
	acpCmd.Flags().StringVarP(&acpAgent, "agent", "a", "", "agent to serve as the agent: its composed profiles + engine binding (see 'ctxloom agent list')")
	acpCmd.MarkFlagsMutuallyExclusive("profile", "agent")
	rootCmd.AddCommand(acpCmd)
}
