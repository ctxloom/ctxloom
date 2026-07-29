package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var (
	acpProfile   string
	acpLLM       string
	acpAgent     string
	acpWorkspace string
)

// acpCmd is the ACP (Agent Client Protocol) command tree: SERVER (ctxloom
// speaks ACP to an editor that connects in) and CLIENT (ctxloom connects OUT
// to an ACP-speaking agent) are the two directions — see acpServerCmd and
// acpClientCmd. Bare `ctxloom acp` (no subcommand) is a deprecated alias for
// `acp server`, its historical behavior, kept working exactly as before
// (CLI-primary reorg plan, Decision 5) — runACPServerBareAlias prints the
// one-line pointer itself.
//
// Deliberately NOT cobra's `Deprecated` field: cobra's IsAvailableCommand
// treats ANY non-empty Deprecated as unavailable UNCONDITIONALLY (checked
// before Runnable/HasAvailableSubCommands — see cobra's command.go), and both
// `doc.GenMarkdownTreeCustom` (gen-docs) and the root --help subcommand
// listing skip an unavailable command's entire subtree without recursing —
// so marking THIS node deprecated would silently hide acp server/client/
// entries too, exactly the commands the reorg wants MORE discoverable, not
// less. Only leaf commands with no children (acpAgentsCmd, agentSetupCmd)
// use the real field; a parent with a bare-invocation alias prints its own
// notice instead (see trustCmd's shape for the leaf-only pattern this
// deviates from, and why).
var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "ACP (Agent Client Protocol): serve ctxloom to an editor, or connect ctxloom out to an ACP-speaking agent",
	Long: `Experimental — interfaces may change and it is not yet verified against all editors.

ACP (Agent Client Protocol) has two directions under ctxloom, each its own
subcommand:

  ctxloom acp server   ctxloom SERVES the protocol (stdio) so an ACP editor
                        client (Zed's agent panel, VS Code, Nori, ...)
                        connects IN and drives ctxloom sessions.
  ctxloom acp client    ctxloom CONNECTS OUT to an ACP-speaking agent (a
                        third-party command that itself speaks ACP) and
                        drives one headless turn against it.
  ctxloom acp entries   Lists the ACP agent-server entries to paste into an
                        editor's config for the server direction.

Bare 'ctxloom acp' (no subcommand) is a deprecated alias for 'acp server'.`,
	Args: cobra.NoArgs,
	RunE: runACPServerBareAlias,
}

// acpBareDeprecation is the one-line pointer bare `ctxloom acp` prints (same
// shape/wording cobra's own Deprecated field would print, reproduced by hand
// here — see acpCmd's doc comment for why the real field can't be used).
const acpBareDeprecation = "use `ctxloom acp server` instead"

// runACPServerBareAlias is bare acpCmd's RunE: prints the deprecation pointer
// to stderr, then runs the exact same serve logic as `acp server` (runACPServer)
// — additive shim, not yet a removal (CLAUDE.md/plan Phase 1 posture).
func runACPServerBareAlias(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "Command %q is deprecated, %s\n", cmd.Name(), acpBareDeprecation)
	return runACPServer(cmd, args)
}

// acpServerCmd is the real home of ctxloom-as-ACP-server: ctxloom serves the
// protocol over stdio so an ACP client connects in. This is the exact
// pre-reorg bare `ctxloom acp` behavior, moved under an explicit noun (CLI-
// primary reorg plan, Decision 5).
var acpServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Serve ctxloom as an Agent Client Protocol agent (stdio) for an editor to connect to",
	Long: `Experimental — interfaces may change and it is not yet verified against all editors.

Serve ctxloom AS an ACP agent over stdio, so any ACP client (Zed's agent
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
from the editor — 'ctxloom acp entries' prints the entries ready to paste.

--workspace worktree runs that agent's session in its own fresh git worktree
(ISO2) instead of the editor's own cwd — reuses the exact worktree machinery
'ctxloom run --agent'/agent_run drive, with a first-turn announcement of the
worktree path (the editor's own view stays unaware of it). Requires --agent:
worktree isolation is for a deliberately-configured agent entry, never the
plain 'ctxloom acp server' one, even when the project's 'workspace:' default
is "worktree" for map/weave.

Stdout carries the protocol; all diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: runACPServer,
}

// runACPServer is the ACP-server RunE, shared by acpServerCmd (the real home)
// and the Deprecated bare acpCmd alias, so the two can never drift.
func runACPServer(cmd *cobra.Command, args []string) error {
	// U034-F03: --workspace is documented (acpServerCmd's Long text, above) as
	// "Requires --agent" — worktree isolation is for a deliberately-configured
	// agent entry, never the plain, agent-less server. acpWorkspaceAxis
	// (internal/operations/engine_session.go) already silently returns the
	// empty axis whenever acpAgent is empty; left unchecked, a user who
	// passes --workspace worktree with no --agent gets NO isolation and is
	// told nothing — the worst failure direction for an isolation flag.
	// Checked here (not a PreRunE) so the bare `acp` alias and `acp server`
	// share one enforcement point without duplicating the wiring.
	if err := validateACPWorkspaceRequiresAgent(cmd, acpAgent); err != nil {
		return err
	}
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
		return operations.OpenEngineSession(ctx, req, acpCoord, acpProfile, acpAgent, acpLLM, acpWorkspace)
	})
}

// validateACPWorkspaceRequiresAgent enforces U034-F03: --workspace only means
// anything paired with --agent (acpWorkspaceAxis silently returns the empty
// axis otherwise). Split out from runACPServer so it can be unit tested
// without going anywhere near acpagent.Serve's blocking stdio read loop.
func validateACPWorkspaceRequiresAgent(cmd *cobra.Command, agent string) error {
	if cmd.Flags().Changed("workspace") && agent == "" {
		return fmt.Errorf("--workspace requires --agent (worktree isolation is scoped to a specific agent entry, not the plain acp server)")
	}
	return nil
}

// registerACPServerFlags binds the ACP-server flag set to cmd. Shared by the
// top-level `acp` (deprecated bare-serve alias) and `acp server` so both
// surfaces accept the same options against the same package-level flag vars:
// a cobra command has exactly one parent, so the alias must be a distinct
// command sharing the flags/RunE, not the same *cobra.Command.
func registerACPServerFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&acpProfile, "profile", "p", "", "profile to assemble context from (default: the configured defaults)")
	cmd.Flags().StringVarP(&acpLLM, "llm", "l", "", "LLM config label to drive (default: the agent's/profile's llm, then the primary)")
	cmd.Flags().StringVarP(&acpAgent, "agent", "a", "", "agent to serve as the agent: its composed profiles + engine binding (see 'ctxloom agent list')")
	cmd.Flags().StringVar(&acpWorkspace, "workspace", "", "Session workspace axis (none|worktree; empty = project default). Honored only together with --agent (ISO2)")
	cmd.MarkFlagsMutuallyExclusive("profile", "agent")
	_ = cmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
}

func init() {
	registerACPServerFlags(acpCmd)
	registerACPServerFlags(acpServerCmd)
	acpCmd.AddCommand(acpServerCmd)
	acpCmd.AddCommand(acpClientCmd)
	rootCmd.AddCommand(acpCmd)
}
