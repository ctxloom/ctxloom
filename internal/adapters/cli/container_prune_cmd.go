package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var (
	containerPruneApply   bool
	containerPruneMinAge  time.Duration
	containerPruneRuntime string
)

var containerPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove superseded ctxloom agent images (a dry run unless --apply)",
	Long: `Find the agent images ctxloom built that nothing uses any more, and — with
--apply — remove them. Without --apply this only prints the plan.

Every ctxloom commit (and every change to the admitted companion set) builds
a new agent image tag, and the old ones are never overwritten, so they pile
up. Every ctxloom build also applies a one-off ownership tag (own-...) that
no later build reuses or moves, and labels the image with it. An image is
ctxloom's only if it still carries that labelled ownership tag, so a build
whose primary tag a rebuild took over is still found and removed. An image
that merely looks like ctxloom-agent-*, one built FROM a ctxloom image under
another tag, or one with no tag left at all is reported as unowned and never
touched. Removing an image removes every tag it holds, its ownership tag
included.

An owned image is KEPT when any of these holds:
  - it is the image a configured agent in this project runs (current)
  - a container, running or stopped, uses it (referenced)
  - it is the newest image in its slot — same kind, engine, base content and
    companion set — which protects other projects, HOMEs and worktrees
  - it is younger than --min-age (young)
  - a kept image was built FROM it (parent)
Everything else is superseded.

Every runtime present is swept (docker, podman), one section each; --runtime
narrows it to one. Removal is never forced: the runtime still refuses an
image a container started using since the plan was made.

Exit status: 0 when the plan printed or everything planned was removed
(including nothing to do); 1 when any removal failed (each is listed); 3 when
no container runtime is available.`,
	Args: cobra.NoArgs,
	RunE: runContainerPrune,
}

func runContainerPrune(cmd *cobra.Command, _ []string) error {
	// No runtime is a ClassIsolation finding recorded inside ContainerPrune;
	// this gate is what turns it into exit 3.
	gates := newPhaseGates(os.Stderr, App().Strictness)
	rep, err := containerPrune(cmd.Context(), App(), operations.ContainerPruneRequest{
		Apply:   containerPruneApply,
		MinAge:  containerPruneMinAge,
		Runtime: containerPruneRuntime,
	})
	if err != nil {
		return err
	}
	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}
	if err := emit(cmd, rep, func() error { return renderContainerPrune(cmd.OutOrStdout(), rep) }); err != nil {
		return err
	}
	return containerPruneExit(rep)
}

// containerPrune is the prune service; a var so the CLI test scripts its
// outcomes (no runtime, a failed removal) without a container runtime.
var containerPrune = operations.ContainerPrune

// containerPruneExit is the status after the report is out: 1 when anything
// planned did not happen, nil otherwise — a dry run with work to do is not a
// failure.
func containerPruneExit(rep operations.ContainerPruneReport) error {
	if rep.Failed() {
		return &ExitError{Code: 1}
	}
	return nil
}

// pruneActionLabels are the column labels of the text report.
var pruneActionLabels = map[operations.PruneAction]string{
	operations.PruneKeep:    "KEEP",
	operations.PruneRemove:  "REMOVE",
	operations.PruneRemoved: "REMOVED",
	operations.PruneFailed:  "FAILED",
}

// renderContainerPrune writes the human report: one section per runtime, one
// line per image, then the section's tally. Extracted from RunE so the
// formatting is testable with an injected report.
func renderContainerPrune(out io.Writer, rep operations.ContainerPruneReport) error {
	w := iox.NewErrWriter(out)
	for _, sec := range rep.Runtimes {
		w.Println(sec.Runtime)
		if sec.Error != "" {
			w.Printf("  ERROR   %s\n", sec.Error)
			continue
		}
		for _, img := range sec.Images {
			w.Printf("  %-8s%s  %s\n", pruneActionLabels[img.Action], img.Ref, pruneReasonText(img, rep))
		}
		for _, ref := range sec.Unowned {
			w.Printf("  %-8s%s  unowned (no ctxloom label)\n", "SKIP", ref)
		}
		w.Printf("  %s\n", pruneTally(sec, rep.Applied))
	}
	return w.Err()
}

// pruneReasonText is one line's reason in words.
func pruneReasonText(img operations.ContainerPruneImage, rep operations.ContainerPruneReport) string {
	switch img.Action {
	case operations.PruneFailed:
		return "removal failed: " + img.Error
	case operations.PruneRemove, operations.PruneRemoved:
		return fmt.Sprintf("superseded (%s old, %s)", pruneAge(img), operations.FormatImageBytes(img.Bytes))
	}
	switch img.Reason {
	case isolation.KeepCurrent:
		return "current identity"
	case isolation.KeepReferenced:
		return "used by a container"
	case isolation.KeepNewestInSlot:
		return "newest in slot"
	case isolation.KeepYoung:
		return "younger than " + rep.MinAge.String()
	case isolation.KeepParent:
		return "base of a kept image"
	}
	return img.Reason.String()
}

// pruneAge renders an image's age in whole days (hours under a day).
func pruneAge(img operations.ContainerPruneImage) string {
	age := pruneNow().Sub(img.Created)
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}

// pruneNow is the render clock; a var so the golden test pins it.
var pruneNow = time.Now

// pruneTally is a section's closing line.
func pruneTally(sec operations.ContainerPruneRuntime, applied bool) string {
	counts := map[operations.PruneAction]int{}
	for _, img := range sec.Images {
		counts[img.Action]++
	}
	if !applied {
		if counts[operations.PruneRemove] == 0 {
			return "nothing to remove"
		}
		return fmt.Sprintf("%d to remove, %s reclaimable — dry run; pass --apply to remove",
			counts[operations.PruneRemove], operations.FormatImageBytes(sec.Bytes))
	}
	tally := fmt.Sprintf("%d removed, %s reclaimed", counts[operations.PruneRemoved], operations.FormatImageBytes(sec.Bytes))
	if n := counts[operations.PruneFailed]; n > 0 {
		tally += fmt.Sprintf("; %d failed", n)
	}
	return tally
}

func init() {
	containerPruneCmd.Flags().BoolVar(&containerPruneApply, "apply", false,
		"remove the superseded images (default: print the plan and remove nothing)")
	containerPruneCmd.Flags().DurationVar(&containerPruneMinAge, "min-age", operations.DefaultImagePruneMinAge,
		"keep any image younger than this")
	containerPruneCmd.Flags().StringVar(&containerPruneRuntime, "runtime", "",
		"sweep only this container runtime (docker|podman); every available one when empty")
	containerCmd.AddCommand(containerPruneCmd)
}
