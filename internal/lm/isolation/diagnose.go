package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Diagnosis is the container-capability report `ctxloom container check`
// renders: each field answers one axis of "would `runtime: container`
// actually launch here, and if not, why". Read-only — Diagnose never builds
// an image or changes state, and it never errors (a diagnostic that blocks
// startup would violate the fault-tolerance contract).
type Diagnosis struct {
	// InContainer + Markers: whether THIS process runs inside a container
	// (dev container, CI, pod) and which heuristics matched.
	InContainer bool     `json:"in_container"`
	Markers     []string `json:"markers,omitempty"`
	// Runtime is the selected container runtime ("docker" | "podman" |
	// "none"); Reachable whether its daemon answered.
	Runtime   string `json:"runtime"`
	Reachable bool   `json:"reachable"`
	// Image is the backend's agent image reference; ImagePresent whether it
	// exists locally (Diagnose never builds it).
	Image        string `json:"image,omitempty"`
	ImagePresent bool   `json:"image_present"`
	// ImageStale is meaningful only when ImagePresent + locally buildable: the
	// image's baked ctxloom/companion binaries no longer match the host's, so
	// the next containerized run rebuilds it. Diagnose reports; it never builds.
	ImageStale bool `json:"image_stale,omitempty"`
	// SharedFS reports whether the daemon shares this process's filesystem:
	// "ok" (marker probe passed), "mismatch: …" (probe failed — the
	// docker-outside-of-docker signature), or "unprobed: …" (no local image
	// to probe with; an advisory heuristic is included).
	SharedFS string `json:"shared_fs"`
	// Guidance is the actionable summary, one line per finding.
	Guidance []string `json:"guidance,omitempty"`
}

// Diagnose builds the container-capability report for the named backend.
// Behavior-accurate where possible (daemon reachability, the marker probe
// against a present image) and clearly-labeled advisory where not.
func Diagnose(ctx context.Context, backend string, img ImageConfig) Diagnosis {
	d := Diagnosis{Markers: containerMarkers(), SharedFS: "unprobed: no runtime"}
	d.InContainer = len(d.Markers) > 0

	rt := SelectRuntime("")
	if _, isHost := rt.(Host); isHost {
		d.Runtime = "none"
		d.Guidance = append(d.Guidance,
			"no container runtime is reachable: `runtime: container` agents abort startup (exit 3) unless --degraded, which runs them on the host"+noRuntimeHint())
		return d
	}
	d.Runtime = rt.Name()
	d.Reachable = true

	c := containerFor(rt, backend, img)
	d.Image = c.image
	d.ImagePresent = c.imagePresent(ctx)

	sources, devBase, devErr := c.containerBuildSources("")
	if devErr != nil {
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("project devcontainer auto-detection failed (%v); a containerized run builds without it (or set isolation_devcontainer_service, or opt out with isolation_devcontainer_base: false)", devErr))
	}

	if d.ImagePresent {
		// Staleness is meaningful only for a locally-buildable image; a
		// user-owned isolation_images override (no build sources) is run as-is
		// and never flagged stale.
		if len(sources) > 0 && imageStale(c.imageLabels(ctx), c.provenanceFor(devBase)) {
			d.ImageStale = true
			d.Guidance = append(d.Guidance,
				fmt.Sprintf("agent image %s was built from different ctxloom/companion binaries (or base Containerfile/devcontainer/engine-set config) than are installed now; the next containerized run rebuilds it (or run `ctxloom container build %s`)", c.image, backend))
		}
		diagnoseProbe(ctx, rt, c.image, &d)
	} else {
		diagnoseAdvisory(ctx, rt, &d)
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("agent image %s is not present; a containerized run builds it on first use (or run `ctxloom container build %s`)", c.image, backend))
	}
	return d
}

// diagnoseProbe runs the definitive marker probe against the present image
// and folds the outcome into the diagnosis.
func diagnoseProbe(ctx context.Context, rt Runtime, image string, d *Diagnosis) {
	perr := sharedFSProbe(ctx, rt, image)
	if perr == nil {
		d.SharedFS = "ok"
		d.Guidance = append(d.Guidance, "containerized agents can launch here (`runtime: container`)")
		return
	}
	var mism *sharedFSMismatch
	if errors.As(perr, &mism) {
		// A DEFINITIVE negative: the probe ran and proved the fs is not shared.
		d.SharedFS = "mismatch: " + perr.Error()
		g := "the daemon does NOT share this process's filesystem: containerized agents cannot mount this project"
		if d.InContainer {
			g += " — this looks like docker-outside-of-docker; enable the dev container docker-in-docker feature, or keep agents on `runtime: host`"
		}
		d.Guidance = append(d.Guidance, g)
		return
	}
	// The probe could not RUN (daemon down/cold, image unreadable, timeout) —
	// that is not a sharing verdict, so do not cry docker-outside-of-docker.
	// Surface the real cause and invite a re-check (the failure was not cached).
	d.SharedFS = "unprobed: " + perr.Error()
	d.Guidance = append(d.Guidance,
		"the shared-filesystem probe could not run; resolve the error above (daemon reachable? image pullable?) and re-run `ctxloom container check`")
}

// diagnoseAdvisory fills SharedFS with the cheap no-image heuristic: a daemon
// whose reported name matches our hostname is almost certainly local (true
// DinD or a plain host); a differing name inside a container suggests the
// host's daemon. Labeled advisory — the marker probe is the real answer.
func diagnoseAdvisory(ctx context.Context, rt Runtime, d *Diagnosis) {
	name, err := daemonName(ctx, rt)
	host, herr := os.Hostname()
	switch {
	case err != nil || herr != nil:
		d.SharedFS = "unprobed: no local image to probe with"
	case name == host:
		d.SharedFS = "unprobed: likely shared (advisory: daemon name matches this hostname)"
	default:
		d.SharedFS = fmt.Sprintf("unprobed: possibly the host's daemon (advisory: daemon name %q != hostname %q)", name, host)
		if d.InContainer {
			d.Guidance = append(d.Guidance,
				"the daemon may be the host's (docker-outside-of-docker); containerized agents would fail their filesystem probe and abort startup (exit 3) unless --degraded")
		}
	}
}

// daemonInfoTimeout bounds one `<runtime> info` call: the advisory heuristic is
// best-effort, so a wedged daemon degrades it (falls to "unprobed") rather than
// hanging `ctxloom container check` on a caller ctx that may carry no deadline.
const daemonInfoTimeout = 10 * time.Second

// daemonName asks the runtime for its daemon's reported host name. It routes
// through the probeExec seam (so the whole diagnose path is testable without a
// runtime) under its own timeout, and selects a template that exists on BOTH
// docker and podman — docker exposes the name at the top level, podman under
// Host — so podman does not fail the template and silently break the advisory.
func daemonName(ctx context.Context, rt Runtime) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, daemonInfoTimeout)
	defer cancel()
	out, err := probeExec(cctx, rt.Binary(), []string{"info", "--format", daemonNameTemplate(rt)})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// daemonNameTemplate picks the `info` Go-template field that reports the daemon
// host's name per runtime. `docker info` exposes it as top-level {{.Name}};
// `podman info` has no top-level Name (that template is an execution error) and
// carries the host name under {{.Host.Hostname}}.
func daemonNameTemplate(rt Runtime) string {
	if rt.Name() == "podman" {
		return "{{.Host.Hostname}}"
	}
	return "{{.Name}}"
}
