package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

var (
	weaveProfiles    []string
	weaveWorkspace   string
	weaveAgents      []string
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
	Short: "Fan a task across agents/profiles in parallel, then synthesize the results",
	Long: `Experimental — interfaces and behavior may change.

Run several members in parallel over one shared task (map), then pipe their
outputs into a high-power synthesis profile that combines them into a single
result (reduce).

A member is either a local agent (--agents) or a bare profile (-p):
  --agents a,b   named agents, each run on ITS OWN engine + composed
                    profile-context (the engine binding is yours, set locally)
  -p prof1,prof2    bare profiles — sugar for a default-engine agent: each
                    runs with its profile's own llm: (unless --llm overrides all)
Members from both flags run together. The -s synthesizer runs on its own
(typically high-power) llm:. The whole map→reduce runs in-process — no shell —
so it works identically on every platform.

This is the composite of the exposed components: it is equivalent to
  ctxloom weave -p A -p B --map-only "task" | ctxloom run -p SYNTH --print
but portable and single-invocation. Use the components directly when you want to
inspect or post-process the intermediate parts.

Inject non-ctxloom outputs as additional parts to synthesize alongside (or
instead of) live members:
  --part NAME=FILE     include FILE's contents as a labeled part (repeatable)
  --parts-from DIR     include every file in DIR as a part (named by filename)

--map-only is an alias for --no-synthesize: fan out and emit the labeled parts,
skip the reduce step.

The task is taken from the arguments, or from stdin when no arguments are given.

Examples:
  ctxloom weave -p code-review/security -p code-review/perf \
    -s code-review/synthesis "review this diff"
  ctxloom weave --agents go-cr-security,go-cr-correctness -s synthesis "review"
  git diff | ctxloom weave -p reviewer/a -p reviewer/b -s synthesis
  ctxloom weave -p a -p b -s synth --part legacy=old-report.txt "audit"
  ctxloom weave -s synth --parts-from ./collected "merge these findings"
  ctxloom weave -p a -p b --map-only "just fan out, no reduce"`,
	RunE: runWeave,
}

func runWeave(cmd *cobra.Command, args []string) error {
	members := mergeMembers(weaveAgents, weaveProfiles)
	if len(members) == 0 && weavePartsFrom == "" && len(weaveParts) == 0 {
		return fmt.Errorf("nothing to weave: pass members (--agents/-p) and/or injected parts (--part/--parts-from)")
	}
	if !weaveNoSynth && weaveSynthesize == "" {
		return fmt.Errorf("a synthesis profile is required (-s/--synthesize); or pass --no-synthesize to emit parts only")
	}

	// Fail-loudly choke owner (CLAUDE.md): weave LAUNCHES engine processes, so
	// it owns a startup gate exactly like `ctxloom run`/`mcp`/`acp` and
	// `profile materialize`. The checkpoint precedes the config load because
	// that load is what RECORDS the fatal-class findings (printAndRecordConfigWarnings)
	// the gate below reads.
	startupMark := strictness.Checkpoint()

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	task, err := readTask(args, os.Stdin, stdinIsPiped())
	if err != nil {
		return err
	}

	injected, err := collectInjectedParts(weavePartsFrom, weaveParts)
	if err != nil {
		return err
	}
	// The guard that matters is on the RESOLVED inputs, not on the flags —
	// see checkWeaveInputs.
	if err := checkWeaveInputs(members, injected, task); err != nil {
		return err
	}

	// Abort BEFORE fanning members out: a config broken badly enough that
	// `ctxloom run` refuses to start must not instead be handed to N parallel
	// agents on whatever partial context survived it. --degraded (env
	// CTXLOOM_DEGRADED=1) is the escape hatch, same as every other gate.
	if ferr := failOnFindings(os.Stderr, startupMark); ferr != nil {
		return ferr
	}

	result, weaveErr := operations.Weave(cmd.Context(), cfg, operations.WeaveRequest{
		Workspace:     weaveWorkspace,
		Members:       members,
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
			clidiag.Warn("ctxloom", "saving parts failed: %v", err)
		}
	}

	// Surface failed members without failing the whole run (partial success is
	// success — CLAUDE.md).
	if result != nil {
		for _, p := range result.Parts {
			if p.Failed() {
				clidiag.Warn("ctxloom", "member %q failed: %s", p.Profile, p.Err)
			}
		}
	}

	// Synthesis failure: warn but still surface the parts (partial success is
	// success — CLAUDE.md). --format json emits the full WeaveResult; text falls
	// back to the labeled parts when there's no report.
	if weaveErr != nil {
		clidiag.Warn("ctxloom", "%v; emitting parts instead", weaveErr)
	}
	if result == nil {
		return nil
	}
	text, fallback := weaveText(result, weaveErr, weaveNoSynth)
	if fallback != "" {
		clidiag.Warn("ctxloom", "%s; emitting parts instead", fallback)
	}
	// The output floor (U043-F02): a run that produced neither a synthesis nor
	// a single part has nothing to say, and printing a blank line and exiting 0
	// says it succeeded. Checked BEFORE emit so --format json fails too.
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("weave produced no output: no synthesis report and no member parts")
	}
	return emit(cmd, result, func() error {
		fmt.Fprint(cmd.OutOrStdout(), text)
		return nil
	})
}

