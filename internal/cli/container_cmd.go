package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var containerCmd = &cobra.Command{
	Use:   "container",
	Short: "Manage agent container images",
	Long: `Manage the per-backend agent images a containerized engine runs in
(agents with 'runtime: container', or the project 'runtime:' default).`,
}

var (
	containerBuildBaseImage         string
	containerBuildBaseContainerfile string
	containerBuildRuntime           string
	containerBuildKeepCache         bool
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

By default the build runs with --pull --no-cache so a rebuild picks up the most
recent client; --keep-cache reuses layers for a fast local iteration. Runs of
` + "`ctxloom run`/`map`/`weave`" + ` also build this image automatically when it
is absent (honoring isolation_base_containerfile); this command is the explicit
path (refresh, custom base). To run a fully user-provided image instead, set
isolation_images in config — those are run as-is and never built.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var backend string
		if len(args) == 1 {
			backend = args[0]
			if names := backends.List(); !slices.Contains(names, backend) {
				sort.Strings(names)
				return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
			}
		} else {
			cfg, err := GetConfig()
			if err != nil {
				return fmt.Errorf("no backend given and the config is unavailable to resolve the default: %w", err)
			}
			backend, _ = operations.ResolveBackend(cfg, "")
		}

		baseContainerfile := containerBuildBaseContainerfile
		if baseContainerfile == "" {
			// The config knob applies when the flag doesn't override it, so the
			// explicit build and the on-the-fly build produce the same image.
			if cfg, cerr := GetConfig(); cerr == nil {
				baseContainerfile = cfg.IsolationBaseContainerfilePath()
			}
		}
		image, err := isolation.BuildAgentImage(cmd.Context(), backend, isolation.ImageBuildOptions{
			BaseImage:         containerBuildBaseImage,
			BaseContainerfile: baseContainerfile,
			Runtime:           containerBuildRuntime,
			KeepCache:         containerBuildKeepCache,
			Output:            os.Stdout,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Built %s for backend %s\n", image, backend)
		return nil
	},
}

// containerDiagnose is the Diagnose seam, a var so the CLI rendering is
// testable with an injected report.
var containerDiagnose = isolation.Diagnose

// containerCheckCmd reports whether `runtime: container` agents can actually
// launch here: in-container detection, runtime reachability, image presence,
// and the shared-filesystem probe (the docker-outside-of-docker detector).
// Diagnostic only — always exits 0, never builds or changes anything.
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

Diagnostic only: always exits 0 and never builds images or changes state.
Run it inside a dev container to learn whether to enable docker-in-docker
or keep agents on 'runtime: host'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var backend string
		if len(args) == 1 {
			backend = args[0]
			if names := backends.List(); !slices.Contains(names, backend) {
				sort.Strings(names)
				return fmt.Errorf("unknown backend %q (available: %s)", backend, strings.Join(names, ", "))
			}
		} else if cfg, err := GetConfig(); err == nil {
			backend, _ = operations.ResolveBackend(cfg, "")
		}
		img := isolation.ImageConfig{}
		if cfg, err := GetConfig(); err == nil {
			img = operations.IsolationImageConfig(cfg, backend)
		}
		d := containerDiagnose(cmd.Context(), backend, img)
		return emit(cmd, d, func() error { return renderContainerCheck(cmd.OutOrStdout(), backend, d) })
	},
}

// renderContainerCheck writes the human-readable diagnosis. Extracted from
// RunE so the formatting is testable with an injected report.
func renderContainerCheck(out io.Writer, backend string, d isolation.Diagnosis) error {
	w := iox.NewErrWriter(out)
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
		"build the shared base stage from this Containerfile (your environment; the engine's agent stage layers on top) instead of the embedded default")
	containerBuildCmd.Flags().StringVar(&containerBuildRuntime, "runtime", "",
		"container runtime to build with (docker|podman); auto-detected when empty")
	containerBuildCmd.Flags().BoolVar(&containerBuildKeepCache, "keep-cache", false,
		"reuse cached layers instead of --pull --no-cache (a fresh build fetches the most recent client)")
	containerCmd.AddCommand(containerBuildCmd)
	containerCmd.AddCommand(containerCheckCmd)
	rootCmd.AddCommand(containerCmd)
}
