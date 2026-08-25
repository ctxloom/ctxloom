package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/resources"
)

var containerCmd = groupNode(&cobra.Command{
	Use:   "container",
	Short: "Manage agent container images",
	Long: `Manage the per-backend agent images a containerized engine runs in
(agents with 'runtime: container', or the project 'runtime:' default).`,
})

var (
	containerBuildBaseImage           string
	containerBuildBaseContainerfile   string
	containerBuildNoDevcontainerBase  bool
	containerBuildDevcontainerService string
	containerBuildEngines             []string
	containerBuildRuntime             string
	containerBuildKeepCache           bool
)

var containerBuildCmd = &cobra.Command{
	Use:   "build [backend]",
	Short: "Build the agent container image for a backend",
	Long: `Build the agent image a containerized run of the given backend uses
(the configured default backend when omitted).

The image builds in two stages: a shared BASE (the distro plus the coding-agent
tool layer — git, ripgrep, curl, certs, jq) and the engine's AGENT stage (the
client CLI install plus the RUNNING ctxloom binary) layered on top, so a
rebuilt image never needs a ctxloom release. The client validates the build
from inside the image (its --version gate), and the install fetches the MOST
RECENT client — never pinned.

The base stage is yours to replace: --base-containerfile (or config
isolation_base_containerfile) builds the base from your own Containerfile —
your tools, your certs, your mirrors — and the same agent stage layers on top.
Alternatively --base-image skips the client install entirely and overlays
ctxloom onto an image that ALREADY ships the client CLI.

Absent an explicit base, the project's own .devcontainer/devcontainer.json (or
.devcontainer.json) is auto-detected and used as the base instead of the
embedded default — "an isolated agent should run in the environment the human
develops in". --no-devcontainer-base (or config isolation_devcontainer_base:
false) opts out. A devcontainer.json declaring "features" is NOT honored
(pre1 does not depend on the devcontainer CLI) — a loud warning names what is
skipped. A devcontainer.json declaring dockerComposeFile needs an explicit
service pick (--devcontainer-service, or config isolation_devcontainer_service)
since a multi-service compose project does not map to one agent container.

The engine set (claude-code, codex, kiro, opencode — each via
its OWN official installer, one independently-cacheable Containerfile layer)
composes into ONE shared image by default; --engines (or config
isolation_engines) trims it.

By default the build runs with --pull --no-cache so a rebuild picks up the most
recent client; --keep-cache reuses layers for a fast local iteration. Runs of
` + "`ctxloom run`/`acp`" + ` (and delegated ` + "`agent_run`" + ` children) also build this image
automatically when it is absent (honoring the same base/engine resolution); this command is the
explicit path (refresh, custom base). To run a fully user-provided image
instead, set isolation_images in config — those are run as-is and never built.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runContainerBuild,
}

func runContainerBuild(cmd *cobra.Command, args []string) error {
	var backend string
	cfg, cerr := GetConfig()
	if len(args) == 1 {
		backend = args[0]
		if names := backends.List(); !slices.Contains(names, backend) {
			sort.Strings(names)
			return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
		}
	} else {
		if cerr != nil {
			return fmt.Errorf("no backend given and the config is unavailable to resolve the default: %w", cerr)
		}
		backend, _ = operations.ResolveBackend(cfg, "")
	}

	if cerr != nil {
		cfg = nil
	}
	opts := containerBuildOptions(containerBuildFlagValues{
		BaseImage:           containerBuildBaseImage,
		BaseContainerfile:   containerBuildBaseContainerfile,
		Runtime:             containerBuildRuntime,
		DevcontainerService: containerBuildDevcontainerService,
		Engines:             containerBuildEngines,
		NoDevcontainerBase:  containerBuildNoDevcontainerBase,
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
	BaseImage           string
	BaseContainerfile   string
	Runtime             string
	DevcontainerService string
	Engines             []string
	NoDevcontainerBase  bool
	KeepCache           bool
}

// containerBuildOptions resolves the build options from flags plus config: a
// config knob applies only where the corresponding flag didn't pick a value,
// so the explicit build and the on-the-fly build resolve the same base/engine
// set. A nil cfg (config unavailable) resolves from flags alone.
//
// Two invariants live here:
//
//   - BaseImage and BaseContainerfile are mutually exclusive
//     (isolation.BuildAgentImage rejects the pair outright), so a config base
//     Containerfile is inherited only when NEITHER flag chose a base. An
//     explicit --base-image must never be turned into a hard failure by a
//     project default the user did not name on this command line.
//   - a config isolation_images entry for this backend is run AS-IS and never
//     built (isolation.containerFor), so whatever this command builds is not
//     the image a run will use. That is reported on warn, because building an
//     image nothing will run is otherwise indistinguishable from success.
func containerBuildOptions(flags containerBuildFlagValues, cfg *config.Config, backend string, warn io.Writer) isolation.ImageBuildOptions {
	opts := isolation.ImageBuildOptions{
		BaseImage:         flags.BaseImage,
		BaseContainerfile: flags.BaseContainerfile,
		Runtime:           flags.Runtime,
		KeepCache:         flags.KeepCache,
	}
	if cfg != nil {
		img := operations.IsolationImageConfig(cfg, backend)
		if opts.BaseImage == "" && opts.BaseContainerfile == "" {
			opts.BaseContainerfile = img.BaseContainerfile
		}
		opts.AppRoot = img.AppRoot
		opts.NoDevcontainerBase = img.NoDevcontainerBase
		opts.DevcontainerService = img.DevcontainerService
		opts.Engines = img.Engines
		if img.Image != "" {
			clidiag.Fwarn(warn, "ctxloom", "config isolation_images pins %s for backend %s: that image is run AS-IS and never built, so a %s agent will NOT use the image this build produces (unset isolation_images for %s to run what you build)",
				img.Image, backend, backend, backend)
		}
	}
	if flags.NoDevcontainerBase {
		opts.NoDevcontainerBase = true
	}
	if flags.DevcontainerService != "" {
		opts.DevcontainerService = flags.DevcontainerService
	}
	if len(flags.Engines) > 0 {
		opts.Engines = flags.Engines
	}
	return opts
}

// containerProvenanceCmd prints the content digest of the ctxloom + companion
// binaries THIS ctxloom would bake into an agent image — the value stamped as
// the image's ctxloom.provenance label. Hidden plumbing: the ahead-of-time
// `container-build-*` just recipes run the freshly-built binary through it to
// stamp a label matching what they bake, so a later run doesn't see the image
// as stale. Also handy for debugging a rebuild ("does the image's label match
// `ctxloom container provenance`?").
var containerProvenanceCmd = &cobra.Command{
	Use:    "provenance",
	Short:  "Print the content digest of the binaries baked into agent images",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runContainerProvenance,
}

func runContainerProvenance(cmd *cobra.Command, args []string) error {
	// The DEFAULT-base digest on purpose: the ahead-of-time just recipes
	// this stamps for bake the embedded default base; a custom
	// isolation_base_containerfile build stamps its own label inside
	// BuildAgentImage.
	fmt.Fprintln(cmd.OutOrStdout(), isolation.HostProvenanceDigest(""))
	return nil
}

// toolingPrompt is the instruction preamble `container tooling list`
// emits above the collected bundle declarations: locate/scaffold the base
// Containerfile, propose a diff, get EXPLICIT per-change user approval,
// rebuild. A markdown resource, not Go — the procedure is data.
var toolingPrompt = resources.MustGetPromptText("tooling")

// toolingCmdLong documents `ctxloom container tooling`.
const toolingCmdLong = `Collect every trusted bundle's 'tooling' command — the tools its
content needs inside the agent container image — and emit them with
instructions for the LLM: scaffold/locate the editable base Containerfile
('ctxloom container scaffold'), propose the additions as a diff, get the
user's explicit approval per change, then rebuild ('ctxloom container build').

Collection is TRUST-GATED: declarations from unreviewed bundles are withheld
like any other gated content, and nothing is ever applied automatically on
pull/sync — the edit is the LLM's, gated by the user.`

// runToolingListCmd is containerToolingListCmd's RunE. It emits the
// agent-image tooling instructions plus every TRUSTED bundle's `tooling`
// command: the LLM runs this, reads the declarations, and folds them — with
// the user's explicit approval — into the scaffolded base Containerfile.
// Read-only: collection goes through the trust gate and nothing is written
// here.
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
	Short: "Agent-image tooling declarations from trusted bundles",
	Long:  toolingCmdLong,
}, "list")

