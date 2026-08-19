package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	acpRunLLM       string
	acpRunProfile   string
	acpRunWorkDir   string
	acpRunVerbosity int
	// acpRunOneShot selects the single-turn form: one prompt in, the answer
	// out, exit. Bare is the session form — a conversation that keeps taking
	// turns until you end it. That is the same split `ctxloom run` draws, in
	// the same word, because the two verbs mean the same thing and differ only
	// in which engine surface they drive.
	acpRunOneShot bool
)

// acpRunFactory is a test seam over this leaf's plugin-client construction:
// nil self-invokes the compiled-in backend through the ONE sanctioned door for
// reaching an engine (pb.Client.Chat/Run via the plugin tree —
// internal/acptest.TestNoImportInvariant_ACPClientOnlyFromPluginTree polices
// this: internal/cli must never import internal/acp, the ACP client driver,
// directly). Tests inject a stub pb.Client so no real ACP subprocess is
// spawned — same shape as init.go's authPingFactory.
var acpRunFactory pb.ClientFactory

// errACPRunLLMRequired is returned when --llm is unset; a sentinel (not a
// string-matched error) per CLAUDE.md's error-handling standard.
var errACPRunLLMRequired = fmt.Errorf("--llm is required: the configured ACP-CLIENT llm label to drive (an 'llm:' entry in .ctxloom/config.yaml with type: acp — see `ctxloom llm list`)")

// acpRunCmd drives ctxloom's OUTBOUND ACP direction: a configured 'llm:'
// label whose backend type is "acp" — ctxloom's generic outbound ACP driver
// (internal/acp), which spawns that label's configured 'command' (e.g.
// "kiro-cli acp", "claude-code-acp", "codex-acp") and speaks Agent Client
// Protocol to it. Both forms reach that engine through the plugin tree — the
// SAME door 'ctxloom run --llm <label>' uses for any other engine — rather
// than constructing internal/acp's driver in-process: ctxloom's own
// architecture requires every engine spawn to go through that one door
// (label-resolved from project config, launched via the plugin subprocess),
// never a CLI-local ad hoc exec surface — see NewChatDriver's callers in
// internal/lm/backends/registry.go for the sanctioned construction sites.
var acpRunCmd = &cobra.Command{
	Use:   "run [prompt]",
	Short: "Connect ctxloom OUT to a configured ACP-speaking agent: a session, or one turn with --one-shot",
	Long: `Experimental — interfaces may change and it is not yet verified against all editors.

Drive a configured ACP-CLIENT llm entry: an 'llm:' label in
.ctxloom/config.yaml whose type is "acp" — ctxloom's generic outbound ACP
driver, which spawns the label's configured 'command' (e.g. "kiro-cli acp",
"claude-code-acp", "codex-acp") and speaks Agent Client Protocol to it.

Bare, this opens a SESSION with that agent: one typed line is one turn, the
agent's turns render as they arrive, and Ctrl-D (EOF) ends the conversation.
A prompt on the command line opens the session with that first turn and then
keeps listening.

--one-shot drives a SINGLE turn instead — prompt in, response out, exit —
and takes its prompt from piped stdin when the command line carries none.
That is the same word, for the same mode, as 'ctxloom run --one-shot'.

--llm names the label (required — 'ctxloom llm list' shows what's
configured; no ACP-type label yet? add one under 'llm:' with 'type: acp'
and a 'command:' naming the target's ACP-mode invocation). --profile
assembles a profile's context, delivered as the lead block of the first
turn.

ACP is structured/headless — the session is a turn loop over the protocol,
not the agent's own TUI. This is the exact backend a full 'ctxloom run --llm
<label>' session against an acp-type engine drives; 'acp run' is the direct
door onto it: verify a configured ACP agent actually answers before wiring
it into an agent binding (the acp-setup skill uses it this way).

Examples:
  ctxloom acp run --llm kiro
  ctxloom acp run --llm kiro "what do you see in this repo?"
  ctxloom acp run --llm kiro --one-shot "say hello"
  git diff | ctxloom acp run --llm kiro --profile reviewer --one-shot`,
	Args: cobra.ArbitraryArgs,
	RunE: runACPRun,
}

