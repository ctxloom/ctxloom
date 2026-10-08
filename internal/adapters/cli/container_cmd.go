package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/resources"
)

var containerCmd = groupNode(&cobra.Command{
	Use:   "container",
	Short: "Manage agent container images",
	Long: `Manage the per-backend agent images a containerized engine runs in
(agents with 'runtime: container', or the project 'runtime:' default).`,
})

var (
	containerBuildOverlayImage        string
	containerBuildBase                string
	containerBuildDevcontainerService string
	containerBuildEngines             []string
	containerBuildRuntime             string
	containerBuildKeepCache           bool
)

// containerBuildCmd builds in two stages: a shared base (the distro plus the
// coding-agent tool layer) and the engine's agent stage (the client CLI, from
// that engine's own official installer as its own cacheable layer, plus the
// running ctxloom binary), so a rebuilt image never needs a ctxloom release.
// The client is never pinned: the install fetches the newest, and the build
// validates it from inside the image with its --version gate. The devcontainer
// base exists because an isolated agent should run in the environment the
// human develops in; its "features" are not honored because this build does
// not depend on the devcontainer CLI, and a compose project needs a service
// named because several services do not map to one agent container. A chosen
// base that fails to build is refused, never silently substituted.
var containerBuildCmd = &cobra.Command{
	Use:   "build [backend]",
	Short: "Build the agent container image for a backend",
	Long: `Build the agent image a containerized run of the given backend uses (the
configured default backend when omitted). Each image carries one engine and
the running ctxloom binary; --engines (or config isolation_engines) builds
several, one image each.

The base comes from --base (or config isolation_base): 'ctxloom' (the
embedded default), 'devcontainer' (the project's .devcontainer/devcontainer.json
or .devcontainer.json; its "features" are skipped with a warning, and a
compose file needs --devcontainer-service), or an image ref. Unset, the
project's devcontainer is used when there is one. --overlay-image instead adds
ctxloom to an image that already ships the engine's client.

Builds use --pull --no-cache so the client is the newest; --keep-cache reuses
layers for quick local iteration. ` + "`ctxloom run`" + ` and delegated agents build
the image themselves when it is missing; use this command to refresh it. To
run your own image as-is, set isolation_images in config instead.`,
	Example: `  ctxloom container build
  ctxloom container build --runtime podman`,
	Args: cobra.MaximumNArgs(1),
	RunE: runContainerBuild,
}