// containerToolingListCmd is the tooling sub-noun's `list` domain verb: emit
// every trusted bundle's declared agent-image tooling for the LLM to apply.
var containerToolingListCmd = &cobra.Command{
	Use:   "list",
	Short: "Emit trusted bundles' agent-image tooling declarations for the LLM to apply",
	Args:  cobra.NoArgs,
	RunE:  runToolingListCmd,
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
	w := iox.NewErrWriter(out)
	if len(entries) == 0 {
		w.Println("No trusted bundles declare container tooling (a bundle ships it as a 'tooling' command).")
		w.Println("Untrusted declarations are withheld — review with `ctxloom review`, then re-run.")
		return w.Err()
	}
	w.Println(toolingPrompt)
	for _, e := range entries {
		w.Printf("\n### %s\n\n%s\n", e.Source, e.Content)
	}
	return w.Err()
}

// containerScaffoldCmd is the write half the tooling flow calls (with the
// user's permission): materialize the embedded default base Containerfile as
// an editable local file and wire it into config, so the default auto-build
// and explicit builds pick it up from then on.
var (
	containerScaffoldPath  string
	containerScaffoldForce bool
)

var containerScaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Materialize the editable base Containerfile and wire it into config",
	Long: `Write the embedded default base Containerfile to an editable local file
(default: .ctxloom/base.Containerfile) and set 'isolation_base_containerfile'
so every locally-built agent image — the default auto-build included — layers
on it. Content-identical to what the default build was already using, so
nothing changes until you edit it.

Idempotent and WIP-safe: an already-configured base is returned as-is, and an
existing file at the target is adopted, never overwritten (--force overwrites).`,
	Args: cobra.NoArgs,
	RunE: runContainerScaffold,
}

