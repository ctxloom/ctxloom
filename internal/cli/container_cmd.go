package cli

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
)

var containerCmd = &cobra.Command{
	Use:   "container",
	Short: "Manage agent container images",
	Long: `Manage the per-backend agent images the container isolation policy runs
(defaults.isolation: container, or a subagent's isolation override).`,
}

var (
	containerBuildBaseImage string
	containerBuildRuntime   string
	containerBuildKeepCache bool
)

var containerBuildCmd = &cobra.Command{
	Use:   "build [backend]",
	Short: "Build the agent container image for a backend",
	Long: `Build the agent image a containerized run of the given backend uses
(the configured default backend when omitted).

The image is assembled from the best available source: the client's OFFICIAL
container image when the vendor ships one (ctxloom overlaid on top), otherwise
an embedded recipe that installs the MOST RECENT client CLI — never pinned. In
both cases the client validates the build from inside the image (its --version
gate), and the RUNNING ctxloom binary is layered in, so a rebuilt image never
needs a ctxloom release.

By default the build runs with --pull --no-cache so a rebuild picks up the most
recent client; --keep-cache reuses layers for a fast local iteration. Runs of
`+"`ctxloom run`/`map`/`weave`"+` also build this image automatically when it
is absent; this command is the explicit path (refresh, custom base). To run a
fully user-provided image instead, set isolation_images in config — those are
run as-is and never built.`,
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

		image, err := isolation.BuildAgentImage(cmd.Context(), backend, isolation.ImageBuildOptions{
			BaseImage: containerBuildBaseImage,
			Runtime:   containerBuildRuntime,
			KeepCache: containerBuildKeepCache,
			Output:    os.Stdout,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Built %s for backend %s\n", image, backend)
		return nil
	},
}

func init() {
	containerBuildCmd.Flags().StringVar(&containerBuildBaseImage, "base-image", "",
		"overlay ctxloom onto this base image (must already ship the client CLI) instead of the default build sources")
	containerBuildCmd.Flags().StringVar(&containerBuildRuntime, "runtime", "",
		"container runtime to build with (docker|podman); auto-detected when empty")
	containerBuildCmd.Flags().BoolVar(&containerBuildKeepCache, "keep-cache", false,
		"reuse cached layers instead of --pull --no-cache (a fresh build fetches the most recent client)")
	containerCmd.AddCommand(containerBuildCmd)
	rootCmd.AddCommand(containerCmd)
}