func runContainerBuild(cmd *cobra.Command, args []string) error {
	var backend string
	cfg, cerr := GetConfig()
	if len(args) == 1 {
		backend = args[0]
		if names := operations.EngineNames(App().Engines()); !slices.Contains(names, backend) {
			sort.Strings(names)
			return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
		}
	} else {
		if cerr != nil {
			return fmt.Errorf("no backend given and the config is unavailable to resolve the default: %w", cerr)
		}
		backend, _ = operations.ResolveBackend(App().Engines(), cfg, "")
	}

	if cerr != nil {
		cfg = nil
	}
	opts := containerBuildOptions(containerBuildFlagValues{
		OverlayImage:        containerBuildOverlayImage,
		Base:                containerBuildBase,
		Runtime:             containerBuildRuntime,
		DevcontainerService: containerBuildDevcontainerService,
		Engines:             containerBuildEngines,
		KeepCache:           containerBuildKeepCache,
	}, cfg, backend, cmd.ErrOrStderr())
	opts.Output = os.Stdout

	// --engines / isolation_engines names WHICH IMAGES TO BUILD, not what goes
	// inside one image. An agent image carries exactly ONE engine, so there is
	// no composition to select — but pre-building several at once is still
	// useful, and a flag that silently did nothing would be worse than no flag
	// (this codebase's characteristic failure: exit 0, a success message, and
	// the thing not done).
	targets := opts.Engines
	if len(targets) == 0 {
		targets = []string{backend}
	}
	for _, engine := range targets {
		image, err := isolation.BuildAgentImage(cmd.Context(), engine, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Built %s for backend %s\n", image, engine)
	}
	return nil
}

// containerBuildFlagValues carries `container build`'s own flag state into
// containerBuildOptions, so the flag-over-config precedence is resolvable —
// and testable — without a cobra command or a container runtime.
type containerBuildFlagValues struct {
	OverlayImage        string
	Base                string
	Runtime             string
	DevcontainerService string
	Engines             []string
	KeepCache           bool
}

// containerBuildOptions resolves the build options from flags plus config: a
// config knob applies only where the corresponding flag didn't pick a value,
// so the explicit build and the on-the-fly build resolve the same base/engine
// set. A nil cfg (config unavailable) resolves from flags alone.
//
// Two invariants live here:
//
//   - OverlayImage and Base are mutually exclusive (isolation.BuildAgentImage
//     rejects the pair outright), so a config isolation_base is inherited only
//     when NEITHER flag chose a base. An
//     explicit --overlay-image must never be turned into a hard failure by a
//     project default the user did not name on this command line.
//   - a config isolation_images entry for this backend is run AS-IS and never
//     built (isolation.containerFor), so whatever this command builds is not
//     the image a run will use. That is reported on warn, because building an
//     image nothing will run is otherwise indistinguishable from success.
func containerBuildOptions(flags containerBuildFlagValues, cfg *config.Config, backend string, warn io.Writer) isolation.ImageBuildOptions {
	opts := isolation.ImageBuildOptions{
		OverlayImage: flags.OverlayImage,
		Base:         flags.Base,
		Runtime:      flags.Runtime,
		KeepCache:    flags.KeepCache,
	}
	if cfg != nil {
		img := launch.ImageConfigFor(cfg, engine.Name(backend))
		if opts.OverlayImage == "" && opts.Base == "" {
			opts.Base = img.Base
		}
		opts.AppRoot = img.AppRoot
		opts.DevcontainerService = img.DevcontainerService
		opts.Engines = img.Engines
		if img.Image != "" {
			clidiag.Fwarn(warn, "ctxloom", "config isolation_images pins %s for backend %s: that image is run AS-IS and never built, so a %s agent will NOT use the image this build produces (unset isolation_images for %s to run what you build)",
				img.Image, backend, backend, backend)
		}
	}
	if flags.DevcontainerService != "" {
		opts.DevcontainerService = flags.DevcontainerService
	}
	if len(flags.Engines) > 0 {
		opts.Engines = flags.Engines
	}
	return opts
}

// toolingPrompt is the instruction preamble `container tooling list`
// emits above the collected bundle declarations; its text is
// resources/prompts/tooling.md. A markdown resource, not Go — the procedure is data.
var toolingPrompt = resources.MustGetPromptText("tooling")

// toolingCmdLong documents `ctxloom container tooling`.
const toolingCmdLong = `Collect every registered companion's typed 'tooling' declaration — the
tools its content needs inside the agent container image — and emit them with
instructions for the LLM: fold the additions into the agent image's base
(the project devcontainer's Dockerfile; 'ctxloom container scaffold' writes one
when the project has none) as a diff, get the user's explicit approval per
change, then rebuild ('ctxloom container build').

Only companions you registered ('ctxloom companion add') contribute: an
unregistered companion on PATH is never run, so it declares nothing, and
project or remote bundles carry no tooling declaration at all. Nothing is ever
applied automatically on pull/sync — the edit is the LLM's, gated by the user.`

// runToolingListCmd is containerToolingListCmd's RunE. It emits the
// agent-image tooling instructions plus every registered companion's typed
// tooling declaration (bundles.InitLoadout.Tooling): the LLM runs this, reads
// the declarations, and folds them — with
// the user's explicit approval — into the agent image's base.
// Read-only: nothing is written here.
func runToolingListCmd(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	entries := operations.CollectTooling(cfg, nil)
	return emit(cmd, toolingJSON{Instructions: toolingPrompt, Declarations: entries}, func() error {
		return renderTooling(cmd.OutOrStdout(), entries)
	})
}

// containerToolingCmd is the container noun's `tooling` sub-noun: a namespace
// carrying the spine verb `list` rather than a bare leaf, so it composes with
// the rest of the CLI's noun-verb shape. Its bare form still lists — the same
// bare-noun-lists-by-default seam `ctxloom remote` uses — so nothing a caller
// already types (`ctxloom container tooling`) stops working.
var containerToolingCmd = groupNodeDefault(&cobra.Command{
	Use:   "tooling",
	Short: "Agent-image tooling declarations from registered companions",
	Long:  toolingCmdLong,
}, "list")

// containerToolingListCmd is the tooling sub-noun's `list` domain verb: emit
// every registered companion's declared agent-image tooling for the LLM to
// apply.
var containerToolingListCmd = &cobra.Command{
	Use:     "list",
	Short:   "Emit registered companions' agent-image tooling declarations for the LLM to apply",
	Example: `  ctxloom container tooling list`,
	Args:    cobra.NoArgs,
	RunE:    runToolingListCmd,
}

// toolingJSON is the --format json shape for `container tooling list`.
type toolingJSON struct {
	Instructions string                          `json:"instructions"`
	Declarations []operations.ToolingDeclaration `json:"declarations"`
}

// renderTooling writes the instruction preamble plus the collected
// declarations, each attributed to its source bundle. Extracted from RunE so
// the formatting is testable with injected entries.
func renderTooling(out io.Writer, entries []operations.ToolingDeclaration) error {
	w := errwriter.New(out)
	if len(entries) == 0 {
		w.Println("No registered companion declares container tooling (a companion declares it as `tooling` in its loadout's init section).")
		w.Println("An unregistered companion is never run, so it declares nothing — register one with `ctxloom companion add`.")
		return w.Err()
	}
	w.Println(toolingPrompt)
	for _, e := range entries {
		w.Printf("\n### %s\n\n%s\n", e.Source, e.Content)
	}
	return w.Err()
}

// containerScaffoldCmd is the write half the tooling flow calls (with the
// user's permission): give a project with no devcontainer one, seeded from the
// embedded default base, so its agent image's base becomes editable.
var containerScaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Write a project devcontainer seeded from ctxloom's default base",
	Long: `Write .devcontainer/devcontainer.json and a .devcontainer/Dockerfile seeded
from ctxloom's embedded default base, so the agent image's base is a file you
can edit — and the environment your editor's devcontainer support opens too.

With isolation_base unset (or 'devcontainer'), every locally-built agent image
builds on it from then on. Refused when the project already has a devcontainer
(.devcontainer/ or .devcontainer.json): edit that one instead.`,
	Example: `  ctxloom container scaffold`,
	Args:    cobra.NoArgs,
	RunE:    runContainerScaffold,
}

