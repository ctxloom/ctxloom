package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
)

var (
	weaveMembers     []string
	weaveSynthesize  string
	weaveLLM         string
	weaveConcurrency int
	weaveSaveParts   string
	weavePartsFrom   string
	weaveParts       []string
	weaveNoSynth     bool
	weaveVerbosity   int
)

var weaveCmd = &cobra.Command{
	Use:   "weave [flags] [task...]",
	Short: "Fan a task across profiles in parallel, then synthesize the results",
	Long: `Run several profiles in parallel over one shared task (map), then pipe their
outputs into a high-power synthesis profile that combines them into a single
result (reduce).

Each -p member runs as its own oneshot agent with its own context and LLM; the
-s synthesizer runs on its own (typically high-power) llm:. The whole map→reduce
runs in-process — no shell — so it works identically on every platform.

This is the composite of the exposed components: it is equivalent to
  ctxloom map -p A -p B "task" | ctxloom run -p SYNTH --print
but portable and single-invocation. Use the components directly when you want to
inspect or post-process the intermediate parts.

Inject non-ctxloom outputs as additional parts to synthesize alongside (or
instead of) live members:
  --part NAME=FILE     include FILE's contents as a labeled part (repeatable)
  --parts-from DIR     include every file in DIR as a part (named by filename)

The task is taken from the arguments, or from stdin when no arguments are given.

Examples:
  ctxloom weave -p code-review/security -p code-review/perf \
    -s code-review/synthesis "review this diff"
  git diff | ctxloom weave -p reviewer/a -p reviewer/b -s synthesis
  ctxloom weave -p a -p b -s synth --part legacy=old-report.txt "audit"
  ctxloom weave -s synth --parts-from ./collected "merge these findings"
  ctxloom weave -p a -p b --no-synthesize "just fan out, no reduce"`,
	RunE: runWeave,
}

func runWeave(cmd *cobra.Command, args []string) error {
	if len(weaveMembers) == 0 && weavePartsFrom == "" && len(weaveParts) == 0 {
		return fmt.Errorf("nothing to weave: pass members (-p) and/or injected parts (--part/--parts-from)")
	}
	if !weaveNoSynth && weaveSynthesize == "" {
		return fmt.Errorf("a synthesis profile is required (-s/--synthesize); or pass --no-synthesize to emit parts only")
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

	injected, err := collectInjectedParts(weavePartsFrom, weaveParts)
	if err != nil {
		return err
	}

	result, weaveErr := operations.Weave(cmd.Context(), cfg, operations.WeaveRequest{
		Members:       weaveMembers,
		Synthesize:    weaveSynthesize,
		Task:          task,
		LLM:           weaveLLM,
		InjectedParts: injected,
		WorkDir:       projectroot.WorkDir(),
		Concurrency:   weaveConcurrency,
		NoSynthesize:  weaveNoSynth,
		Verbosity:     weaveVerbosity,
	})

	if weaveSaveParts != "" && result != nil {
		if err := saveParts(weaveSaveParts, result.Parts); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: saving parts failed: %v\n", err)
		}
	}

	// Surface failed members without failing the whole run (partial success is
	// success — CLAUDE.md).
	if result != nil {
		for _, p := range result.Parts {
			if p.Failed() {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: member %q failed: %s\n", p.Profile, p.Err)
			}
		}
	}

	// Synthesis failure: warn and fall back to the labeled parts so the work
	// isn't lost.
	if weaveErr != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %v; emitting parts instead\n", weaveErr)
		if result != nil {
			fmt.Fprint(cmd.OutOrStdout(), operations.FormatParts(result.Parts))
		}
		return nil
	}

	if weaveNoSynth {
		fmt.Fprint(cmd.OutOrStdout(), operations.FormatParts(result.Parts))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), result.Report)
	return nil
}

// collectInjectedParts reads --parts-from <dir> (each file → a part named by its
// base filename without extension) and --part NAME=FILE entries into parts that
// weave synthesizes alongside live members.
func collectInjectedParts(dir string, named []string) ([]operations.Part, error) {
	var parts []operations.Part

	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("--parts-from %q: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("--parts-from: read %q: %w", path, err)
			}
			name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			parts = append(parts, operations.Part{Profile: name, Output: strings.TrimSpace(string(data))})
		}
	}

	for _, spec := range named {
		name, file, ok := strings.Cut(spec, "=")
		if !ok || name == "" || file == "" {
			return nil, fmt.Errorf("--part must be NAME=FILE, got %q", spec)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("--part %q: %w", spec, err)
		}
		parts = append(parts, operations.Part{Profile: name, Output: strings.TrimSpace(string(data))})
	}

	return parts, nil
}

func init() {
	rootCmd.AddCommand(weaveCmd)

	weaveCmd.Flags().StringSliceVarP(&weaveMembers, "profile", "p", nil, "Member profile to run in parallel (repeatable)")
	weaveCmd.Flags().StringVarP(&weaveSynthesize, "synthesize", "s", "", "Synthesis profile that combines member outputs (high-power)")
	weaveCmd.Flags().StringVarP(&weaveLLM, "llm", "l", "", "Override the LLM for every member (synthesizer keeps its own llm:)")
	weaveCmd.Flags().IntVar(&weaveConcurrency, "concurrency", 0, "Max members to run at once (default 4)")
	weaveCmd.Flags().StringVar(&weaveSaveParts, "save-parts", "", "Directory to write each member's raw output into")
	weaveCmd.Flags().StringVar(&weavePartsFrom, "parts-from", "", "Inject every file in this directory as a part to synthesize")
	weaveCmd.Flags().StringArrayVar(&weaveParts, "part", nil, "Inject NAME=FILE as a part to synthesize (repeatable)")
	weaveCmd.Flags().BoolVar(&weaveNoSynth, "no-synthesize", false, "Emit the labeled parts only; skip synthesis")
	weaveCmd.Flags().CountVarP(&weaveVerbosity, "verbose", "v", "Increase verbosity (repeatable)")

	_ = weaveCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("synthesize", completeProfileNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
}
