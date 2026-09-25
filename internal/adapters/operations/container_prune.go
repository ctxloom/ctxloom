package operations

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// DefaultImagePruneMinAge is the image-side grace window `container prune`
// and the doctor nudge keep anything younger than: long enough that a
// concurrent worktree mid-launch at another commit keeps its image.
const DefaultImagePruneMinAge = 24 * time.Hour

// noPruneRuntimeFixIt is the remedy when prune has no runtime to ask.
const noPruneRuntimeFixIt = "start docker or podman (or name one that is running with --runtime)"

// PruneAction is what `container prune` did, or would do, with one image.
type PruneAction string

const (
	PruneKeep    PruneAction = "keep"
	PruneRemove  PruneAction = "remove" // planned (dry run)
	PruneRemoved PruneAction = "removed"
	PruneFailed  PruneAction = "failed"
)

// ContainerPruneRequest scopes one `container prune`.
type ContainerPruneRequest struct {
	// Apply removes the superseded images; false (the default) only plans.
	Apply bool
	// MinAge keeps anything younger (DefaultImagePruneMinAge when zero).
	MinAge time.Duration
	// Runtime narrows the sweep to one runtime by name; "" sweeps every
	// runtime doctor enumerates.
	Runtime string
}

// ContainerPruneReport is the whole sweep, one section per runtime.
type ContainerPruneReport struct {
	Applied  bool                    `json:"applied"`
	MinAge   time.Duration           `json:"min_age"`
	Runtimes []ContainerPruneRuntime `json:"runtimes"`
}

// ContainerPruneRuntime is one runtime's section.
type ContainerPruneRuntime struct {
	Runtime string                `json:"runtime"`
	Images  []ContainerPruneImage `json:"images"`
	// Unowned are ctxloom-agent-* named images with no ownership label:
	// reported, never touched.
	Unowned []string `json:"unowned,omitempty"`
	// Bytes is the unique-layer total planned for removal (dry run) or
	// actually removed (--apply).
	Bytes int64 `json:"bytes"`
	// Error is set when the runtime could not be planned at all.
	Error string `json:"error,omitempty"`
}

// ContainerPruneImage is one owned image's line.
type ContainerPruneImage struct {
	Ref     string               `json:"ref"`
	Action  PruneAction          `json:"action"`
	Reason  isolation.KeepReason `json:"reason"`
	Created time.Time            `json:"created"`
	Bytes   int64                `json:"bytes"`
	Error   string               `json:"error,omitempty"`
}

// Failed reports whether anything the sweep set out to do did not happen: a
// runtime that could not be planned, or a removal the runtime refused.
func (r ContainerPruneReport) Failed() bool {
	for _, rt := range r.Runtimes {
		if rt.Error != "" {
			return true
		}
		for _, img := range rt.Images {
			if img.Action == PruneFailed {
				return true
			}
		}
	}
	return false
}

// ContainerPrune plans — and with Apply, performs — the removal of superseded
// ctxloom agent images on every runtime present (isolation.PlanImagePrune has
// the keep rules). No runtime at all is a non-degradable ClassIsolation
// finding: the command was asked to act on a container runtime and cannot.
//
// --apply refuses to run on a config that did not load: the current-identity
// rule reads the configured agents, and removing images against an empty
// config would drop that protection silently.
func ContainerPrune(ctx context.Context, app *App, req ContainerPruneRequest) (ContainerPruneReport, error) {
	if req.MinAge == 0 {
		req.MinAge = DefaultImagePruneMinAge
	}
	rep := ContainerPruneReport{Applied: req.Apply, MinAge: req.MinAge}
	if err := knownRuntime(req.Runtime); err != nil {
		return rep, err
	}
	cfg, cfgErr := app.Config(ctx)
	if cfgErr != nil && req.Apply {
		return rep, fmt.Errorf("refusing to prune: the config did not load (%w), so this project's current agent images cannot be protected — fix the config, or run without --apply to see the plan", cfgErr)
	}
	runtimes := pruneRuntimes(pruneAvailableRuntimes(), req.Runtime)
	if len(runtimes) == 0 {
		strictness.FailAlways(strictness.ClassIsolation, noPruneRuntimeFixIt,
			"container prune: no container runtime is available to prune")
		return rep, nil
	}
	now := time.Now()
	for _, rt := range runtimes {
		opts := imagePruneOptions(app.Engines(), cfg, rt, req.MinAge, now)
		rep.Runtimes = append(rep.Runtimes, pruneRuntime(ctx, rt, opts, req.Apply))
	}
	return rep, nil
}

// pruneAvailableRuntimes is the runtimes present (doctor's probe); a var so
// the no-runtime refusal is testable on a host that has one.
var pruneAvailableRuntimes = doctorRuntimes