func runContainerScaffold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	dir, err := operations.ScaffoldDevcontainer(cfg)
	if err != nil {
		return err
	}
	w := errwriter.New(cmd.OutOrStdout())
	w.Printf("Devcontainer: %s\n", dir)
	w.Println("Edit its Dockerfile (the engine's agent stage layers on top), then run `ctxloom container build`.")
	if base := cfg.IsolationBase(); base != "" && base != config.IsolationBaseDevcontainer {
		clidiag.Fwarn(cmd.ErrOrStderr(), "ctxloom", "isolation_base is %q, so agent images do NOT build on this devcontainer; unset it (or set isolation_base: devcontainer) to use it", base)
	}
	return w.Err()
}

// containerCheckCmd reports whether `runtime: container` agents can actually
// launch here: in-container detection, runtime reachability, image presence,
// and the shared-filesystem probe (the docker-outside-of-docker detector).
// Diagnostic only — no probe outcome ever fails the command, and nothing is
// built or changed. USAGE errors still exit non-zero: an unknown backend
// argument, or a --format value this build cannot render (emit()).
var containerCheckCmd = &cobra.Command{
	Use:   "check [backend]",
	Short: "Diagnose container capability (runtime, image, shared filesystem)",
	Long: `Report whether containerized agents ('runtime: container') can launch here:

  - whether THIS process runs inside a container (dev container, CI, pod)
  - which container runtime is reachable (docker | podman)
  - whether the backend's agent image is present locally
  - whether the runtime's daemon shares this filesystem — the marker probe
    that detects docker-outside-of-docker, where bind mounts silently
    resolve against the WRONG filesystem and launches hang

Diagnostic only: no probe outcome fails the command, and nothing is built or
changed — read the report, not the exit code. A usage error is still an error
(an unknown backend argument, or a --format this build cannot render).
Run it inside a dev container to learn whether its agents can use the host's
daemon through a mounted socket (docker-outside-of-docker: every path they
mount must be on a bind mount or volume of the dev container), need
docker-in-docker, or should stay on 'runtime: host'.`,
	Example: `  ctxloom container check`,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runContainerCheck,
}

