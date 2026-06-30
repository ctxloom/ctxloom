package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var (
	mapProfiles    []string
	mapLLM         string
	mapConcurrency int
	mapSaveParts   string
	mapVerbosity   int
)

var mapCmd = &cobra.Command{
	Use:   "map [flags] [task...]",
	Short: "Run multiple profiles in parallel over one task (fan-out)",
	Long: `Fan one shared task out to several profiles in parallel, emitting each
agent's output as a labeled block.

Each -p profile is run as its own oneshot agent carrying its own context and
LLM (its profile's llm:, unless --llm overrides all). Runs are bounded by
--concurrency and fault-tolerant: a failed member is reported inline and never
aborts the others.

This is the "map" half of the weave primitive. The labeled output is meant to
be read by a human, saved, or piped into a synthesizer:

  ctxloom map -p code-review/security -p code-review/performance "<diff>" \
    | ctxloom run -p code-review/synthesis --print

The task is taken from the arguments, or from stdin when no arguments are given
(so a diff or file set can be piped in).

Examples:
  ctxloom map -p reviewer/a -p reviewer/b "review this change"
  git diff | ctxloom map -p code-review/security -p code-review/perf
  ctxloom map -p a -p b -p c --concurrency 3 --save-parts ./parts "task"`,
	RunE: runMap,
}

func runMap(cmd *cobra.Command, args []string) error {
	if len(mapProfiles) == 0 {
		return fmt.Errorf("at least one profile is required (-p/--profile)")
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	task := strings.Join(args, " ")
	if task == "" && stdinIsPiped() {
		if data, rerr := io.ReadAll(os.Stdin); rerr == nil {
			task = strings.TrimSpace(string(data))
		}
	}

	parts := operations.MapProfiles(cmd.Context(), cfg, operations.MapProfilesRequest{
		Profiles:    mapProfiles,
		Task:        task,
		LLM:         mapLLM,
		WorkDir:     projectroot.WorkDir(),
		Concurrency: mapConcurrency,
		Verbosity:   mapVerbosity,
	})

	if mapSaveParts != "" {
		if err := saveParts(mapSaveParts, parts); err != nil {
			clidiag.Warn("ctxloom", "saving parts failed: %v", err)
		}
	}

	fmt.Fprint(cmd.OutOrStdout(), operations.FormatParts(parts))

	// Surface failed members on stderr without failing the whole run — partial
	// success is success (CLAUDE.md).
	for _, p := range parts {
		if p.Failed() {
			clidiag.Warn("ctxloom", "member %q failed: %s", p.Profile, p.Err)
		}
	}
	return nil
}

// saveParts writes each member's output to <dir>/<sanitized-profile>.txt for
// later inspection or hand-editing before synthesis.
func saveParts(dir string, parts []operations.Part) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range parts {
		name := strings.ReplaceAll(p.Profile, "/", "_") + ".txt"
		body := p.Output
		if p.Failed() {
			body = "[error: " + p.Err + "]"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(mapCmd)

	mapCmd.Flags().StringSliceVarP(&mapProfiles, "profile", "p", nil, "Profile to run as a parallel member (repeatable)")
	mapCmd.Flags().StringVarP(&mapLLM, "llm", "l", "", "Override the LLM for every member (else each profile's own llm:)")
	mapCmd.Flags().IntVar(&mapConcurrency, "concurrency", 0, "Max members to run at once (default 4)")
	mapCmd.Flags().CountVarP(&mapVerbosity, "verbose", "v", "Increase verbosity (repeatable)")
	mapCmd.Flags().StringVar(&mapSaveParts, "save-parts", "", "Directory to write each member's raw output into")

	_ = mapCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = mapCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
}
