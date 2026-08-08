package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

var (
	materializeTarget   string
	materializeBackend  string
	materializeSurfaces []string
)

// profileMaterializeCmd writes a profile's ASSEMBLED, ready-to-run agent surface
// into a target dir (CLAUDE.md + .mcp.json + settings hooks + commands), so an
// externally-launched agent inherits it with ctxloom out of the loop. Distinct
// from `profile export`, which publishes the profile's YAML definition.
var profileMaterializeCmd = &cobra.Command{
	Use:   "materialize <profile>...",
	Short: "Write a profile's assembled context to a dir as a launchable agent surface",
	Long: `Materialize one or more profiles into --target as a backend's NATIVE on-disk
agent surface — CLAUDE.md (context) + .mcp.json (MCP) + .claude/settings.json
(hooks) + .claude/commands (commands) — so an externally-launched agent inherits
the profile with ctxloom out of the loop.

Each run OVERWRITES the target's ctxloom-managed surfaces (they are the source
of truth) while preserving foreign entries. Unlike 'ctxloom profile export'
(which publishes the profile YAML), this writes the assembled, ready-to-run
config a plain 'claude' / CI / human launch reads by default.

Examples:
  ctxloom profile materialize default --target ./out
  ctxloom profile materialize go-dev cr-correctness-go --target ../worktree`,
	Args: cobra.MinimumNArgs(1),
	RunE: runProfileMaterialize,
}

func runProfileMaterialize(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	// Fail-loudly choke owner (CLAUDE.md): checkpoint before materialize so every
	// fatal surface-write finding it records through strictness is caught here and
	// aborts the command (exit 3) unless --degraded downgrades them — mirroring
	// how `ctxloom run`/`mcp`/`acp` gate their own startup findings.
	overrides, err := parseSurfaceOverrides(materializeSurfaces)
	if err != nil {
		return err
	}
	mark := strictness.Checkpoint()
	res, err := operations.MaterializeProfile(cmd.Context(), cfg, operations.MaterializeProfileRequest{
		Profiles: args,
		Target:   materializeTarget,
		Backend:  materializeBackend,
		Surfaces: overrides,
	})
	if err != nil {
		return err
	}
	if ferr := failOnFindings(os.Stderr, mark); ferr != nil {
		return ferr
	}
	return emit(cmd, res, func() error {
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("Materialized %s → %s (%s)\n", strings.Join(res.Profiles, ", "), res.Target, res.Backend)
		for _, s := range res.Wrote {
			w.Printf("  wrote %s\n", s)
		}
		// The loss lines sit with the wrote lines, not in a separate pass at the
		// end: they are the same report. A reader who scans only the top of the
		// output must not come away with "wrote four things" as the whole story
		// (whiny-exclusive).
		for _, loss := range res.NotCarried {
			w.Printf("  NOT carried: %s\n", loss)
		}
		for _, warn := range res.Warnings {
			w.Printf("  warning: %s\n", warn)
		}
		return w.Err()
	})
}

func init() {
	profileCmd.AddCommand(profileMaterializeCmd)
	profileMaterializeCmd.Flags().StringVar(&materializeTarget, "target", "", "Target directory to write the agent surface into (required)")
	profileMaterializeCmd.Flags().StringVar(&materializeBackend, "backend", operations.DefaultMaterializeBackend, "Backend surface to write (claude-code)")
	profileMaterializeCmd.Flags().StringArrayVar(&materializeSurfaces, "surface", nil,
		"Override where a surface is delivered: <kind>=<approach> (repeatable). See --help for what this project's engines support.")
	_ = profileMaterializeCmd.MarkFlagRequired("target")
	profileMaterializeCmd.ValidArgsFunction = completeProfileNames
	_ = profileMaterializeCmd.RegisterFlagCompletionFunc("surface", completeSurfaceOverrides)

	// Help is computed against THIS project's engines, not written down. The
	// flag's vocabulary is ctxloom's own and varies per engine, so a static
	// table would be both a second source and the wrong one for most readers.
	defaultHelp := profileMaterializeCmd.HelpFunc()
	profileMaterializeCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		defaultHelp(c, args)
		cfg, err := GetConfig()
		if err != nil {
			// No config is a normal state (help outside a project), not a
			// failure worth interrupting help for.
			return
		}
		fmt.Fprint(c.OutOrStdout(), surfaceHelpFor(configuredEngines(cfg)))
	})
}
