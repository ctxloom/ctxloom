package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// materializeFlags is one invocation's flags.
type materializeFlags struct {
	target   string
	backends []string
	surfaces []string
	dryRun   bool
	yes      bool
	force    bool
	release  bool
	diff     string
}

var matFlags materializeFlags

// materializeCmd is THE at-rest delivery: the profiles' assembled surfaces
// written into a directory as each engine's native files, so a launch with
// ctxloom out of the loop inherits them.
var materializeCmd = &cobra.Command{
	Use:   "materialize [<profile>...]",
	Short: "Write the assembled profiles into a directory as each engine's native files",
	Long: `Write the assembled profiles into --target as each engine's NATIVE files —
its context file, MCP registry, settings and hooks, command and skill
directories — so an agent launched there with ctxloom OUT of the loop
inherits them. With no profiles, the default agent's are written.

--target takes any directory, absolute or relative to the working
directory; a missing one is created. It need not be a ctxloom project or a
git repository: config, the default agent and profiles always come from the
project you run this in.

With no --target the command would write the PROJECT directory itself,
shared by every session and person using this checkout. It refuses to unless
--yes is given; --dry-run shows what it would do and writes nothing. --force
is a separate override, for a target that is an engine's user-global scope
(running from $HOME); --yes never implies it.

--backend (repeatable) names the engines; by default the project's
configured engines. --surface KIND[=file][:DEST] (repeatable) delivers only
the named kinds (context, mcp, settings, hooks, commands, skills) and leaves
the rest untouched; context=file:PATH writes the context into PATH inside
the target instead of the engine's own file.

--release takes out what an earlier materialize wrote (the selected kinds;
default all), leaving everything else. --diff FILE compares the context as it
would be written against FILE, writing nothing.

ctxloom's own session MCP endpoint is never written: it exists only inside a
'ctxloom run' session. A materialized file is not meant to be committed: the
record of what ctxloom wrote lives in your home directory, so on another
machine a committed copy reads as your own text and gets a second copy
appended.`,
	Example: `  ctxloom materialize --target ./out
  ctxloom materialize go-dev --target ../worktree --backend claude-code
  ctxloom materialize --target ./out --surface context=file:docs/AGENTS.md
  ctxloom materialize --target ./out --release --surface skills
  ctxloom materialize --dry-run
  ctxloom materialize --yes
  ctxloom materialize default --diff ../bob-checkout/CLAUDE.md`,
	RunE: runMaterialize,
}

func runMaterialize(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	specs, err := operations.ParseSurfaceSpecs(matFlags.surfaces)
	if err != nil {
		return err
	}
	if err := checkMaterializeFlags(args, specs); err != nil {
		return err
	}
	if matFlags.diff != "" {
		return runMaterializeDiff(afero.NewOsFs(), cmd, cfg, args, specs)
	}
	req := operations.MaterializeRequest{
		Profiles: profilesOrNil(args), Target: matFlags.target, Explicit: matFlags.target != "",
		Engines: matFlags.backends, Surfaces: specs, Release: matFlags.release, DryRun: matFlags.dryRun, Force: matFlags.force,
	}
	if !req.Explicit {
		req.Target = projectroot.WorkDir()
		if err := guardProjectTarget(cmd.ErrOrStderr(), cfg, req, specs); err != nil {
			return err
		}
	}
	gates := newPhaseGates(os.Stderr, App().Strictness)
	res, err := operations.Materialize(cmd.Context(), App().Engines(), cfg, req)
	if err != nil {
		return err
	}
	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}
	if err := emit(cmd, res, func() error { return renderMaterialize(cmd.OutOrStdout(), res) }); err != nil {
		return err
	}
	return clidiag.WarnErrors("ctxloom", res.Errors)
}

// profilesOrNil is the positional profiles, nil when none were named (the
// default agent's).
func profilesOrNil(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return args
}

// checkMaterializeFlags refuses the flag combinations the command line can
// see are incoherent before anything is resolved.
func checkMaterializeFlags(args []string, specs []operations.SurfaceSpec) error {
	if matFlags.release && (len(args) > 0 || matFlags.diff != "") {
		return fmt.Errorf("--release takes no profiles and no --diff: it removes what an earlier materialize wrote")
	}
	if matFlags.diff == "" {
		return nil
	}
	if len(matFlags.backends) > 1 {
		return fmt.Errorf("--diff compares one engine's context; name at most one --backend")
	}
	if len(specs) > 0 && !slices.ContainsFunc(specs, func(s operations.SurfaceSpec) bool { return s.Kind == present.Context }) {
		return fmt.Errorf("--diff compares the context; a --surface list without context leaves nothing to compare")
	}
	return nil
}

// guardProjectTarget is the project-directory guard (R14): with no --target
// the target is the project directory, and writing (or releasing) there needs
// --yes. A dry run is not gated but says the same. Never interactive.
func guardProjectTarget(w io.Writer, cfg *config.Config, req operations.MaterializeRequest, specs []operations.SurfaceSpec) error {
	if matFlags.yes && !matFlags.dryRun {
		return nil
	}
	engines, err := operations.MaterializeEngines(App().Engines(), cfg, req)
	if err != nil {
		return err
	}
	kinds := delivery.AllKinds()
	if len(specs) > 0 {
		kinds = nil
		for _, s := range specs {
			kinds = append(kinds, s.Kind)
		}
	}
	verb := "write"
	if req.Release {
		verb = "release"
	}
	var names []string
	for _, k := range kinds {
		names = append(names, k.String())
	}
	fmt.Fprintf(w, "WARNING: no --target given, so this would %s the PROJECT directory %s\n", verb, req.Target)
	fmt.Fprintf(w, "  engines: %s\n  kinds:   %s\n", strings.Join(engines, ", "), strings.Join(names, ", "))
	fmt.Fprintf(w, "  It is shared by every session and person using this checkout. Pass --target DIR, or --yes to %s it.\n", verb)
	if matFlags.dryRun {
		return nil
	}
	return operations.ErrProjectTargetUnconfirmed
}