func runContainerCheck(cmd *cobra.Command, args []string) error {
	cfg, cerr := GetConfig()
	var backend string
	if len(args) == 1 {
		backend = args[0]
		if names := operations.EngineNames(App().Engines()); !slices.Contains(names, backend) {
			sort.Strings(names)
			return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
		}
	} else if cerr == nil {
		backend, _ = operations.ResolveBackend(App().Engines(), cfg, "")
	}
	img := isolation.ImageConfig{}
	if cerr == nil {
		img = launch.ImageConfigFor(cfg, engine.Name(backend))
	}
	d := containerCheckConfigGap(isolation.Diagnose(cmd.Context(), backend, img), len(args) == 1, cerr)
	return emit(cmd, d, func() error { return renderContainerCheck(cmd.OutOrStdout(), backend, d) })
}

// containerCheckConfigGap folds an unloadable config into the diagnosis as
// guidance. Without it the command reports on whatever it could still probe
// and says nothing about the config: with no backend argument the backend stays
// EMPTY, so the report describes no engine at all, and with one given the
// project's isolation_images override is never consulted. A diagnostic that
// silently narrows its own scope is worse than one that names the gap.
func containerCheckConfigGap(d isolation.Diagnosis, backendGiven bool, cerr error) isolation.Diagnosis {
	if cerr == nil {
		return d
	}
	if backendGiven {
		d.Guidance = append(d.Guidance, fmt.Sprintf(
			"the config did not load (%v), so this project's isolation_images override was not consulted — the image line above reflects the default tag only", cerr))
		return d
	}
	d.Guidance = append(d.Guidance, fmt.Sprintf(
		"the config did not load (%v), so no default backend could be resolved and no engine's image was checked — name one explicitly: `ctxloom container check <backend>`", cerr))
	return d
}

// renderContainerCheck writes the human-readable diagnosis. Extracted from
// RunE so the formatting is testable with an injected report.
func renderContainerCheck(out io.Writer, backend string, d isolation.Diagnosis) error {
	w := errwriter.New(out)
	if backend == "" {
		// An unresolved backend is a real state here (no argument plus an
		// unloadable config); an empty parenthesis reads as a rendering bug.
		backend = "(unresolved)"
	}
	w.Printf("Container capability (backend: %s)\n", backend)
	if d.InContainer {
		w.Printf("  in a container:  yes (%s)\n", strings.Join(d.Markers, ", "))
	} else {
		w.Println("  in a container:  no")
	}
	if d.Reachable {
		w.Printf("  runtime:         %s (reachable)\n", d.Runtime)
	} else {
		w.Printf("  runtime:         %s\n", d.Runtime)
	}
	if d.Image != "" {
		presence := "absent"
		if d.ImagePresent {
			presence = "present"
			if d.ImageStale {
				presence = "present (stale — rebuilds next run)"
			}
		}
		w.Printf("  agent image:     %s (%s)\n", d.Image, presence)
	}
	w.Printf("  shared fs:       %s\n", d.SharedFS)
	for _, g := range d.Guidance {
		w.Printf("  -> %s\n", g)
	}
	return w.Err()
}

func init() {
	containerBuildCmd.Flags().StringVar(&containerBuildOverlayImage, "overlay-image", "",
		"overlay ctxloom onto this base image (must already ship the client CLI) instead of the default build sources")
	containerBuildCmd.Flags().StringVar(&containerBuildBase, "base", "",
		"the base the engine's agent stage layers onto: ctxloom | devcontainer | <image ref> (overrides config isolation_base)")
	containerBuildCmd.Flags().StringVar(&containerBuildDevcontainerService, "devcontainer-service", "",
		"docker-compose service to use as the base when the project devcontainer.json declares dockerComposeFile")
	containerBuildCmd.Flags().StringSliceVar(&containerBuildEngines, "engines", nil,
		"engines to build an agent image for, one image each (any engine that declares a container installer); empty = the configured backend")
	containerBuildCmd.Flags().StringVar(&containerBuildRuntime, "runtime", "",
		"container runtime to build with (docker|podman); auto-detected when empty")
	containerBuildCmd.Flags().BoolVar(&containerBuildKeepCache, "keep-cache", false,
		"reuse cached layers instead of --pull --no-cache (a fresh build fetches the most recent client)")
	containerCmd.AddCommand(containerBuildCmd)
	containerCmd.AddCommand(containerCheckCmd)
	containerCmd.AddCommand(containerScaffoldCmd)
	// Real home of the deprecated top-level `ctxloom tooling`: the container
	// image is (today) where declarations land.
	containerCmd.AddCommand(containerToolingCmd)
	containerToolingCmd.AddCommand(containerToolingListCmd)
	rootCmd.AddCommand(containerCmd)
}