func runContainerScaffold(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	path, err := operations.ScaffoldContainerBase(config.NewManager(), cfg, containerScaffoldPath, containerScaffoldForce)
	if err != nil {
		return err
	}
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("Base Containerfile: %s\n", path)
	w.Println("Edit it (the engine's agent stage layers on top), then run `ctxloom container build`.")
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
Run it inside a dev container to learn whether to enable docker-in-docker
or keep agents on 'runtime: host'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runContainerCheck,
}

func runContainerCheck(cmd *cobra.Command, args []string) error {
	cfg, cerr := GetConfig()
	var backend string
	if len(args) == 1 {
		backend = args[0]
		if names := backends.List(); !slices.Contains(names, backend) {
			sort.Strings(names)
			return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
		}
	} else if cerr == nil {
		backend, _ = operations.ResolveBackend(cfg, "")
	}
	img := isolation.ImageConfig{}
	if cerr == nil {
		img = operations.IsolationImageConfig(cfg, backend)
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
	w := iox.NewErrWriter(out)
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
	containerBuildCmd.Flags().StringVar(&containerBuildBaseImage, "base-image", "",
		"overlay ctxloom onto this base image (must already ship the client CLI) instead of the default build sources")
	containerBuildCmd.Flags().StringVar(&containerBuildBaseContainerfile, "base-containerfile", "",
		"build the shared base stage from this Containerfile (your environment; the engine's agent stage layers on top) instead of an auto-detected devcontainer / the embedded default")
	containerBuildCmd.Flags().BoolVar(&containerBuildNoDevcontainerBase, "no-devcontainer-base", false,
		"do not auto-detect the project's .devcontainer/devcontainer.json as the base image")
	containerBuildCmd.Flags().StringVar(&containerBuildDevcontainerService, "devcontainer-service", "",
		"docker-compose service to use as the base when the detected devcontainer.json declares dockerComposeFile")
	containerBuildCmd.Flags().StringSliceVar(&containerBuildEngines, "engines", nil,
		"engines to compose into the agent image (claude-code,codex,kiro,opencode); empty = every known engine")
	containerBuildCmd.Flags().StringVar(&containerBuildRuntime, "runtime", "",
		"container runtime to build with (docker|podman); auto-detected when empty")
	containerBuildCmd.Flags().BoolVar(&containerBuildKeepCache, "keep-cache", false,
		"reuse cached layers instead of --pull --no-cache (a fresh build fetches the most recent client)")
	containerScaffoldCmd.Flags().StringVar(&containerScaffoldPath, "path", "",
		"project-root-relative path for the base Containerfile (default .ctxloom/base.Containerfile)")
	containerScaffoldCmd.Flags().BoolVar(&containerScaffoldForce, "force", false,
		"overwrite an existing file / re-point an already-configured base")
	containerCmd.AddCommand(containerBuildCmd)
	containerCmd.AddCommand(containerCheckCmd)
	containerCmd.AddCommand(containerScaffoldCmd)
	containerCmd.AddCommand(containerProvenanceCmd)
	// Real home of the deprecated top-level `ctxloom tooling`: the container
	// image is (today) where declarations land.
	containerCmd.AddCommand(containerToolingCmd)
	containerToolingCmd.AddCommand(containerToolingListCmd)
	rootCmd.AddCommand(containerCmd)
}
