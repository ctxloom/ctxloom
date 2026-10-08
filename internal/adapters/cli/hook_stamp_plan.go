package cli

import (
	"os"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// stampPlanCmd reads a post_file_edit hook payload on stdin, through the
// codec of the engine --engine names (engine.HookEvent.Path is the edited
// file), and, when the edited file
// matches the plan-file pattern, stamps the active session's harp name
// into the file's YAML frontmatter. No-op when CTXLOOM_SESSION_HARP is
// unset or the edited file isn't a plan file.
var stampPlanCmd = &cobra.Command{
	Use:    "stamp-plan",
	Short:  "Stamp the active session's harp name into a plan file's frontmatter (internal — used by the PostFileEdit hook)",
	Hidden: true,
	RunE:   runStampPlan,
}

func runStampPlan(cmd *cobra.Command, args []string) error {
	harp := os.Getenv(sessions.EnvHarp)
	if harp == "" {
		// No active session — silent no-op so the hook is safe to
		// install before Phase 3's session naming ships.
		return nil
	}
	// Machine hook: never fail the host agent's tool call over a stamping
	// hiccup. Every reason nothing was stamped is reported on the
	// warn-and-continue channel rather than vanishing.
	kind, err := firingEngine(cmd)
	if err != nil {
		clidiag.Warn("ctxloom", "stamp-plan: %v", err)
		return nil
	}
	ev, err := readHookEvent(cmd, kind.Hooks(), wire.HookEventPostFileEdit)
	if err != nil {
		// A payload the firing engine's codec cannot decode is a contract
		// break with that engine. A payload that decodes but names no file
		// (a non-edit tool call) is an ordinary event and stays silent, so
		// this never becomes per-tool-call noise.
		clidiag.Warn("ctxloom", "stamp-plan: %v", err)
		return nil
	}
	if ev.Path == "" || !memory.IsPlanFile(ev.Path) {
		return nil
	}
	if err := memory.StampPlanFile(afero.NewOsFs(), ev.Path, harp); err != nil {
		clidiag.Warn("ctxloom", "stamp-plan: %v", err)
	}
	return nil
}

func init() {
	// stamp-plan is a machine callback (PostFileEdit hook target), so it lives
	// under the hidden `hook` namespace.
	hookCmd.AddCommand(stampPlanCmd)
}