func runACPRun(cmd *cobra.Command, args []string) error {
	if acpRunLLM == "" {
		return errACPRunLLMRequired
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	return runACPRunWithConfig(cmd, cfg, strings.Join(args, " "), os.Stdin, stdinIsPiped())
}

// runACPRunWithConfig chooses the form the invocation asked for. It takes cfg,
// the turn source and the piped-stdin answer directly so the choice itself is
// testable without a real .ctxloom project or a real terminal: which of the two
// forms a given invocation runs is the one thing about this leaf that must
// never quietly become the other.
func runACPRunWithConfig(cmd *cobra.Command, cfg *config.Config, prompt string, stdin io.Reader, stdinPiped bool) error {
	if acpRunOneShot {
		// The universal-reducer rule `ctxloom run --one-shot` follows: with no
		// prompt on the command line, piped stdin IS the prompt, and a
		// one-shot with nothing to ask refuses rather than spending its one
		// turn on an empty question.
		prompt, err := finalizeRunPrompt(prompt, true, stdinPiped, stdin)
		if err != nil {
			return err
		}
		return runACPOneshotWithConfig(cmd, cfg, prompt)
	}
	return runACPSessionWithConfig(cmd, cfg, prompt, stdin)
}

// runACPOneshotWithConfig is the --one-shot form's core, taking cfg directly so
// it is unit-testable without a real .ctxloom project on disk (GetConfig always
// resolves from the ambient project). Reads the bound flag vars (llm/
// profile/workdir/verbosity) exactly as runACPRun does.
func runACPOneshotWithConfig(cmd *cobra.Command, cfg *config.Config, prompt string) error {
	if _, err := acpRunBackend(cfg); err != nil {
		return err
	}
	workDir, err := acpRunWorkingDir()
	if err != nil {
		return err
	}

	result, err := operations.RunOneshot(cmd.Context(), cfg, operations.RunOneshotRequest{
		Profile:   acpRunProfile,
		Task:      prompt,
		LLM:       acpRunLLM,
		WorkDir:   workDir,
		Verbosity: acpRunVerbosity,
		Factory:   acpRunFactory,
	})
	if err != nil {
		return err
	}
	return emit(cmd, result, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Println(result.Output)
		return w.Err()
	})
}

