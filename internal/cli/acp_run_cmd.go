package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	acpRunLLM       string
	acpRunProfile   string
	acpRunWorkDir   string
	acpRunVerbosity int
	// acpRunOneShot is `run --one-shot`'s spelling on this leaf, and it is
	// REQUIRED here rather than defaulted.
	//
	// This leaf drives exactly one turn; there is no interactive form of it.
	// A flag that names a mode implies the other mode exists, so a bare
	// invocation that quietly behaved as a one-shot would tell a caller they
	// had opened a session when they had not. Requiring the word says out loud
	// what the leaf does, without inventing a capability that is not there.
	acpRunOneShot bool
)

// acpRunFactory is a test seam over operations.RunOneshot's plugin-client
// construction: nil self-invokes the compiled-in backend through the ONE
// sanctioned door for reaching an engine (pb.Client.Chat/Run via the plugin
// tree — internal/acptest.TestNoImportInvariant_ACPClientOnlyFromPluginTree
// polices this: internal/cli must never import internal/acp, the ACP client
// driver, directly). Tests inject a stub pb.Client so no real ACP subprocess
// is spawned — same shape as init.go's authPingFactory.
var acpRunFactory pb.ClientFactory

// errACPRunLLMRequired is returned when --llm is unset; a sentinel (not a
// string-matched error) per CLAUDE.md's error-handling standard.
var errACPRunLLMRequired = fmt.Errorf("--llm is required: the configured ACP-CLIENT llm label to drive (an 'llm:' entry in .ctxloom/config.yaml with type: acp — see `ctxloom llm list`)")

// acpRunCmd drives ctxloom's OUTBOUND ACP direction: a configured 'llm:'
// label whose backend type is "acp" — ctxloom's generic outbound ACP driver
// (internal/acp), which spawns that label's configured 'command' (e.g.
// "kiro-cli acp", "claude-code-acp", "codex-acp") and speaks Agent Client
// Protocol to it. This rides operations.RunOneshot — the SAME plugin-tree
// door 'ctxloom run --llm <label>' uses for any other engine — rather than
// constructing internal/acp's driver in-process: ctxloom's own architecture
// requires every engine spawn to go through that one door (label-resolved
// from project config, launched via the plugin subprocess), never a
// CLI-local ad hoc exec surface — see NewChatDriver's callers in
// internal/lm/backends/registry.go for the sanctioned construction sites.
var acpRunCmd = &cobra.Command{
	Use:   "run <prompt>",
	Short: "Connect ctxloom OUT to a configured ACP-speaking agent and drive one headless turn",
	Long: `Experimental — interfaces may change and it is not yet verified against all editors.

Drive ONE headless turn against a configured ACP-CLIENT llm entry: an
'llm:' label in .ctxloom/config.yaml whose type is "acp" — ctxloom's generic
outbound ACP driver, which spawns the label's configured 'command' (e.g.
"kiro-cli acp", "claude-code-acp", "codex-acp") and speaks Agent Client
Protocol to it.

--llm names the label (required — 'ctxloom llm list' shows what's
configured; no ACP-type label yet? add one under 'llm:' with 'type: acp'
and a 'command:' naming the target's ACP-mode invocation). --profile
optionally assembles a profile's context as the lead fragment, exactly as
any other oneshot run.

This is the exact backend a full 'ctxloom run --llm <label>' session
against an acp-type engine would drive — 'acp run' is the smoke-test/ad
hoc door onto it: verify a configured ACP agent actually answers before
wiring it into an agent binding (the acp-setup skill's client branch uses
it this way).

ACP is structured/headless only (no TUI) — this is one turn, not a session,
which is why --one-shot is required rather than assumed: the flag states the
mode the invocation actually runs in, the same word 'ctxloom run --one-shot'
uses.`,
	Args: cobra.ExactArgs(1),
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
	return runACPRunWithConfig(cmd, cfg, args[0])
}

// runACPRunWithConfig is runACPRun's core, taking cfg directly so it is
// unit-testable without a real .ctxloom project on disk (GetConfig always
// resolves from the ambient project). Reads the bound flag vars (llm/
// profile/workdir/verbosity) exactly as runACPRun does.
func runACPRunWithConfig(cmd *cobra.Command, cfg *config.Config, prompt string) error {
	if backendName, _ := operations.ResolveBackend(cfg, acpRunLLM); backendName != "acp" {
		return fmt.Errorf("llm %q resolves to backend %q, not \"acp\" — `ctxloom acp run` only drives a configured ACP-CLIENT entry (an 'llm:' label with type: acp); see `ctxloom llm list`", acpRunLLM, backendName)
	}

	workDir := acpRunWorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		workDir = wd
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

func init() {
	acpRunCmd.Flags().StringVar(&acpRunLLM, "llm", "", "the configured ACP-client llm label to drive (an 'llm:' entry with type: acp; required)")
	acpRunCmd.Flags().StringVar(&acpRunProfile, "profile", "", "profile to assemble context from as the lead fragment (default: none — just the prompt)")
	acpRunCmd.Flags().StringVar(&acpRunWorkDir, "workdir", "", "working directory the target agent runs in (default: cwd)")
	acpRunCmd.Flags().IntVar(&acpRunVerbosity, "verbosity", 0, "plugin transport verbosity")
	acpRunCmd.Flags().BoolVar(&acpRunOneShot, "one-shot", false, "Run one turn non-interactively, print the response, and exit (required: this leaf has no interactive form)")
	_ = acpRunCmd.MarkFlagRequired("one-shot")
	_ = acpRunCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = acpRunCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
}
