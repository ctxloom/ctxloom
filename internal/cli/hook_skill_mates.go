package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/claude"
	claudeengine "github.com/ctxloom/ctxloom/internal/claude/engine"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/turnchange"
)

// skillMatesProg names this hook on the clidiag warning channel.
const skillMatesProg = "ctxloom hook skill-mates"

var hookSkillMatesCmd = &cobra.Command{
	Use:    "skill-mates",
	Hidden: true, // Machine callback (PostToolUse hook, Skill matcher) - not for direct use
	Short:  "Name a completed skill's link-group mates the session has not invoked",
	Long: `Reads a PostToolUse hook payload on stdin and, when the completed tool was a
Skill call whose skill belongs to a link group with members this session has
not yet invoked, emits one additionalContext line naming them.

A link group's skills deliver together, but the second skill of a consequent
pair has a condition that is false when the human speaks and turns true only
once the first has run -- which no listing text can say. This hook says it at
the one moment it is true.

"Not yet invoked" is read off the session's own transcript (its prior Skill
tool calls), so nothing is persisted. The hook is silent -- {} -- for any
other tool, for a skill in no group, and once every mate has been invoked.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookSkillMates,
}

// runHookSkillMates never reports a nonzero exit and always leaves one JSON
// object on stdout: a PostToolUse hook that fails interrupts the tool call it
// rode on, and a missed line costs one nudge, not a session. Every reason the
// line was not emitted is NAMED on the diagnostic channel instead.
func runHookSkillMates(cmd *cobra.Command, args []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "%s: panic: %v\n", skillMatesProg, r)
			fmt.Fprintln(cmd.OutOrStdout(), "{}")
			err = nil
		}
	}()
	out, decideErr := skillMatesOutput(cmd)
	if decideErr != nil {
		clidiag.Warn(skillMatesProg, "no skill-mates line: %v", decideErr)
		out = claude.PostToolUseOutput{}
	}
	if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(out); encErr != nil {
		clidiag.Warn(skillMatesProg, "failed to encode output: %v", encErr)
		fmt.Fprintln(cmd.OutOrStdout(), "{}")
	}
	return nil
}

// skillMatesOutput does the work and RETURNS its failure rather than warning
// itself, so a test can assert which reason fired without parsing stderr.
//
// The Skill check comes first, before any session, transcript or config is
// touched: for every other tool the answer is silence and there is nothing to
// fail at.
func skillMatesOutput(cmd *cobra.Command) (claude.PostToolUseOutput, error) {
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return claude.PostToolUseOutput{}, err
	}
	var payload claude.PostToolUsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return claude.PostToolUseOutput{}, fmt.Errorf("decode hook payload: %w", err)
	}
	if _, ok := claude.InvokedSkill(payload.ToolName, payload.ToolInput); !ok {
		return claude.PostToolUseOutput{}, nil
	}
	harp := os.Getenv(agent.SessionHarpEnv)
	if harp == "" {
		return claude.PostToolUseOutput{}, errors.New("no " + agent.SessionHarpEnv + " in the environment: there is no session whose transcript says what was invoked")
	}
	// The transcript is read through the ACTIVE engine's own adapter, as
	// next-step does: this verb is claude-code's today, but the reader is
	// selected by the session's recorded engine, never assumed.
	adapter, src, err := operations.ResolveTurnTranscript(cmd.Context(), harp, payload.TranscriptPath)
	if err != nil {
		return claude.PostToolUseOutput{}, err
	}
	evs, err := turnchange.ReadTranscript(cmd.Context(), adapter, src)
	if err != nil {
		return claude.PostToolUseOutput{}, err
	}
	// The delivered set is resolved the way the session's own assembly
	// resolved it -- the default agent's profiles over the project config the
	// hook process inherits -- so the mates named are skills the engine has.
	cfg, err := config.Load()
	if err != nil {
		return claude.PostToolUseOutput{}, fmt.Errorf("load project config: %w", err)
	}
	delivered := slices.DeleteFunc(backends.LoadSkillExports(cfg, cfg.DefaultAgentProfiles()),
		func(s *bundles.LoadedSkill) bool { return !claudeengine.SkillEnabled(s) })
	return buildSkillMatesOutput(payload, delivered, evs), nil
}

func init() {
	hookCmd.AddCommand(hookSkillMatesCmd)
}