// runACPSessionWithConfig is the bare form's core: it opens ONE conversation
// with the configured agent and drives turns through it until the caller ends
// it. opening is the command-line prompt (empty: the session opens with the
// first line read), and stdin supplies every turn after that.
//
// It drives the engine's structured-chat surface — the same substrate `acp
// serve` drives — because that is what an ACP agent offers: a conversation of
// turns over the protocol. Permission requests are answered by the posture the
// label configures: this loop has no connected editor client to forward them
// to (unlike `acp serve`, which does), so there is no prompt to ask.
func runACPSessionWithConfig(cmd *cobra.Command, cfg *config.Config, opening string, stdin io.Reader) error {
	backendName, err := acpRunBackend(cfg)
	if err != nil {
		return err
	}
	workDir, err := acpRunWorkingDir()
	if err != nil {
		return err
	}

	ctxResult, err := operations.AssembleContext(cmd.Context(), cfg, operations.AssembleContextRequest{
		Profile: acpRunProfile,
	})
	if err != nil {
		return fmt.Errorf("assemble context: %w", err)
	}
	// Naming a profile and delivering none of its context is never what the
	// caller asked for, and the session would still open, still answer, and
	// still look right. This check is a hand-copied duplicate of
	// operations.runResolvedAgent's analogous empty-context refusal for the
	// single-turn form, not shared code — the two forms do not run through a
	// common choke point, so a change to one is not a change to the other.
	if acpRunProfile != "" && strings.TrimSpace(ctxResult.Context) == "" {
		return fmt.Errorf("empty context: profile %q assembled to nothing — refusing to open the session unspecialised (check the profile's fragments/bundles resolve, or drop --profile to open it context-free)", acpRunProfile)
	}

	_, model := operations.ResolveBackend(cfg, acpRunLLM)
	labelEntry, _ := cfg.GetLLMEntry(acpRunLLM)
	// A session has a human in it, so a posture that gates each tool call is
	// honorable here and is left as declared — the headless flooring that a
	// one-shot needs (it cannot answer a prompt, so it would hang) is exactly
	// what must NOT happen to an interactive turn loop.
	permission := resolvePermissionMode("", "", labelEntry.Permissions, cfg.GetPermissions(), backendName,
		pb.ExecutionMode_INTERACTIVE, backends.EnforcesReadOnlyPlan(backendName))
	// A session with no channel to render or answer a permission prompt
	// (there is no connected editor client here, unlike `acp serve`) must
	// refuse a posture that would block on one, rather than open a
	// conversation where every gated call silently gets reject_once with
	// nobody asked. SafeHeadless is exactly the right predicate even though
	// this is an interactive session, not a headless run: true for bypass
	// (nothing prompts) and for plan (but acp cannot enforce a genuine
	// read-only plan, so CollapsePlanIfUnenforced already turned a
	// configured plan into default above — refusing that too is correct);
	// false for default/acceptEdits, precisely the silently-rejecting
	// postures. This is INTERIM: the follow-up that lifts it is rendering
	// and answering permission prompts in runChatSession.
	if !permission.SafeHeadless() {
		return fmt.Errorf("acp run: permission posture %q cannot open this session — this session has no channel to render or answer an engine permission prompt (not built yet), so every gated call would be silently rejected with nobody asked; set llm %q's permissions to %q, the only posture acp can run headless-safe here",
			permission, acpRunLLM, agent.PermissionBypass)
	}

	warnACPSessionWorkspaceAxis(cfg.GetWorkspace())

	// The project's RUNTIME axis, parsed here — as early as practical for
	// this single-turn form, via the single canonical ParseRuntimeAxis — so a
	// typo'd project `runtime:` fails loud instead of riding into
	// ChatRequest.Runtime as an unvalidated string.
	runtimeAxis, err := agent.ParseRuntimeAxis(cfg.GetRuntime())
	if err != nil {
		return fmt.Errorf("project `runtime:` default: %w", err)
	}

	factory := acpRunFactory
	if factory == nil {
		factory = pb.DefaultClientFactory()
	}
	client, err := factory(backendName, acpRunLLM, acpRunVerbosity)
	if err != nil {
		return fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	announceACPSession(cmd, acpRunLLM, model)

	return runChatSession(cmd.Context(), client, agent.ChatRequest{
		WorkDir:     workDir,
		Model:       model,
		Permissions: permission,
		// The project's RUNTIME axis rides the conversation (ISO1): a
		// container project runs the engine subprocess in a container, and
		// internal/acp fails loudly if it cannot, rather than quietly
		// answering from the host. The single-turn form already resolves this
		// axis through operations.RunOneshot, and two forms of one verb that
		// isolate differently is a difference nobody would think to look for.
		Runtime: runtimeAxis,
	}, chatTurns{
		Lead:    ctxResult.Context,
		Opening: opening,
		Stdin:   stdin,
	}, outputFormatOf(cmd), cmd.OutOrStdout())
}

// acpRunBackend resolves --llm and refuses any label that is not an ACP-client
// entry. Both forms consult it: they reach the same engine surface, so a label
// configured for a different engine is as wrong for one as for the other.
func acpRunBackend(cfg *config.Config) (string, error) {
	backendName, _ := operations.ResolveBackend(cfg, acpRunLLM)
	if backendName != "acp" {
		return "", fmt.Errorf("llm %q resolves to backend %q, not \"acp\" — `ctxloom acp run` only drives a configured ACP-CLIENT entry (an 'llm:' label with type: acp); see `ctxloom llm list`", acpRunLLM, backendName)
	}
	return backendName, nil
}

// acpRunWorkingDir is the directory the target agent runs in: --workdir, else
// the launch directory.
func acpRunWorkingDir() (string, error) {
	if acpRunWorkDir != "" {
		return acpRunWorkDir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return wd, nil
}

// warnACPSessionWorkspaceAxis says out loud that the session runs in the
// project directory when the project's workspace axis asks for a worktree.
//
// The WORKSPACE axis has no carrier on a chat conversation, so this form
// cannot honor it — the same limit `ctxloom acp serve` has, which is why
// worktree isolation there is reserved for an explicitly-named agent. Left
// silent, a project configured for worktree isolation would get a session
// editing the live tree while every other surface gave it a worktree, and
// nothing anywhere would name the difference. `ctxloom run --agent` is the
// surface that isolates the files.
func warnACPSessionWorkspaceAxis(workspace string) {
	// A spelling the axis does not admit is its own warning: this entry has
	// no error path to refuse on, and silence here would leave a project
	// whose `workspace:` is a typo believing it asked for something.
	axis, err := isolation.ParseWorkspaceAxis(workspace)
	if err != nil {
		clidiag.Warn("ctxloom", "%s — this project's workspace axis is set to a value nothing can honor; fix `workspace:` in .ctxloom/config.yaml", err)
		return
	}
	if axis != isolation.WorkspaceWorktree {
		return
	}
	clidiag.Warn("ctxloom", "this project's workspace axis is %q, and an ACP session has no carrier for it: this conversation runs in the project directory. Use `ctxloom run --agent <name> --workspace worktree` for a session with its own worktree.", workspace)
}

// announceACPSession tells a person that the conversation is open and how to
// end it. A session that has opened but not yet been typed at prints nothing,
// which is indistinguishable from a command that hung. The same invocation
// with its turns piped in has no person to tell and wants none of the chatter,
// so the announcement follows the TERMINAL rather than the mode, and goes to
// stderr, where it never mixes into the conversation's own output.
func announceACPSession(cmd *cobra.Command, label, model string) {
	if !isInteractiveTerminal() {
		return
	}
	engine := label
	if model != "" {
		engine += " · " + model
	}
	w := iox.NewErrWriter(cmd.ErrOrStderr())
	w.Printf("ctxloom: session open with %s — one line is one turn; Ctrl-D ends it.\n", engine)
	_ = w.Err()
}

func init() {
	acpRunCmd.Flags().StringVar(&acpRunLLM, "llm", "", "the configured ACP-client llm label to drive (an 'llm:' entry with type: acp; required)")
	acpRunCmd.Flags().StringVar(&acpRunProfile, "profile", "", "profile to assemble context from, delivered as the first turn's lead block (default: none — just the prompt)")
	acpRunCmd.Flags().StringVar(&acpRunWorkDir, "workdir", "", "working directory the target agent runs in (default: cwd)")
	acpRunCmd.Flags().IntVar(&acpRunVerbosity, "verbosity", 0, "plugin transport verbosity")
	acpRunCmd.Flags().BoolVar(&acpRunOneShot, "one-shot", false, "Run one turn non-interactively, print the response, and exit (default: open a session)")
	_ = acpRunCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = acpRunCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
}
