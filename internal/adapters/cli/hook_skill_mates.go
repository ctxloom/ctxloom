package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/turnchange"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// skillMatesProg names this hook on the clidiag warning channel.
const skillMatesProg = "ctxloom hook skill-mates"

var hookSkillMatesCmd = &cobra.Command{
	Use:    "skill-mates",
	Hidden: true, // Machine callback (post_tool hook, narrowed to the skill tool class) - not for direct use
	Short:  "Name a completed skill's link-group mates the session has not invoked",
	Long: `Reads a post_tool hook payload on stdin, through the codec of the engine
--engine names, and, when the completed tool call ran a skill that belongs to a
link group with members this session has not yet invoked, answers with one line
of context naming them.

A link group's skills deliver together, but the second skill of a consequent
pair has a condition that is false when the human speaks and turns true only
once the first has run -- which no listing text can say. This hook says it at
the one moment it is true.

"Not yet invoked" is read off the session's own transcript (its prior skill
tool calls), so nothing is persisted. The hook answers with nothing for any
other tool, for a skill in no group, and once every mate has been invoked.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookSkillMates,
}

// runHookSkillMates never reports a nonzero exit: a post_tool hook that fails
// interrupts the tool call it rode on, and a missed line costs one nudge, not
// a session. Every reason the line was not emitted is NAMED on the diagnostic
// channel instead, and the engine still gets its codec's empty answer.
func runHookSkillMates(cmd *cobra.Command, args []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "%s: panic: %v\n", skillMatesProg, r)
			err = nil
		}
	}()
	kind, err := firingEngine(cmd)
	if err != nil {
		clidiag.Warn(skillMatesProg, "no skill-mates line: %v", err)
		return nil
	}
	resp, decideErr := skillMatesOutput(cmd, kind)
	if decideErr != nil {
		clidiag.Warn(skillMatesProg, "no skill-mates line: %v", decideErr)
		resp = engine.HookResponse{}
	}
	if encErr := writeHookResponse(cmd, kind.Hooks(), wire.HookEventPostTool, resp); encErr != nil {
		clidiag.Warn(skillMatesProg, "failed to answer the hook: %v", encErr)
	}
	return nil
}

// skillMatesOutput does the work and RETURNS its failure rather than warning
// itself, so a test can assert which reason fired without parsing stderr.
//
// The skill check comes first, before any session, transcript or config is
// touched: for every other tool the answer is silence and there is nothing to
// fail at. The firing engine (kind) decides everything engine-specific: its
// codec reads the payload and the transcript's skill calls, and its name
// selects the exports the delivered set is read through.
func skillMatesOutput(cmd *cobra.Command, kind engine.Engine) (engine.HookResponse, error) {
	codec := kind.Hooks()
	ev, err := readHookEvent(cmd, codec, wire.HookEventPostTool)
	if err != nil {
		return engine.HookResponse{}, err
	}
	if ev.Skill == "" {
		return engine.HookResponse{}, nil
	}
	harp := os.Getenv(agent.SessionHarpEnv)
	if harp == "" {
		return engine.HookResponse{}, errors.New("no " + agent.SessionHarpEnv + " in the environment: there is no session whose transcript says what was invoked")
	}
	// The transcript is read through the ACTIVE engine's own adapter, as
	// next-step does: the reader is selected by the session's recorded
	// engine, never assumed.
	adapter, src, err := operations.ResolveTurnTranscript(cmd.Context(), afero.NewOsFs(), App().Engines(), harp, ev.Transcript)
	if err != nil {
		return engine.HookResponse{}, err
	}
	evs, err := turnchange.ReadTranscript(cmd.Context(), afero.NewOsFs(), adapter, src)
	if err != nil {
		return engine.HookResponse{}, err
	}
	delivered, err := deliveredEnabledSkills(cmd.Context(), string(kind.Root().Name))
	if err != nil {
		return engine.HookResponse{}, err
	}
	return skillMatesResponse(codec, ev, delivered, evs), nil
}

// deliveredEnabledSkills is the skill set the firing engine was delivered,
// resolved the way the session's own assembly resolved it -- the default
// agent's profiles over the project config the hook process inherits, read
// through that engine's exports -- so the mates named are skills the engine
// has.
func deliveredEnabledSkills(ctx context.Context, engineName string) ([]*bundles.LoadedSkill, error) {
	cfg, err := GetConfig()
	if err != nil {
		return nil, fmt.Errorf("load project config: %w", err)
	}
	pkg, err := operations.AssemblePackage(ctx, cfg, operations.PackageRequest{})
	if err != nil {
		return nil, fmt.Errorf("assemble the delivered set: %w", err)
	}
	exports, err := operations.ExportsFor(App().Engines(), pkg, engineName)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, s := range exports.Skills {
		enabled[s.Name] = s.Enabled
	}
	return slices.DeleteFunc(operations.LoadedSkills(pkg),
		func(s *bundles.LoadedSkill) bool { return !enabled[s.Frontmatter.Name] }), nil
}

func init() {
	hookCmd.AddCommand(hookSkillMatesCmd)
}