// knownRuntime rejects a --runtime naming no runtime ctxloom knows ("" is
// every one), so a typo is a usage error rather than "no runtime available".
func knownRuntime(name string) error {
	if name == "" {
		return nil
	}
	var names []string
	for _, rt := range ociRuntimes() {
		if rt.Name() == name {
			return nil
		}
		names = append(names, rt.Name())
	}
	return fmt.Errorf("unknown container runtime %q (%s)", name, strings.Join(names, "|"))
}

// pruneRuntimes narrows the available runtimes to the one named ("" = all).
func pruneRuntimes(available []isolation.Runtime, name string) []isolation.Runtime {
	if name == "" {
		return available
	}
	for _, rt := range available {
		if rt.Name() == name {
			return []isolation.Runtime{rt}
		}
	}
	return nil
}

// imagePruneOptions builds one runtime's plan options: the refs this
// project's configured agents (and its default backend) resolve to are live.
func imagePruneOptions(reg engine.Registry, cfg *config.Config, rt isolation.Runtime, minAge time.Duration, now time.Time) isolation.ImagePruneOptions {
	opts := isolation.ImagePruneOptions{MinAge: minAge, Now: now}
	if cfg == nil {
		return opts
	}
	backends := doctorConfiguredEngines(reg, cfg)
	if b, _ := ResolveBackend(reg, cfg, ""); b != "" {
		backends = append(backends, b)
	}
	for _, b := range backends {
		if ref, ok := isolation.LiveImageRef(rt, b, launch.ImageConfigFor(cfg, engine.Name(b))); ok {
			opts.Live = append(opts.Live, ref)
		}
	}
	return opts
}

// pruneRuntime plans one runtime and, when apply, removes what the plan
// marks superseded.
func pruneRuntime(ctx context.Context, rt isolation.Runtime, opts isolation.ImagePruneOptions, apply bool) ContainerPruneRuntime {
	sec := ContainerPruneRuntime{Runtime: rt.Name()}
	plan, err := isolation.PlanImagePrune(ctx, rt, opts)
	if err != nil {
		sec.Error = err.Error()
		return sec
	}
	sec.Unowned = plan.Unowned
	failed := map[string]string{}
	if apply {
		res := isolation.ApplyImagePrune(ctx, rt, plan)
		for _, f := range res.Failed {
			failed[f.Image.ID] = f.Err.Error()
		}
	}
	for _, v := range plan.Verdicts {
		line := pruneLine(v, apply, failed)
		if line.Action == PruneRemove || line.Action == PruneRemoved {
			sec.Bytes += line.Bytes
		}
		sec.Images = append(sec.Images, line)
	}
	return sec
}

// pruneLine renders one verdict as the action taken (or planned).
func pruneLine(v isolation.ImageVerdict, apply bool, failed map[string]string) ContainerPruneImage {
	line := ContainerPruneImage{Ref: pruneRef(v.Image), Reason: v.Keep, Created: v.Image.Created, Bytes: v.Image.Size}
	switch {
	case v.Keep != isolation.Superseded:
		line.Action = PruneKeep
	case !apply:
		line.Action = PruneRemove
	case failed[v.Image.ID] != "":
		line.Action, line.Error = PruneFailed, failed[v.Image.ID]
	default:
		line.Action = PruneRemoved
	}
	return line
}

// pruneRef names an image by its first ref, or its ID when dangling.
func pruneRef(img isolation.OwnedImage) string {
	if len(img.Refs) > 0 {
		return img.Refs[0]
	}
	return img.ID
}

// doctorCheckSupersededImages is the read-only nudge toward `container
// prune`: it plans every runtime present and reports how many superseded
// agent images each holds and their unique-layer bytes. It removes nothing.
func doctorCheckSupersededImages(ctx context.Context, runtimes []isolation.Runtime, plan func(context.Context, isolation.Runtime) (isolation.ImagePrunePlan, error)) DoctorCheck {
	const marker = "DOCTOR-CHECK-SUPERSEDED-IMAGES-x4"
	if len(runtimes) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: "no container runtime on this host; nothing to check"}
	}
	var found []string
	var total int
	var bytes int64
	for _, rt := range runtimes {
		p, err := plan(ctx, rt)
		if err != nil {
			return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf("could not list %s images: %v", rt.Name(), err)}
		}
		if n := len(p.Superseded()); n > 0 {
			total += n
			bytes += p.Reclaimable()
			found = append(found, fmt.Sprintf("%d %s", n, rt.Name()))
		}
	}
	if total == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "no superseded agent images"}
	}
	return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
		"%d superseded agent image(s) (%s), %s reclaimable — run `ctxloom container prune --apply`",
		total, strings.Join(found, ", "), FormatImageBytes(bytes))}
}

// FormatImageBytes renders an image size in the decimal units docker and
// podman report sizes in, so a figure here reads the same as theirs.
func FormatImageBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
