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
	mapAgents      []string
	mapLLM         string
	mapConcurrency int
	mapSaveParts   string
	mapVerbosity   int
)

var mapCmd = &cobra.Command{
	Use:   "map [flags] [task...]",
	Short: "Run multiple agents/profiles in parallel over one task (fan-out)",
	Long: `Fan one shared task out to several members in parallel, emitting each
agent's output as a labeled block.

A member is either a local agent (--agents) or a bare profile (-p):
  --agents a,b   named agents, each run on ITS OWN engine + composed
                    profile-context (the engine binding is yours, set locally)
  -p prof1,prof2    bare profiles — sugar for a default-engine agent: each
                    runs with its profile's own llm: (unless --llm overrides all)
Members from both flags run together; -p needs no hand-written agent, so
fanning a set of review profiles works with zero local setup. Runs are bounded
by --concurrency and fault-tolerant: a failed member is reported inline and
never aborts the others.

This is the "map" half of the weave primitive. The labeled output is meant to
be read by a human, saved, or piped into a synthesizer:

  ctxloom map -p code-review/security -p code-review/performance "<diff>" \
    | ctxloom run -p code-review/synthesis --print

The task is taken from the arguments, or from stdin when no arguments are given
(so a diff or file set can be piped in).

Examples:
  ctxloom map -p reviewer/a -p reviewer/b "review this change"
  ctxloom map --agents go-cr-security,go-cr-correctness "review this change"
  git diff | ctxloom map -p code-review/security -p code-review/perf
  ctxloom map -p a -p b -p c --concurrency 3 --save-parts ./parts "task"`,
	RunE: runMap,
}

func runMap(cmd *cobra.Command, args []string) error {
	members := mapMembers()
	if len(members) == 0 {
		return fmt.Errorf("at least one member is required (--agents and/or -p/--profile)")
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
		Members:     members,
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

	// Surface failed members on stderr without failing the whole run — partial
	// success is success (CLAUDE.md).
	for _, p := range parts {
		if p.Failed() {
			clidiag.Warn("ctxloom", "member %q failed: %s", p.Profile, p.Err)
		}
	}

	return emit(cmd, operations.MapProfilesResult{Parts: parts}, func() error {
		fmt.Fprint(cmd.OutOrStdout(), operations.FormatParts(parts))
		return nil
	})
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

// mapMembers is the ordered member list for `map`: named agents first, then
// bare profiles. cobra can't recover the interleaving of two separate flags, so
// the order is deterministic (agents then profiles); a member resolves the
// same regardless of position.
func mapMembers() []string { return mergeMembers(mapAgents, mapProfiles) }

// mergeMembers concatenates the named-agent and bare-profile member lists into
// the single ordered member slice the operations layer fans across. Both kinds
// resolve through the same agent-or-bare-profile path; this only fixes their
// order (agents first).
func mergeMembers(subs, profiles []string) []string {
	members := make([]string, 0, len(subs)+len(profiles))
	members = append(members, subs...)
	members = append(members, profiles...)
	return members
}

func init() {
	rootCmd.AddCommand(mapCmd)

	mapCmd.Flags().StringSliceVarP(&mapProfiles, "profile", "p", nil, "Bare profile to run as a parallel member, default-engine sugar (repeatable)")
	mapCmd.Flags().StringSliceVar(&mapAgents, "agents", nil, "Named local agent(s) to run as members, each on its own engine (comma-separated/repeatable)")
	mapCmd.Flags().StringVarP(&mapLLM, "llm", "l", "", "Override the LLM for every member (else each member's own engine/llm:)")
	mapCmd.Flags().IntVar(&mapConcurrency, "concurrency", 0, "Max members to run at once (default 4)")
	mapCmd.Flags().CountVarP(&mapVerbosity, "verbose", "v", "Increase verbosity (repeatable)")
	mapCmd.Flags().StringVar(&mapSaveParts, "save-parts", "", "Directory to write each member's raw output into")

	_ = mapCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = mapCmd.RegisterFlagCompletionFunc("agents", completeAgentNames)
	_ = mapCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
}