// readTask resolves the shared task: the arguments, or stdin when there are
// none and stdin is piped.
//
// A stdin READ FAILURE is returned, not swallowed (U043-F08). The old form was
// `if data, rerr := io.ReadAll(os.Stdin); rerr == nil { task = ... }` with no
// else — a broken pipe mid-read left task empty and weave fanned every member
// out over nothing, having been told the task was simply blank.
func readTask(args []string, stdin io.Reader, piped bool) (string, error) {
	if task := strings.TrimSpace(strings.Join(args, " ")); task != "" {
		return task, nil
	}
	if !piped {
		return "", nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read task from stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// checkWeaveInputs rejects a run that has nothing to work with, gating on the
// RESOLVED inputs rather than on the flags that were passed (U043-F03): the old
// guard accepted any non-empty `--parts-from`, so a directory that was empty —
// or held only subdirectories, which the scan skips — resolved to zero parts and
// went on to "synthesize" them.
//
// An empty task is rejected only when there are MEMBERS to fan out (U043-F08):
// running engines over an empty prompt is never what the user meant, while a
// parts-only run legitimately has no task (the synthesis prompt just omits that
// section).
func checkWeaveInputs(members []string, injected []operations.Part, task string) error {
	if len(members) == 0 && len(injected) == 0 {
		return fmt.Errorf("nothing to weave: no members (--agents/-p) and no parts resolved from --part/--parts-from")
	}
	if len(members) > 0 && strings.TrimSpace(task) == "" {
		return fmt.Errorf("no task to weave: pass it as arguments or on stdin (members would otherwise run on an empty prompt)")
	}
	return nil
}

// weaveText picks what the run emits and, when that is not the synthesis the
// user asked for, why. An empty report is a synthesis that did not happen
// (U043-F02): it used to print as one blank line with exit 0 — the
// characteristic bug — and now falls back to the labeled parts exactly as a
// synthesis ERROR does, with a warning. The caller enforces the floor: if this
// returns nothing, there is nothing to print and the run failed.
func weaveText(result *operations.WeaveResult, weaveErr error, noSynth bool) (out, fallbackReason string) {
	if noSynth {
		return operations.FormatParts(result.Parts), ""
	}
	if weaveErr != nil {
		return operations.FormatParts(result.Parts), "" // the caller already warned with the error itself
	}
	if strings.TrimSpace(result.Report) == "" {
		return operations.FormatParts(result.Parts), "synthesis produced no output"
	}
	return result.Report + "\n", ""
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

// saveParts writes each member's output to <dir>/<sanitized-profile>.txt for
// later inspection or hand-editing before synthesis.
//
// Two silent no-ops closed here (U043-F05). Zero parts used to create the
// directory and return nil — the user passed --save-parts and got an empty
// directory reported as success. And the filename was the profile with `/`
// replaced by `_`, with no uniqueness check: `-p a/b -p a_b`, or an agent and a
// profile of the same name (mergeMembers does not dedup), collapsed onto one
// file and one member's entire output was lost.
func saveParts(dir string, parts []operations.Part) error {
	if len(parts) == 0 {
		return fmt.Errorf("--save-parts %s: nothing to save (the run produced no parts)", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	taken := make(map[string]bool, len(parts))
	for _, p := range parts {
		stem := strings.ReplaceAll(p.Profile, "/", "_")
		name := stem
		// Probe rather than count, so a member literally named `a-2` cannot be
		// clobbered by the disambiguation of two members named `a`.
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s-%d", stem, i)
		}
		taken[name] = true
		body := p.Output
		if p.Failed() {
			body = "[error: " + p.Err + "]"
		}
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(body+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(weaveCmd)

	weaveCmd.Flags().StringSliceVarP(&weaveProfiles, "profile", "p", nil, "Bare profile member, default-engine sugar (repeatable)")
	weaveCmd.Flags().StringSliceVar(&weaveAgents, "agents", nil, "Named local agent member(s), each on its own engine (comma-separated/repeatable)")
	weaveCmd.Flags().StringVarP(&weaveSynthesize, "synthesize", "s", "", "Synthesis profile that combines member outputs (high-power)")
	weaveCmd.Flags().StringVarP(&weaveLLM, "llm", "l", "", "Override the LLM for every member (synthesizer keeps its own llm:)")
	weaveCmd.Flags().IntVar(&weaveConcurrency, "concurrency", 0, "Max members to run at once (default 4)")
	weaveCmd.Flags().StringVar(&weaveWorkspace, "workspace", "", "Session workspace axis for every member (none|worktree; empty = project default)")
	weaveCmd.Flags().StringVar(&weaveSaveParts, "save-parts", "", "Directory to write each member's raw output into")
	weaveCmd.Flags().StringVar(&weavePartsFrom, "parts-from", "", "Inject every file in this directory as a part to synthesize")
	weaveCmd.Flags().StringArrayVar(&weaveParts, "part", nil, "Inject NAME=FILE as a part to synthesize (repeatable)")
	weaveCmd.Flags().BoolVar(&weaveNoSynth, "no-synthesize", false, "Emit the labeled parts only; skip synthesis (alias: --map-only)")
	// --map-only is a SPELLING of --no-synthesize, normalized onto it rather
	// than registered as a second BoolVar over the same pointer. Two flags
	// sharing one variable means registration order decides the default, only
	// the typed spelling is marked Changed, and the alias shows up in --help
	// and in shell completion as if it were a separate feature.
	weaveCmd.Flags().SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "map-only" {
			name = "no-synthesize"
		}
		return pflag.NormalizedName(name)
	})
	weaveCmd.Flags().CountVarP(&weaveVerbosity, "verbose", "v", "Increase verbosity (repeatable)")

	_ = weaveCmd.RegisterFlagCompletionFunc("profile", completeProfileNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("agents", completeAgentNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("synthesize", completeProfileNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("llm", completeLLMNames)
	_ = weaveCmd.RegisterFlagCompletionFunc("workspace", completeWorkspaceNames)
}
