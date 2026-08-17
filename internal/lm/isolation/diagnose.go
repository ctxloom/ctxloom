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
	// ImageStale is true only when staleness was CHECKED and came back stale:
	// the image is present, locally buildable, and its baked ctxloom/companion
	// binaries no longer match the host's, so the next containerized run
	// rebuilds it. Diagnose reports; it never builds.
	//
	// A false is NOT the same as "verified up to date". Being a plain bool it
	// cannot express the third state — a user-owned isolation_images override
	// is never inspected, and an unresolvable expected provenance leaves
	// nothing to compare against — and both of those report false. Read
	// Guidance, which always names which case produced it; diagnoseStaleness
	// is where that distinction is made.
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

	// ProbeRuntime, not SelectRuntime: a diagnosis reports what is REACHABLE,
	// so it must not filter by an ownership mode nobody asked for here. The
	// report names the runtime; the ownership demand belongs to a run.
	rt := ProbeRuntime("")
	if _, isHost := rt.(Host); isHost {
		d.Runtime = "none"
		d.Guidance = append(d.Guidance,
			"no container runtime is reachable: `runtime: container-rootless` / `runtime: container-rootful` agents abort startup (exit 3) unless --degraded, which runs them on the host"+noRuntimeHint())
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
		diagnoseStaleness(ctx, c, backend, sources, devBase, &d)
		diagnoseProbe(ctx, rt, c.image, diagnoseProbeRoots(), &d)
	} else {
		diagnoseAdvisory(ctx, rt, &d)
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("agent image %s is not present; a containerized run builds it on first use (or run `ctxloom container build %s`)", c.image, backend))
	}
	return d
}

// diagnoseStaleness folds the present image's staleness verdict into the
// diagnosis. Staleness is meaningful only for a locally-buildable image whose
// EXPECTED provenance can actually be computed; a user-owned isolation_images
// override is run as-is and never inspected, and an unresolvable host binary
// or unreadable base Containerfile leaves nothing to compare against.
//
// ImageStale is a plain bool, so all three outcomes — stale, verified current,
// and NOT CHECKED — collapse onto two values, and both not-checked cases read
// as "false". Neither can be silent about it: the guidance names which case
// produced the false, so "not stale" is never mistaken for "verified up to
// date". Diagnose reports; it never builds.
func diagnoseStaleness(ctx context.Context, c Container, backend string, sources []buildSource, devBase *baseStage, d *Diagnosis) {
	if len(sources) == 0 {
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("agent image %s is a user-owned override (isolation_images): ctxloom runs it as-is and never inspects or rebuilds it, so its staleness is NOT CHECKED here", c.image))
		return
	}
	wantProvenance := c.provenanceFor(devBase)
	if wantProvenance == "" {
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("agent image %s: staleness could not be checked — the expected provenance is unresolvable on this host (the running ctxloom/companion binaries or the base Containerfile could not be read), so a containerized run cannot tell whether this image matches", c.image))
		return
	}
	if imageStale(c.imageLabels(ctx), wantProvenance) {
		d.ImageStale = true
		d.Guidance = append(d.Guidance,
			fmt.Sprintf("agent image %s was built from different ctxloom/companion binaries (or base Containerfile/devcontainer/engine-set config) than are installed now; the next containerized run rebuilds it (or run `ctxloom container build %s`)", c.image, backend))
	}
}

// diagnoseProbeRoots is `ctxloom container check`'s best-effort mount-root
// set: unlike PrepareWorkspace's real gate, Diagnose is read-only and has no
// prepared workspace to draw a mount set from (it never resolves auth,
// creates scratch, or mirrors a gitdir — that would make a read-only
// diagnostic have side effects). The current working directory is the best
// available stand-in for "the project this check is being run against" (the
// command's own doc says to run it inside the project), so probing it is
// still a real improvement over the old single throwaway tempdir: it answers
// "would the actual project directory share", not "does os.TempDir() share".
// It does NOT cover a future run's auth-mount or gitdir-mirror roots — those
// require the side-effecting resolution only the real gate performs.
func diagnoseProbeRoots() []string {
	if wd, err := os.Getwd(); err == nil {
		return []string{wd}
	}
	return nil
}

// diagnoseProbe runs the definitive marker probe against the present image
// and folds the outcome into the diagnosis.
func diagnoseProbe(ctx context.Context, rt Runtime, image string, roots []string, d *Diagnosis) {
	perr := sharedFSProbe(ctx, rt, image, roots)
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