// renderMaterialize is the text report: one block per engine — what was
// written, released and not carried, the premise withholds, and what the
// writers skipped.
func renderMaterialize(out io.Writer, res *operations.MaterializeResult) error {
	w := errwriter.New(out)
	verb := map[string]string{operations.MaterializePlanned: "Would materialize", operations.MaterializeReleased: "Released"}[res.Status]
	if verb == "" {
		verb = "Materialized"
	}
	w.Printf("%s → %s (%s)\n", verb, res.Target, res.Status)
	if res.Created {
		w.Printf("  created %s\n", res.Target)
	}
	if len(res.Profiles) > 0 {
		w.Printf("  profiles: %s\n", strings.Join(res.Profiles, ", "))
	}
	for _, e := range res.Engines {
		w.Printf("%s\n", e.Engine)
		printEach(w, "  wrote %s\n", e.Wrote)
		printEach(w, "  released %s\n", e.Released)
		if e.ContextFile != "" {
			w.Printf("  context file %s\n", e.ContextFile)
		}
		for _, loss := range e.NotCarried {
			w.Printf("  NOT carried: %s\n", loss)
		}
		for _, p := range e.WithheldByPremise {
			w.Printf("  withheld by premise: %s (%s)%s\n", p.Name, p.Premise, map[bool]string{true: " → " + p.Delivered, false: ""}[p.Delivered != ""])
		}
		printEach(w, "  skipped: %s\n", e.Skipped)
		printEach(w, "  warning: %s\n", e.Warnings)
	}
	printEach(w, "warning: %s\n", res.Warnings)
	return w.Err()
}

func printEach(w *errwriter.Writer, format string, items []string) {
	for _, s := range items {
		w.Printf(format, s)
	}
}

// runMaterializeDiff compares the context as materialize would write it for
// one engine — through the same consumer the write uses, so a premised
// fragment is inlined exactly when the write inlines it — against an
// already-delivered file. Read-only.
func runMaterializeDiff(fsys afero.Fs, cmd *cobra.Command, cfg *config.Config, args []string, specs []operations.SurfaceSpec) error {
	engines, err := operations.MaterializeEngines(App().Engines(), cfg, operations.MaterializeRequest{Engines: matFlags.backends, Explicit: true})
	if err != nil {
		return err
	}
	if len(engines) != 1 {
		return fmt.Errorf("--diff compares one engine's context; %d are configured (%s): name one with --backend", len(engines), strings.Join(engines, ", "))
	}
	consumer := operations.MaterializedFor(App().Engines(), engines[0])
	if len(specs) > 0 && !slices.ContainsFunc(specs, func(s operations.SurfaceSpec) bool { return s.Kind == present.Skills }) {
		consumer = consumer.WithoutSkills()
	}
	asm, err := operations.AssembleContext(cmd.Context(), cfg, operations.AssembleContextRequest{Profiles: args, Consumer: consumer})
	if err != nil {
		return fmt.Errorf("assemble context for %v: %w", args, err)
	}
	theirs, err := afero.ReadFile(fsys, matFlags.diff)
	if err != nil {
		return fmt.Errorf("read %s to compare against: %w", matFlags.diff, err)
	}
	label := strings.Join(args, "+")
	if label == "" {
		label = "the default agent"
	}
	// The section writer ends the file with a newline the assembled text may
	// lack: a trailing newline is not a difference in what was delivered.
	mine, delivered := strings.TrimRight(asm.Context, "\n")+"\n", strings.TrimRight(string(theirs), "\n")+"\n"
	diffText := ""
	if mine != delivered {
		diffText, err = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: difflib.SplitLines(delivered), B: difflib.SplitLines(mine),
			FromFile: matFlags.diff, ToFile: label + " (materialized here)", Context: 3,
		})
		if err != nil {
			return fmt.Errorf("diff %s against %s's materialized context: %w", matFlags.diff, label, err)
		}
	}
	result := profileMaterializeDiffJSON{Profiles: args, ComparedTo: matFlags.diff, Identical: diffText == "", Diff: diffText}
	return emit(cmd, result, func() error { return renderMaterializeDiff(cmd.OutOrStdout(), label, result) })
}

func init() {
	rootCmd.AddCommand(materializeCmd)
	f := materializeCmd.Flags()
	f.StringVar(&matFlags.target, "target", "", "Directory to write into: any path, created if missing (empty = the project directory, which needs --yes)")
	f.StringArrayVar(&matFlags.backends, "backend", nil, "Engine to write for (repeatable; empty = the configured engines)")
	f.StringArrayVar(&matFlags.surfaces, "surface", nil, "KIND[=file][:DEST]: deliver only this kind (repeatable); DEST names the context file inside the target")
	f.BoolVar(&matFlags.dryRun, "dry-run", false, "Show what would be written, and write nothing")
	f.BoolVar(&matFlags.yes, "yes", false, "Confirm writing the PROJECT directory when no --target is given")
	f.BoolVar(&matFlags.force, "force", false, "Proceed even when the target is an engine's user-global scope (e.g. $HOME)")
	f.BoolVar(&matFlags.release, "release", false, "Remove what an earlier materialize wrote (the selected kinds; default all)")
	f.StringVar(&matFlags.diff, "diff", "", "Compare the context as it would be written against this file, writing nothing")
	materializeCmd.ValidArgsFunction = completeProfileNames
}
